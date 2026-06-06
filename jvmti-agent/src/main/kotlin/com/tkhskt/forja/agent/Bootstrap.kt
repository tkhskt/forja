package com.tkhskt.forja.agent

import dalvik.system.DexClassLoader
import java.io.File

/**
 * Bootstrap called from the JVMTI agent (agent.cpp). Three responsibilities:
 *
 *  1. `inject(appLoader, dexPath, optDirPath)` merges `agent-bundle.dex`
 *     into appLoader's pathList.dexElements so RulesInterceptor /
 *     FileRulesProvider / Rule become visible through the host app's
 *     classloader.
 *
 *  2. `enableSelfDestructMode(appLoader)` flips FileRulesProvider into the
 *     mode where rules.json is consumed-and-deleted on read.
 *
 *  3. `modifyExistingClients(clients, appLoader)` walks the OkHttpClient
 *     instances the agent harvested via JVMTI's IterateOverInstancesOfClass
 *     and reflectively replaces each one's `interceptors` field with
 *     `[RulesInterceptor] + original list`.
 *
 * Non-SDK API touched via reflection:
 *   - `dalvik.system.BaseDexClassLoader.pathList`
 *   - `dalvik.system.DexPathList.dexElements`
 *   - `okhttp3.OkHttpClient.interceptors` (private final, Kotlin val)
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
     * @param clients   Array of OkHttpClient instances the agent harvested via
     *                  JVMTI IterateOverInstancesOfClass + GetObjectsWithTags
     *                  (passed in as `jobject[]`, so the static type is Array<Any>).
     * @param appLoader Classloader used to resolve RulesInterceptor / OkHttpClient.
     * @return The number of instances whose interceptors were actually swapped.
     */
    @JvmStatic
    fun modifyExistingClients(clients: Array<Any>, appLoader: ClassLoader): Int {
        if (clients.isEmpty()) return 0

        val ruleClass = appLoader.loadClass("com.tkhskt.forja.RulesInterceptor")
        val ruleCtor = ruleClass.getDeclaredConstructor()

        val clientClass = appLoader.loadClass("okhttp3.OkHttpClient")
        // OkHttp 4.x: `@get:JvmName("interceptors") val interceptors: List<Interceptor>`
        // At the JVM level this is `private final List interceptors`. On ART,
        // Field.set works on final fields after setAccessible(true) (different
        // from JDK 17+'s behavior).
        val interceptorsField = clientClass
            .getDeclaredField("interceptors")
            .apply { isAccessible = true }

        var modified = 0
        for (client in clients) {
            @Suppress("UNCHECKED_CAST")
            val orig = interceptorsField.get(client) as List<Any>
            // Skip instances that already have a RulesInterceptor — avoid
            // double-attach.
            if (orig.any { ruleClass.isInstance(it) }) continue
            val wrapped = ArrayList<Any>(orig.size + 1).apply {
                add(ruleCtor.newInstance())
                addAll(orig)
            }
            interceptorsField.set(client, wrapped)
            modified++
        }
        return modified
    }
}
