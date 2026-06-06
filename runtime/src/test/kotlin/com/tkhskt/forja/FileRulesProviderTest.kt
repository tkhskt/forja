package com.tkhskt.forja

import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * Exercises the bits of FileRulesProvider that work outside an Android
 * runtime. The file()/appContext() reflection path requires
 * android.app.ActivityThread which we don't ship in the test classpath, so
 * we only verify:
 *   - the self-destruct flag flip
 *   - that rules() returns the cached list when no Application is reachable
 *
 * The full file-read / self-destruct path is covered end-to-end by the
 * e2e suite against a real device.
 */
class FileRulesProviderTest {

    @BeforeEach fun resetState() {
        setCache(emptyList())
        setSelfDestruct(false)
    }

    @AfterEach fun cleanup() {
        setCache(emptyList())
        setSelfDestruct(false)
    }

    @Test
    fun rules_returnsCache_whenNoApplicationContext() {
        val canned = listOf(
            Rule("a", true, "example.com", null, 200, null, emptyMap(), null)
        )
        setCache(canned)
        // appContext() returns null in JVM tests (android.app.ActivityThread
        // is not on the test classpath), so file() also returns null and
        // rules() short-circuits with the cache.
        val out = FileRulesProvider.rules()
        assertEquals(canned, out)
    }

    @Test
    fun enableSelfDestructMode_flipsFlag_idempotent() {
        // Flag starts false from @BeforeEach.
        FileRulesProvider.enableSelfDestructMode()
        assertTrue(getSelfDestruct(), "first call should enable self-destruct")
        // Second call should be a no-op (different log path, no exception).
        FileRulesProvider.enableSelfDestructMode()
        assertTrue(getSelfDestruct(), "second call should keep self-destruct on")
    }

    // ---- private-field manipulation helpers -----------------------------

    private fun setCache(rules: List<Rule>) {
        val field = FileRulesProvider::class.java.getDeclaredField("cache")
        field.isAccessible = true
        field.set(FileRulesProvider, rules)
    }

    private fun setSelfDestruct(value: Boolean) {
        val field = FileRulesProvider::class.java.getDeclaredField("selfDestruct")
        field.isAccessible = true
        field.setBoolean(FileRulesProvider, value)
    }

    private fun getSelfDestruct(): Boolean {
        val field = FileRulesProvider::class.java.getDeclaredField("selfDestruct")
        field.isAccessible = true
        return field.getBoolean(FileRulesProvider)
    }
}
