package com.tkhskt.forja.agent

import com.tkhskt.forja.RulesInterceptor
import dalvik.system.DexClassLoader
import java.io.File

/**
 * Bootstrap called from the JVMTI agent (agent.cpp). Three responsibilities:
 *
 *  1. `inject(appLoader, dexPath, optDirPath)` merges `agent-bundle.dex`
 *     into appLoader's pathList.dexElements so RulesInterceptor /
 *     FileRulesProvider / Bootstrap become visible through the host app's
 *     classloader.
 *
 *  2. `enableSelfDestructMode(appLoader)` flips FileRulesProvider into the
 *     mode where rules.json is consumed-and-deleted on read.
 *
 *  3. `wrapInterceptors(original)` is the *exit-hook target*. The agent
 *     rewrites `okhttp3.OkHttpClient.interceptors()` (via slicer) so its
 *     return value is routed through this method, which prepends a
 *     `RulesInterceptor`. OkHttp calls `interceptors()` once per request to
 *     assemble the chain, so this covers every client — existing and future —
 *     without touching instance fields. The instrumented getter stays
 *     JIT-compiled, so there's no deopt and no per-client main-thread work.
 *
 * Non-SDK API touched via reflection:
 *   - `dalvik.system.BaseDexClassLoader.pathList`
 *   - `dalvik.system.DexPathList.dexElements`
 *
 * Debuggable apps have hidden-API restrictions disabled, so this works
 * without root.
 */
object Bootstrap {

    @JvmStatic
    fun inject(appLoader: ClassLoader, dexPath: String, optDirPath: String) {
        val optDir = File(optDirPath).apply { if (!exists()) mkdirs() }

        // Build a child DexClassLoader and merge its dexElements into
        // appLoader's pathList. The child loader itself isn't kept — it has
        // done its job once the dexElements are spliced in.
        val ourLoader = DexClassLoader(dexPath, optDir.absolutePath, null, appLoader)

        val baseLoaderClass = Class.forName("dalvik.system.BaseDexClassLoader")
        val pathListField = baseLoaderClass.getDeclaredField("pathList").apply { isAccessible = true }

        val appPathList = pathListField.get(appLoader)
        val ourPathList = pathListField.get(ourLoader)

        val pathListClass = Class.forName("dalvik.system.DexPathList")
        val dexElementsField =
            pathListClass.getDeclaredField("dexElements").apply { isAccessible = true }

        @Suppress("UNCHECKED_CAST")
        val appElements = dexElementsField.get(appPathList) as Array<Any?>
        @Suppress("UNCHECKED_CAST")
        val ourElements = dexElementsField.get(ourPathList) as Array<Any?>

        val componentType = requireNotNull(appElements.javaClass.componentType) {
            "DexPathList.dexElements is not an array type — incompatible Android version?"
        }
        @Suppress("UNCHECKED_CAST")
        val merged = java.lang.reflect.Array.newInstance(
            componentType,
            appElements.size + ourElements.size,
        ) as Array<Any?>
        System.arraycopy(appElements, 0, merged, 0, appElements.size)
        System.arraycopy(ourElements, 0, merged, appElements.size, ourElements.size)

        dexElementsField.set(appPathList, merged)
    }

    /**
     * Enable the consume-and-delete mode on FileRulesProvider.
     *
     * - FileRulesProvider lives in app/runtime — it must be loaded via
     *   `appLoader`, not the agent's own classloader. Touching the same
     *   class through two different classloaders would split the state.
     * - Errors are propagated up so the agent's ExceptionDescribe surfaces
     *   them in logcat.
     */
    @JvmStatic
    fun enableSelfDestructMode(appLoader: ClassLoader) {
        val providerClass = appLoader.loadClass("com.tkhskt.forja.FileRulesProvider")
        val instance = providerClass.getField("INSTANCE").get(null)
        val method = providerClass.getMethod("enableSelfDestructMode")
        method.invoke(instance)
    }

    /**
     * Exit-hook target for `okhttp3.OkHttpClient.interceptors()`.
     *
     * Called with the getter's original return value (the app's application
     * interceptor list) on every request. Returns a new list with a single
     * [RulesInterceptor] prepended so it runs first and sees the final logical
     * response. Idempotent: if a RulesInterceptor is already present (e.g. the
     * list is cached and re-read), the original is returned unchanged.
     *
     * The JVM signature is `(Ljava/util/List;)Ljava/util/List;`, which slicer's
     * ExitHook auto-matches to the instrumented method's return type. This
     * resolves through `appLoader` (where both Bootstrap and RulesInterceptor
     * live after [inject]). It must never throw into OkHttp, so any failure
     * falls back to the original list.
     */
    @JvmStatic
    fun wrapInterceptors(original: List<Any?>?): List<Any?> {
        val src = original ?: return emptyList()
        return try {
            if (src.any { it is RulesInterceptor }) {
                src
            } else {
                ArrayList<Any?>(src.size + 1).apply {
                    add(RulesInterceptor())
                    addAll(src)
                }
            }
        } catch (t: Throwable) {
            src
        }
    }
}
