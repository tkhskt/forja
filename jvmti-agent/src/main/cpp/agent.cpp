// forja JVMTI agent — attaches to a debuggable Android app and inserts the
// forja RulesInterceptor into the application interceptor chain of every
// OkHttpClient in the process.
//
// Mechanism (mirrors Android Studio's NetworkInspector / ArtTooling):
// instead of JVMTI breakpoints + per-thread MethodExit (which deoptimize the
// hooked method and run injection on the calling thread), we rewrite the dex
// bytecode of okhttp3.OkHttpClient.interceptors() with an *exit hook* using
// the slicer library. The hook forwards the method's return value through
//
//     com.tkhskt.forja.agent.Bootstrap.wrapInterceptors(List): List
//
// which prepends a RulesInterceptor. Because OkHttp calls interceptors() once
// per request to assemble the chain, this covers every client (existing and
// future) without a heap walk, without reflection on instance fields, and
// without a breakpoint — and the instrumented method stays JIT-compiled, so
// there is no sustained deopt and no main-thread stall on client creation.
//
// Flow:
//   am attach-agent <pkg> "<so_path>=<dex_path>"
//     -> Agent_OnAttach(vm, options="<dex_path>")
//        1. Acquire can_retransform_classes.
//        2. Resolve the app's PathClassLoader (ActivityThread reflection).
//        3. Bootstrap.inject() merges agent-bundle.dex into appLoader so
//           RulesInterceptor / FileRulesProvider / Bootstrap are visible
//           through the host app's classloader.
//        4. Enable self-destruct mode on FileRulesProvider.
//        5. Register a ClassFileLoadHook that instruments
//           okhttp3.OkHttpClient.interceptors() via slicer, then call
//           RetransformClasses on the already-loaded OkHttpClient to apply it.

#include <jni.h>
#include "jvmti.h"
#include <android/log.h>
#include <cstring>
#include <memory>
#include <string>

#include "slicer/dex_format.h"
#include "slicer/instrumentation.h"
#include "slicer/reader.h"
#include "slicer/writer.h"

#define LOG_TAG "ForjaAgent"
#define LOGI(...) __android_log_print(ANDROID_LOG_INFO,  LOG_TAG, __VA_ARGS__)
#define LOGW(...) __android_log_print(ANDROID_LOG_WARN,  LOG_TAG, __VA_ARGS__)
#define LOGE(...) __android_log_print(ANDROID_LOG_ERROR, LOG_TAG, __VA_ARGS__)

namespace {

// okhttp3.OkHttpClient — dotted (loadClass), internal (ClassFileLoadHook name)
// and descriptor (slicer) forms.
constexpr const char* kOkHttpClientDotted   = "okhttp3.OkHttpClient";
constexpr const char* kOkHttpClientInternal = "okhttp3/OkHttpClient";
constexpr const char* kOkHttpClientDesc     = "Lokhttp3/OkHttpClient;";

// OkHttp 4.x/5.x: `@get:JvmName("interceptors") val interceptors: List<...>`
// compiles to a JVM method `List interceptors()`. This is the application
// interceptor list OkHttp reads once per call (RealCall) to build the chain.
constexpr const char* kInterceptorsMethod = "interceptors";
constexpr const char* kInterceptorsSig    = "()Ljava/util/List;";

constexpr const char* kBootstrapClass = "com.tkhskt.forja.agent.Bootstrap";
constexpr const char* kBootstrapDesc  = "Lcom/tkhskt/forja/agent/Bootstrap;";
constexpr const char* kWrapMethod     = "wrapInterceptors";

// All const-after-init — only Agent_OnAttach writes to them.
JavaVM*   gVm    = nullptr;
jvmtiEnv* gJvmti = nullptr;

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

/// Loads an arbitrary class via appLoader. Takes a dotted name
/// (e.g. "okhttp3.OkHttpClient"). Returns nullptr (exception cleared) if the
/// class isn't in the app's classpath.
jclass LoadAppClass(JNIEnv* env, jobject appLoader, const char* dottedName) {
    jclass clClass = env->FindClass("java/lang/ClassLoader");
    if (clClass == nullptr) { env->ExceptionClear(); return nullptr; }
    jmethodID loadClass = env->GetMethodID(
        clClass, "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;");
    if (loadClass == nullptr) { env->ExceptionClear(); return nullptr; }
    jstring jName = env->NewStringUTF(dottedName);
    auto cls = static_cast<jclass>(env->CallObjectMethod(appLoader, loadClass, jName));
    if (env->ExceptionCheck()) env->ExceptionClear();
    env->DeleteLocalRef(jName);
    env->DeleteLocalRef(clClass);
    return cls;
}

/// Flip FileRulesProvider into consume-and-delete mode. Loaded via appLoader
/// (not the agent's own loader) so the singleton state isn't split across two
/// classloaders. Non-fatal on failure (we just keep rules.json on disk).
void EnableSelfDestruct(JNIEnv* env, jobject appLoader) {
    jclass bootClass = LoadAppClass(env, appLoader, kBootstrapClass);
    if (bootClass == nullptr) {
        LOGW("Bootstrap not resolvable via appLoader; self-destruct skipped");
        return;
    }
    jmethodID mid = env->GetStaticMethodID(
        bootClass, "enableSelfDestructMode", "(Ljava/lang/ClassLoader;)V");
    if (mid == nullptr) {
        env->ExceptionClear();
        LOGW("Bootstrap.enableSelfDestructMode not found (persistent mode)");
        return;
    }
    env->CallStaticVoidMethod(bootClass, mid, appLoader);
    if (env->ExceptionCheck()) {
        DescribePendingException(env, "Bootstrap.enableSelfDestructMode");
        LOGW("self-destruct enable failed (persistent mode)");
        return;
    }
    LOGI("self-destruct mode enabled on FileRulesProvider");
}

// --- Bytecode instrumentation (slicer) ---------------------------------

/// dex::Writer allocator backed by JVMTI's Allocate/Deallocate, so the dex
/// image we return from ClassFileLoadHook is owned by the VM (which frees it
/// with Deallocate per the JVMTI contract).
class JvmtiAllocator : public dex::Writer::Allocator {
 public:
    explicit JvmtiAllocator(jvmtiEnv* jvmti) : jvmti_(jvmti) {}

    void* Allocate(size_t size) override {
        unsigned char* mem = nullptr;
        if (jvmti_->Allocate(size, &mem) != JVMTI_ERROR_NONE) return nullptr;
        return mem;
    }
    void Free(void* ptr) override {
        if (ptr != nullptr) {
            jvmti_->Deallocate(reinterpret_cast<unsigned char*>(ptr));
        }
    }

 private:
    jvmtiEnv* jvmti_;
};

/// ClassFileLoadHook callback. Fires for every class load (and on
/// RetransformClasses). For okhttp3.OkHttpClient it rewrites interceptors()
/// with an exit hook routing the return value through
/// Bootstrap.wrapInterceptors. All other classes pass through untouched.
void JNICALL TransformClassFileLoad(
        jvmtiEnv* jvmti, JNIEnv* /*jni*/, jclass /*class_being_redefined*/,
        jobject /*loader*/, const char* name, jobject /*protection_domain*/,
        jint class_data_len, const unsigned char* class_data,
        jint* new_class_data_len, unsigned char** new_class_data) {
    if (name == nullptr || std::strcmp(name, kOkHttpClientInternal) != 0) {
        return;  // Not our class — fast path, leave output untouched.
    }

    dex::Reader reader(class_data, static_cast<size_t>(class_data_len));
    dex::u4 index = reader.FindClassIndex(kOkHttpClientDesc);
    if (index == dex::kNoIndex) {
        LOGW("okhttp3.OkHttpClient not found in its own dex; skipping");
        return;
    }
    reader.CreateClassIr(index);
    std::shared_ptr<ir::DexFile> dexIr = reader.GetIr();

    slicer::MethodInstrumenter mi(dexIr);
    // Tweak::None -> hook signature auto-generated to match the return type,
    // i.e. (Ljava/util/List;)Ljava/util/List;.
    mi.AddTransformation<slicer::ExitHook>(
        ir::MethodId(kBootstrapDesc, kWrapMethod), slicer::ExitHook::Tweak::None);

    if (!mi.InstrumentMethod(
            ir::MethodId(kOkHttpClientDesc, kInterceptorsMethod, kInterceptorsSig))) {
        LOGE("slicer InstrumentMethod(OkHttpClient.interceptors) failed");
        return;
    }

    dex::Writer writer(dexIr);
    JvmtiAllocator allocator(jvmti);
    size_t newSize = 0;
    dex::u1* image = writer.CreateImage(&allocator, &newSize);
    if (image == nullptr) {
        LOGE("slicer CreateImage returned null");
        return;
    }
    *new_class_data = image;
    *new_class_data_len = static_cast<jint>(newSize);
    LOGI("instrumented okhttp3.OkHttpClient.interceptors() (%d -> %zu bytes)",
         class_data_len, newSize);
}

// --- Init --------------------------------------------------------------

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

    // Only can_retransform_classes is required — no breakpoint/method-exit/
    // tag_objects, so the runtime is never forced into a deoptimized mode.
    jvmtiCapabilities caps = {};
    caps.can_retransform_classes = 1;
    if (auto err = jvmti->AddCapabilities(&caps); err != JVMTI_ERROR_NONE) {
        LOGE("AddCapabilities(can_retransform_classes) failed: %d", err);
        return JNI_ERR;
    }

    jvmtiEventCallbacks cbs = {};
    cbs.ClassFileLoadHook = &TransformClassFileLoad;
    if (auto err = jvmti->SetEventCallbacks(&cbs, sizeof(cbs)); err != JVMTI_ERROR_NONE) {
        LOGE("SetEventCallbacks failed: %d", err);
        return JNI_ERR;
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

    // 2) Pick the optimized-dex location (the app's codeCacheDir).
    const std::string optDir = GetCodeCacheDir(env);
    LOGI("opt dir = %s", optDir.c_str());

    // 3) Merge agent-bundle.dex into appLoader so Bootstrap / RulesInterceptor
    //    resolve through the host app's classloader. Must happen before the
    //    instrumented interceptors() runs (and before retransform, for clarity)
    //    so the injected invoke-static to Bootstrap.wrapInterceptors resolves.
    if (!MergeOurDexIntoApp(env, appLoader, dexPath.c_str(), optDir)) {
        LOGE("Bootstrap.inject failed (dex=%s)", dexPath.c_str());
        return JNI_ERR;
    }

    // 4) Switch FileRulesProvider into self-destruct mode (non-fatal).
    EnableSelfDestruct(env, appLoader);

    // 5) Enable the ClassFileLoadHook for the whole session (so OkHttpClient
    //    loaded later by a secondary classloader is also instrumented), then
    //    retransform the already-loaded OkHttpClient to apply the hook now.
    if (auto err = jvmti->SetEventNotificationMode(
            JVMTI_ENABLE, JVMTI_EVENT_CLASS_FILE_LOAD_HOOK, nullptr);
        err != JVMTI_ERROR_NONE) {
        LOGE("SetEventNotificationMode(ENABLE CLASS_FILE_LOAD_HOOK) failed: %d", err);
        return JNI_ERR;
    }

    jclass okClass = LoadAppClass(env, appLoader, kOkHttpClientDotted);
    if (okClass == nullptr) {
        LOGW("%s not loaded yet; the load hook will instrument it on first load",
             kOkHttpClientDotted);
        LOGI("forja JVMTI agent attached (dex=%s, retransform=DEFERRED)", dexPath.c_str());
        return JNI_OK;
    }

    jboolean modifiable = JNI_FALSE;
    if (jvmti->IsModifiableClass(okClass, &modifiable) == JVMTI_ERROR_NONE && modifiable) {
        if (auto err = jvmti->RetransformClasses(1, &okClass); err != JVMTI_ERROR_NONE) {
            LOGE("RetransformClasses(okhttp3.OkHttpClient) failed: %d", err);
        } else {
            LOGI("retransformed okhttp3.OkHttpClient");
        }
    } else {
        LOGW("okhttp3.OkHttpClient is not modifiable; interceptor injection skipped");
    }

    LOGI("forja JVMTI agent attached (dex=%s, retransform=DONE)", dexPath.c_str());
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
