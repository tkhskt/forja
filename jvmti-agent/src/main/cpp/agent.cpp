// forja JVMTI agent — attaches to a debuggable Android app and inserts the
// forja RulesInterceptor into the interceptor chain of every OkHttpClient
// instance in the process. Uses only standard JVMTI (no dependency on
// Android Studio internal APIs like NetworkInspector). Zero-touch: requires
// no build changes on the host app.
//
// Flow:
//   am attach-agent <pkg> "<so_path>=<dex_path>"
//     ↓
//   Agent_OnAttach(vm, options="<dex_path>")
//     ↓
//   1. Acquire JVMTI capabilities (can_tag_objects,
//      can_generate_breakpoint_events, can_generate_method_exit_events).
//   2. Get the app Context via ActivityThread.currentActivityThread().getApplication().
//   3. context.getClassLoader() → the app's PathClassLoader (appLoader).
//   4. Load Bootstrap through a child DexClassLoader(dexPath, codeCacheDir,
//      null, appLoader).
//   5. Bootstrap.inject(appLoader, dexPath, codeCacheDir) merges the DEX into
//      appLoader.
//   6. appLoader.loadClass("okhttp3.OkHttpClient") + JVMTI's
//      IterateOverInstancesOfClass + GetObjectsWithTags to collect every
//      existing instance.
//   7. Call Bootstrap.modifyExistingClients(instances, appLoader) which
//      replaces each instance's `interceptors` field with
//      `[RulesInterceptor] + original list`.
//
// For NEW clients (OkHttpClients created via Builder.build() AFTER attach),
// we set a breakpoint on Builder.build() and use per-thread MethodExit to
// pick up the returned instance and run the same swap. On devices where
// `can_generate_*` is not granted, only existing instances are rewritten
// (= degraded mode).

#include <jni.h>
#include "jvmti.h"
#include <android/log.h>
#include <cstring>
#include <string>

#define LOG_TAG "ForjaAgent"
#define LOGI(...) __android_log_print(ANDROID_LOG_INFO,  LOG_TAG, __VA_ARGS__)
#define LOGW(...) __android_log_print(ANDROID_LOG_WARN,  LOG_TAG, __VA_ARGS__)
#define LOGE(...) __android_log_print(ANDROID_LOG_ERROR, LOG_TAG, __VA_ARGS__)

namespace {

constexpr const char* kOkHttpClientDotted = "okhttp3.OkHttpClient";
constexpr const char* kBuilderDotted      = "okhttp3.OkHttpClient$Builder";
constexpr const char* kBuilderBuildName   = "build";
constexpr const char* kBuilderBuildSig    = "()Lokhttp3/OkHttpClient;";
constexpr const char* kBootstrapClass     = "com.tkhskt.forja.agent.Bootstrap";

// Globals used by the new-client hook (Breakpoint + MethodExit).
// All const-after-init — only Agent_OnAttach writes to them.
JavaVM*   gVm                  = nullptr;
jvmtiEnv* gJvmti               = nullptr;
jobject   gAppLoaderGlobalRef  = nullptr;   // app PathClassLoader (global ref)
jmethodID gBuildMid            = nullptr;   // okhttp3.OkHttpClient$Builder.build()
jclass    gBootstrapClassRef   = nullptr;   // com.tkhskt.forja.agent.Bootstrap (global ref)
jmethodID gBootstrapModifyMid  = nullptr;   // Bootstrap.modifyExistingClients([Ljava/lang/Object;Ljava/lang/ClassLoader;)I
jclass    gObjectClassRef      = nullptr;   // java.lang.Object (global ref) — array element type

// --- Exception-checking helpers ----------------------------------------

/// If a JNI exception is pending, log Throwable.toString() plus up to 12
/// stack-trace frames to logcat (under the ForjaAgent tag) and clear it.
void DescribePendingException(JNIEnv* env, const char* where) {
    if (!env->ExceptionCheck()) {
        LOGE("%s: failed without pending exception", where);
        return;
    }
    jthrowable exc = env->ExceptionOccurred();
    env->ExceptionClear();
    if (exc == nullptr) {
        LOGE("%s: ExceptionOccurred returned null", where);
        return;
    }
    jclass thClass = env->FindClass("java/lang/Throwable");
    if (thClass == nullptr) {
        env->ExceptionClear();
        LOGE("%s: (no Throwable class)", where);
        env->DeleteLocalRef(exc);
        return;
    }

    jmethodID toStringMid = env->GetMethodID(thClass, "toString", "()Ljava/lang/String;");
    auto msg = static_cast<jstring>(env->CallObjectMethod(exc, toStringMid));
    if (env->ExceptionCheck()) env->ExceptionClear();
    if (msg != nullptr) {
        const char* cmsg = env->GetStringUTFChars(msg, nullptr);
        LOGE("%s: %s", where, cmsg ? cmsg : "(null toString)");
        if (cmsg) env->ReleaseStringUTFChars(msg, cmsg);
        env->DeleteLocalRef(msg);
    } else {
        LOGE("%s: (toString returned null)", where);
    }

    jmethodID getStMid = env->GetMethodID(
        thClass, "getStackTrace", "()[Ljava/lang/StackTraceElement;");
    auto frames = static_cast<jobjectArray>(env->CallObjectMethod(exc, getStMid));
    if (env->ExceptionCheck()) {
        env->ExceptionClear();
    } else if (frames != nullptr) {
        jsize n = env->GetArrayLength(frames);
        jsize limit = n < 12 ? n : 12;
        for (jsize i = 0; i < limit; ++i) {
            jobject frame = env->GetObjectArrayElement(frames, i);
            if (frame == nullptr) continue;
            jclass fClass = env->GetObjectClass(frame);
            jmethodID fToString = env->GetMethodID(fClass, "toString", "()Ljava/lang/String;");
            auto fStr = static_cast<jstring>(env->CallObjectMethod(frame, fToString));
            if (env->ExceptionCheck()) {
                env->ExceptionClear();
            } else if (fStr != nullptr) {
                const char* cstr = env->GetStringUTFChars(fStr, nullptr);
                if (cstr) {
                    LOGE("    at %s", cstr);
                    env->ReleaseStringUTFChars(fStr, cstr);
                }
                env->DeleteLocalRef(fStr);
            }
            env->DeleteLocalRef(fClass);
            env->DeleteLocalRef(frame);
        }
        if (n > limit) LOGE("    ... %d more frame(s)", static_cast<int>(n - limit));
        env->DeleteLocalRef(frames);
    }

    env->DeleteLocalRef(thClass);
    env->DeleteLocalRef(exc);
}

/// Macro for "JNI call returned nullptr" and "an exception is pending" as
/// two independent failure modes.
#define CHECK_JNI_OBJ(env, obj, where)                                  \
    do {                                                                \
        if ((env)->ExceptionCheck()) {                                  \
            DescribePendingException((env), (where));                   \
            return false;                                               \
        }                                                               \
        if ((obj) == nullptr) {                                         \
            LOGE("%s: returned null without exception", (where));       \
            return false;                                               \
        }                                                               \
    } while (0)

// --- App ClassLoader retrieval + DEX merge -----------------------------

/// Returns ActivityThread.currentActivityThread().getApplication().getClassLoader().
jobject GetAppClassLoader(JNIEnv* env) {
    jclass atClass = env->FindClass("android/app/ActivityThread");
    if (atClass == nullptr) { env->ExceptionClear(); return nullptr; }

    jmethodID currentAT = env->GetStaticMethodID(
        atClass, "currentActivityThread", "()Landroid/app/ActivityThread;");
    if (currentAT == nullptr) { env->ExceptionClear(); return nullptr; }
    jobject at = env->CallStaticObjectMethod(atClass, currentAT);
    if (at == nullptr || env->ExceptionCheck()) { env->ExceptionClear(); return nullptr; }

    jmethodID getApp = env->GetMethodID(atClass, "getApplication", "()Landroid/app/Application;");
    if (getApp == nullptr) { env->ExceptionClear(); return nullptr; }
    jobject app = env->CallObjectMethod(at, getApp);
    if (app == nullptr || env->ExceptionCheck()) { env->ExceptionClear(); return nullptr; }

    jclass ctxClass = env->FindClass("android/content/Context");
    if (ctxClass == nullptr) { env->ExceptionClear(); return nullptr; }
    jmethodID getLoader = env->GetMethodID(ctxClass, "getClassLoader", "()Ljava/lang/ClassLoader;");
    if (getLoader == nullptr) { env->ExceptionClear(); return nullptr; }
    jobject loader = env->CallObjectMethod(app, getLoader);
    if (loader == nullptr) { env->ExceptionClear(); return nullptr; }
    return loader;
}

/// Returns Context.getCodeCacheDir().getAbsolutePath(). Falls back to
/// "/data/local/tmp" on failure.
std::string GetCodeCacheDir(JNIEnv* env) {
    const char* fallback = "/data/local/tmp";

    jclass atClass = env->FindClass("android/app/ActivityThread");
    if (atClass == nullptr) { env->ExceptionClear(); return fallback; }
    jmethodID currentAT = env->GetStaticMethodID(
        atClass, "currentActivityThread", "()Landroid/app/ActivityThread;");
    jobject at = env->CallStaticObjectMethod(atClass, currentAT);
    if (at == nullptr) { env->ExceptionClear(); return fallback; }
    jmethodID getApp = env->GetMethodID(atClass, "getApplication", "()Landroid/app/Application;");
    jobject app = env->CallObjectMethod(at, getApp);
    if (app == nullptr) { env->ExceptionClear(); return fallback; }

    jclass ctxClass = env->FindClass("android/content/Context");
    jmethodID getCacheDir = env->GetMethodID(ctxClass, "getCodeCacheDir", "()Ljava/io/File;");
    if (getCacheDir == nullptr) { env->ExceptionClear(); return fallback; }
    jobject dir = env->CallObjectMethod(app, getCacheDir);
    if (dir == nullptr) { env->ExceptionClear(); return fallback; }

    jclass fileClass = env->FindClass("java/io/File");
    jmethodID getAbs = env->GetMethodID(fileClass, "getAbsolutePath", "()Ljava/lang/String;");
    auto str = static_cast<jstring>(env->CallObjectMethod(dir, getAbs));
    if (str == nullptr) { env->ExceptionClear(); return fallback; }

    const char* cstr = env->GetStringUTFChars(str, nullptr);
    std::string result(cstr ? cstr : fallback);
    if (cstr) env->ReleaseStringUTFChars(str, cstr);
    return result;
}

/// Loads Bootstrap via a child DexClassLoader and calls Bootstrap.inject to
/// merge the DEX into appLoader.
bool MergeOurDexIntoApp(JNIEnv* env, jobject appLoader, const char* dexPath, const std::string& optDir) {
    jclass dexCL = env->FindClass("dalvik/system/DexClassLoader");
    CHECK_JNI_OBJ(env, dexCL, "FindClass dalvik/system/DexClassLoader");

    jmethodID dexCLCtor = env->GetMethodID(
        dexCL, "<init>",
        "(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;Ljava/lang/ClassLoader;)V");
    if (dexCLCtor == nullptr) {
        DescribePendingException(env, "GetMethodID DexClassLoader.<init>");
        return false;
    }

    jstring jDexPath = env->NewStringUTF(dexPath);
    jstring jOptDir  = env->NewStringUTF(optDir.c_str());
    LOGI("constructing DexClassLoader(dex=%s, optDir=%s)", dexPath, optDir.c_str());

    jobject bootLoader = env->NewObject(dexCL, dexCLCtor, jDexPath, jOptDir, nullptr, appLoader);
    CHECK_JNI_OBJ(env, bootLoader, "new DexClassLoader");

    jclass clClass = env->FindClass("java/lang/ClassLoader");
    CHECK_JNI_OBJ(env, clClass, "FindClass java/lang/ClassLoader");

    jmethodID loadClass = env->GetMethodID(
        clClass, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;");
    if (loadClass == nullptr) {
        DescribePendingException(env, "GetMethodID ClassLoader.loadClass");
        return false;
    }

    jstring jBootstrapName = env->NewStringUTF(kBootstrapClass);
    auto bootstrapClazz = static_cast<jclass>(
        env->CallObjectMethod(bootLoader, loadClass, jBootstrapName));
    CHECK_JNI_OBJ(env, bootstrapClazz, "DexClassLoader.loadClass(Bootstrap)");
    LOGI("loaded Bootstrap class via child DexClassLoader");

    jmethodID injectMid = env->GetStaticMethodID(
        bootstrapClazz, "inject",
        "(Ljava/lang/ClassLoader;Ljava/lang/String;Ljava/lang/String;)V");
    if (injectMid == nullptr) {
        DescribePendingException(env, "GetStaticMethodID Bootstrap.inject");
        return false;
    }
    env->CallStaticVoidMethod(bootstrapClazz, injectMid, appLoader, jDexPath, jOptDir);
    if (env->ExceptionCheck()) {
        DescribePendingException(env, "Bootstrap.inject");
        return false;
    }
    LOGI("Bootstrap.inject succeeded");
    return true;
}

// --- Modify existing OkHttpClient instances ----------------------------

/// Helper that loads an arbitrary class via appLoader. Takes a dotted name
/// (e.g. "okhttp3.OkHttpClient").
jclass LoadAppClass(JNIEnv* env, jobject appLoader, const char* dottedName) {
    jclass clClass = env->FindClass("java/lang/ClassLoader");
    if (clClass == nullptr) { env->ExceptionClear(); return nullptr; }
    jmethodID loadClass = env->GetMethodID(
        clClass, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;");
    if (loadClass == nullptr) { env->ExceptionClear(); return nullptr; }
    jstring jName = env->NewStringUTF(dottedName);
    auto cls = static_cast<jclass>(env->CallObjectMethod(appLoader, loadClass, jName));
    env->DeleteLocalRef(jName);
    env->DeleteLocalRef(clClass);
    return cls;
}

/// IterateOverInstancesOfClass callback. Tags every matching object with 1.
jvmtiIterationControl JNICALL TagAllInstancesCallback(
        jlong /*class_tag*/, jlong /*size*/, jlong* tag_ptr, void* /*user_data*/) {
    *tag_ptr = 1;
    return JVMTI_ITERATION_CONTINUE;
}

/// Resolve the Bootstrap class + Bootstrap.modifyExistingClients jmethodID
/// and cache them as global refs in gBootstrap*** so subsequent callbacks
/// can invoke them immediately. Called once from Agent_OnAttach.
bool ResolveAndCacheBootstrap(JNIEnv* env, jobject appLoader) {
    jclass bootLocal = LoadAppClass(env, appLoader, kBootstrapClass);
    if (env->ExceptionCheck() || bootLocal == nullptr) {
        DescribePendingException(env, "appLoader.loadClass(Bootstrap)");
        return false;
    }
    gBootstrapClassRef = static_cast<jclass>(env->NewGlobalRef(bootLocal));
    env->DeleteLocalRef(bootLocal);

    gBootstrapModifyMid = env->GetStaticMethodID(
        gBootstrapClassRef, "modifyExistingClients",
        "([Ljava/lang/Object;Ljava/lang/ClassLoader;)I");
    if (gBootstrapModifyMid == nullptr) {
        DescribePendingException(env, "GetStaticMethodID Bootstrap.modifyExistingClients");
        return false;
    }

    jclass objLocal = env->FindClass("java/lang/Object");
    if (objLocal == nullptr) { env->ExceptionClear(); return false; }
    gObjectClassRef = static_cast<jclass>(env->NewGlobalRef(objLocal));
    env->DeleteLocalRef(objLocal);
    return true;
}

/// Assemble jobject[1] = {client} and call Bootstrap.modifyExistingClients.
/// Precondition: ResolveAndCacheBootstrap has succeeded and
/// gAppLoaderGlobalRef is non-null.
int InvokeModifyForClients(JNIEnv* env, jobjectArray clients) {
    jint modified = env->CallStaticIntMethod(
        gBootstrapClassRef, gBootstrapModifyMid, clients, gAppLoaderGlobalRef);
    if (env->ExceptionCheck()) {
        DescribePendingException(env, "Bootstrap.modifyExistingClients");
        return -1;
    }
    return static_cast<int>(modified);
}

/// Collect every existing OkHttpClient instance and pass them to
/// Bootstrap.modifyExistingClients. Runs once, right after Agent_OnAttach.
bool ModifyExistingOkHttpClients(JNIEnv* env, jvmtiEnv* jvmti, jobject appLoader) {
    jclass okClass = LoadAppClass(env, appLoader, kOkHttpClientDotted);
    if (env->ExceptionCheck()) {
        DescribePendingException(env, "appLoader.loadClass(okhttp3.OkHttpClient)");
        return false;
    }
    if (okClass == nullptr) {
        LOGW("%s is not loaded in app classpath; nothing to modify", kOkHttpClientDotted);
        return true;  // App doesn't use OkHttp; not an error.
    }

    // 1) Tag every okhttp3.OkHttpClient instance with 1.
    if (auto err = jvmti->IterateOverInstancesOfClass(
            okClass, JVMTI_HEAP_OBJECT_EITHER, &TagAllInstancesCallback, nullptr);
        err != JVMTI_ERROR_NONE) {
        LOGE("IterateOverInstancesOfClass failed: %d", err);
        return false;
    }

    // 2) Collect all objects with tag=1 in one shot.
    jlong tags[] = { 1 };
    jint count = 0;
    jobject* objects = nullptr;
    if (auto err = jvmti->GetObjectsWithTags(1, tags, &count, &objects, nullptr);
        err != JVMTI_ERROR_NONE) {
        LOGE("GetObjectsWithTags failed: %d", err);
        return false;
    }
    LOGI("IterateOverInstancesOfClass returned %d %s instance(s)",
         static_cast<int>(count), kOkHttpClientDotted);

    if (count == 0) {
        if (objects != nullptr) jvmti->Deallocate(reinterpret_cast<unsigned char*>(objects));
        LOGI("no existing OkHttpClient instances yet; nothing to modify");
        return true;
    }

    // 3) Move the jobject[] into a Java Object[].
    jobjectArray jArr = env->NewObjectArray(count, gObjectClassRef, nullptr);
    if (jArr == nullptr) {
        env->ExceptionClear();
        LOGE("NewObjectArray(%d) failed", static_cast<int>(count));
        jvmti->Deallocate(reinterpret_cast<unsigned char*>(objects));
        return false;
    }
    for (jint i = 0; i < count; ++i) {
        env->SetObjectArrayElement(jArr, i, objects[i]);
    }
    jvmti->Deallocate(reinterpret_cast<unsigned char*>(objects));

    // 4) Clear the tags so the next IterateOverInstancesOfClass call starts clean.
    auto clearCb = [](jlong, jlong, jlong* t, void*) -> jvmtiIterationControl {
        *t = 0;
        return JVMTI_ITERATION_CONTINUE;
    };
    jvmti->IterateOverInstancesOfClass(okClass, JVMTI_HEAP_OBJECT_TAGGED, clearCb, nullptr);

    // 5) Invoke Bootstrap.modifyExistingClients.
    int modified = InvokeModifyForClients(env, jArr);
    if (modified < 0) return false;
    LOGI("modifyExistingClients (existing): %d / %d instance(s) updated",
         modified, static_cast<int>(count));
    env->DeleteLocalRef(jArr);
    return true;
}

// --- New-client hook (Breakpoint + per-thread MethodExit) --------------

/// Called right after Builder.build() returns. Routes the new OkHttpClient
/// through modifyExistingClients.
void HandleNewClient(JNIEnv* env, jobject newClient) {
    if (newClient == nullptr) return;
    jobjectArray jArr = env->NewObjectArray(1, gObjectClassRef, newClient);
    if (jArr == nullptr) {
        env->ExceptionClear();
        LOGE("NewObjectArray(1) failed (new client path)");
        return;
    }
    int modified = InvokeModifyForClients(env, jArr);
    if (modified > 0) {
        LOGI("modifyExistingClients (new): 1 instance via Builder.build()");
    }
    env->DeleteLocalRef(jArr);
}

/// Fires at the entry of Builder.build(). Enables MethodExit just for this
/// thread, so OnMethodExit catches the build() return on the same thread.
void JNICALL OnBreakpoint(
        jvmtiEnv* jvmti, JNIEnv* /*env*/, jthread thread, jmethodID method, jlocation /*loc*/) {
    if (method != gBuildMid) return;
    jvmtiError jerr = jvmti->SetEventNotificationMode(
        JVMTI_ENABLE, JVMTI_EVENT_METHOD_EXIT, thread);
    if (jerr != JVMTI_ERROR_NONE) {
        LOGE("SetEventNotificationMode(ENABLE METHOD_EXIT) failed: %d", jerr);
    }
}

void JNICALL OnMethodExit(
        jvmtiEnv* jvmti, JNIEnv* env, jthread thread, jmethodID method,
        jboolean was_popped_by_exception, jvalue return_value) {
    // Skip returns from methods that aren't build() — the common case,
    // exited with minimal cost.
    if (method != gBuildMid) return;

    // If build() exited via an exception, there is no new client to modify
    // — just disable.
    if (!was_popped_by_exception && return_value.l != nullptr) {
        HandleNewClient(env, return_value.l);
    }
    // Our target (build) returned, so disable this thread's MethodExit.
    // Nested calls aren't expected (OkHttp's Builder doesn't do that).
    jvmtiError jerr = jvmti->SetEventNotificationMode(
        JVMTI_DISABLE, JVMTI_EVENT_METHOD_EXIT, thread);
    if (jerr != JVMTI_ERROR_NONE) {
        LOGE("SetEventNotificationMode(DISABLE METHOD_EXIT) failed: %d", jerr);
    }
}

/// Resolve Builder.build()'s jmethodID and arm a breakpoint at its entry.
/// On failure, the agent continues in existing-only mode.
bool SetupBuilderBuildHook(JNIEnv* env, jvmtiEnv* jvmti, jobject appLoader) {
    jclass builderClass = LoadAppClass(env, appLoader, kBuilderDotted);
    if (env->ExceptionCheck()) {
        DescribePendingException(env, "appLoader.loadClass(OkHttpClient$Builder)");
        return false;
    }
    if (builderClass == nullptr) {
        LOGW("%s not in classpath; new-client hook skipped", kBuilderDotted);
        return false;
    }

    gBuildMid = env->GetMethodID(builderClass, kBuilderBuildName, kBuilderBuildSig);
    if (gBuildMid == nullptr) {
        DescribePendingException(env, "GetMethodID Builder.build");
        return false;
    }

    if (auto jerr = jvmti->SetBreakpoint(gBuildMid, 0); jerr != JVMTI_ERROR_NONE) {
        LOGE("SetBreakpoint(Builder.build, 0) failed: %d", jerr);
        return false;
    }
    if (auto jerr = jvmti->SetEventNotificationMode(
            JVMTI_ENABLE, JVMTI_EVENT_BREAKPOINT, nullptr);
        jerr != JVMTI_ERROR_NONE) {
        LOGE("SetEventNotificationMode(ENABLE BREAKPOINT) failed: %d", jerr);
        return false;
    }
    LOGI("breakpoint armed on %s.build()", kBuilderDotted);
    return true;
}

// --- Init --------------------------------------------------------------

/// can_tag_objects is required (for the heap walk).
/// can_generate_breakpoint_events / can_generate_method_exit_events are
/// needed for the new-client hook but some ART devices won't grant them.
/// When that happens we fall back to existing-only mode.
///
/// Returns: hookable (= did we get the new-client capabilities).
bool AcquireCapabilities(jvmtiEnv* jvmti) {
    jvmtiCapabilities full = {};
    full.can_tag_objects = 1;
    full.can_generate_breakpoint_events = 1;
    full.can_generate_method_exit_events = 1;

    jvmtiError jerr = jvmti->AddCapabilities(&full);
    if (jerr == JVMTI_ERROR_NONE) {
        LOGI("capabilities: tag_objects=YES, breakpoint=YES, method_exit=YES");
        return true;
    }
    LOGW("AddCapabilities(full) failed: %d — retrying without breakpoint/method_exit", jerr);

    jvmtiCapabilities minimal = {};
    minimal.can_tag_objects = 1;
    jerr = jvmti->AddCapabilities(&minimal);
    if (jerr != JVMTI_ERROR_NONE) {
        LOGE("AddCapabilities(tag_objects) failed: %d", jerr);
        return false;
    }
    LOGW("capabilities: tag_objects=YES, breakpoint=NO, method_exit=NO "
         "— new clients will NOT be picked up (existing-only mode)");
    return false;
}

jint InitAgent(JavaVM* vm, const char* options) {
    if (options == nullptr || options[0] == '\0') {
        LOGE("agent requires options=<absolute-path-to-agent-bundle.dex>");
        return JNI_ERR;
    }
    const std::string dexPath(options);

    jvmtiEnv* jvmti = nullptr;
    if (vm->GetEnv(reinterpret_cast<void**>(&jvmti), JVMTI_VERSION_1_2) != JNI_OK) {
        LOGE("GetEnv(JVMTI_VERSION_1_2) failed");
        return JNI_ERR;
    }
    gVm = vm;
    gJvmti = jvmti;

    // Acquire capabilities with fallback. can_tag_objects is the minimum.
    const bool hookable = AcquireCapabilities(jvmti);

    // If hookable, wire the Breakpoint / MethodExit callbacks. When the
    // capabilities weren't granted, SetEventCallbacks is still valid but a
    // no-op in effect.
    if (hookable) {
        jvmtiEventCallbacks cbs = {};
        cbs.Breakpoint = &OnBreakpoint;
        cbs.MethodExit = &OnMethodExit;
        if (auto jerr = jvmti->SetEventCallbacks(&cbs, sizeof(cbs)); jerr != JVMTI_ERROR_NONE) {
            LOGE("SetEventCallbacks failed: %d", jerr);
            return JNI_ERR;
        }
    }

    JNIEnv* env = nullptr;
    if (vm->GetEnv(reinterpret_cast<void**>(&env), JNI_VERSION_1_6) != JNI_OK) {
        LOGE("GetEnv(JNI_VERSION_1_6) failed");
        return JNI_ERR;
    }

    // 1) Resolve the app's PathClassLoader.
    jobject appLoader = GetAppClassLoader(env);
    if (appLoader == nullptr) {
        LOGE("could not resolve app ClassLoader (ActivityThread reflection)");
        return JNI_ERR;
    }
    gAppLoaderGlobalRef = env->NewGlobalRef(appLoader);

    // 2) Pick the optimized-dex location (the app's codeCacheDir).
    const std::string optDir = GetCodeCacheDir(env);
    LOGI("opt dir = %s", optDir.c_str());

    // 3) Bootstrap.inject() merges the DEX into appLoader.
    if (!MergeOurDexIntoApp(env, appLoader, dexPath.c_str(), optDir)) {
        LOGE("Bootstrap.inject failed (dex=%s)", dexPath.c_str());
        return JNI_ERR;
    }

    // 4) Resolve and cache Bootstrap.modifyExistingClients (used by both
    //    the existing and new-client paths).
    if (!ResolveAndCacheBootstrap(env, appLoader)) {
        LOGE("ResolveAndCacheBootstrap failed");
        return JNI_ERR;
    }

    // 4.5) Switch FileRulesProvider into self-destruct mode. rules.json is
    //      then consumed-and-deleted, so it fully disappears when the
    //      process dies. Failure isn't fatal — we just run in persistent
    //      mode in that case.
    {
        jmethodID selfDestructMid = env->GetStaticMethodID(
            gBootstrapClassRef, "enableSelfDestructMode",
            "(Ljava/lang/ClassLoader;)V");
        if (selfDestructMid == nullptr) {
            env->ExceptionClear();
            LOGW("Bootstrap.enableSelfDestructMode not found (running in persistent mode)");
        } else {
            env->CallStaticVoidMethod(gBootstrapClassRef, selfDestructMid, gAppLoaderGlobalRef);
            if (env->ExceptionCheck()) {
                DescribePendingException(env, "Bootstrap.enableSelfDestructMode");
                LOGW("self-destruct mode enable failed (running in persistent mode)");
            } else {
                LOGI("self-destruct mode enabled on FileRulesProvider");
            }
        }
    }

    // 5) Heap-walk existing OkHttpClient instances.
    if (!ModifyExistingOkHttpClients(env, jvmti, appLoader)) {
        LOGE("ModifyExistingOkHttpClients failed");
        return JNI_ERR;
    }

    // 6) Hook new build() calls. Armed only if the capabilities were granted.
    //    Failure isn't fatal — we degrade to existing-only mode.
    if (hookable) {
        if (!SetupBuilderBuildHook(env, jvmti, appLoader)) {
            LOGW("Builder.build() hook setup failed; running in existing-only mode");
        }
    }

    LOGI("forja JVMTI agent attached (dex=%s, new-client hook=%s)",
         dexPath.c_str(), hookable ? "ARMED" : "OFF");
    return JNI_OK;
}

}  // namespace

extern "C" JNIEXPORT jint JNICALL
Agent_OnAttach(JavaVM* vm, char* options, void* /*reserved*/) {
    return InitAgent(vm, options);
}

extern "C" JNIEXPORT jint JNICALL
Agent_OnLoad(JavaVM* vm, char* options, void* /*reserved*/) {
    return InitAgent(vm, options);
}
