package com.tkhskt.forja

import android.content.Context
import android.util.Log
import java.io.File

/**
 * Reads filesDir/rules.json and re-parses it only when the mtime changes.
 * The Context is obtained via reflection, so no ContentProvider or manifest
 * change is required on the host app.
 *
 * Bootstrap calls [enableSelfDestructMode] at attach time. While
 * self-destruct is enabled, `rules()` deletes the file immediately after
 * reading it, so no rules persist on disk once the process dies — the
 * behavior matches Android Studio's NetworkInspector "session scope".
 */
object FileRulesProvider {

    private const val TAG = "Forja"
    private const val FILE_NAME = "rules.json"

    @Volatile private var cache: List<Rule> = emptyList()
    @Volatile private var lastModified = -1L

    // While self-destruct is true: (a) delete the file right after reading it
    // and (b) do NOT clear the cache when the file is absent. Bootstrap flips
    // this on at attach.
    @Volatile private var selfDestruct = false

    /** Enable self-destruct mode — called by Bootstrap at attach. */
    @JvmStatic
    fun enableSelfDestructMode() {
        if (!selfDestruct) {
            selfDestruct = true
            Log.d(TAG, "self-destruct mode enabled (file will be consumed-and-deleted)")
        }
    }

    fun rules(): List<Rule> {
        val f = file() ?: return cache
        if (!f.exists()) {
            // Without self-destruct, an absent file means "the user explicitly
            // turned forja off" — clear the cache. With self-destruct, the
            // absent file is just the post-read state, so we keep the cache.
            if (!selfDestruct && lastModified != -1L) {
                cache = emptyList()
                lastModified = -1L
            }
            return cache
        }
        val m = f.lastModified()
        if (m != lastModified) {
            lastModified = m
            cache = try {
                Rule.parseList(f.readText())
            } catch (t: Throwable) {
                Log.w(TAG, "failed to parse $FILE_NAME: ${t.message}")
                emptyList()
            }
            Log.d(TAG, "loaded ${cache.size} rule(s)")
            if (selfDestruct) {
                // Invalidate lastModified so the next CLI write is always
                // reloaded — even if the new file happens to land with the
                // exact same mtime.
                f.delete()
                lastModified = -1L
            }
        }
        return cache
    }

    private fun file(): File? {
        val ctx = appContext() ?: return null
        return File(ctx.filesDir, FILE_NAME)
    }

    /** Retrieve the Application via android.app.ActivityThread.currentApplication(). */
    private fun appContext(): Context? = try {
        val activityThread = Class.forName("android.app.ActivityThread")
        val app = activityThread.getMethod("currentApplication").invoke(null)
        app as? Context
    } catch (t: Throwable) {
        null
    }
}
