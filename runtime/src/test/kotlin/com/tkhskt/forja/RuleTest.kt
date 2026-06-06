package com.tkhskt.forja

import okhttp3.MediaType.Companion.toMediaTypeOrNull
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.jupiter.api.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

class RuleTest {

    // ---- parseList -----------------------------------------------------

    @Test
    fun parseList_minimalEntry() {
        val rules = Rule.parseList("""[{"name":"x","host":"example.com","status":418}]""")
        assertEquals(1, rules.size)
        val r = rules[0]
        assertEquals("x", r.name)
        assertTrue(r.enabled, "enabled defaults to true when absent")
        assertEquals("example.com", r.host)
        assertEquals(418, r.status)
        assertNull(r.body)
    }

    @Test
    fun parseList_bodyAsString_isPreserved() {
        val rules = Rule.parseList(
            """[{"name":"x","host":"a.com","status":200,"body":"hello"}]"""
        )
        assertEquals("hello", rules[0].body)
    }

    @Test
    fun parseList_bodyObject_serializedToJsonText() {
        val rules = Rule.parseList(
            """[{"name":"x","host":"a.com","status":200,"bodyObject":{"k":"v","n":1}}]"""
        )
        val body = rules[0].body
        assertNotNull(body)
        // bodyObject is stored as the JSON string representation so the
        // interceptor can hand it straight to ResponseBody.
        assertTrue(body!!.contains("\"k\""))
        assertTrue(body.contains("\"v\""))
        assertTrue(body.contains("\"n\""))
    }

    @Test
    fun parseList_explicitEnabledFalse_isHonored() {
        val rules = Rule.parseList(
            """[{"name":"x","enabled":false,"host":"a.com"}]"""
        )
        assertFalse(rules[0].enabled)
    }

    @Test
    fun parseList_emptyArray() {
        val rules = Rule.parseList("[]")
        assertTrue(rules.isEmpty())
    }

    @Test
    fun parseList_headersMap_parsed() {
        val rules = Rule.parseList(
            """[{"name":"x","host":"a.com","headers":{"X-A":"1","X-B":"two"}}]"""
        )
        assertEquals("1", rules[0].headers["X-A"])
        assertEquals("two", rules[0].headers["X-B"])
    }

    // ---- matches() -----------------------------------------------------

    @Test
    fun matches_disabledRule_neverMatches() {
        val r = rule(enabled = false, host = "example.com")
        assertFalse(r.matches(buildResponse(url = "https://example.com/")))
    }

    @Test
    fun matches_hostExactMatch() {
        val r = rule(host = "example.com")
        assertTrue(r.matches(buildResponse(url = "https://example.com/")))
        assertFalse(r.matches(buildResponse(url = "https://other.com/")))
    }

    @Test
    fun matches_pathSubstring() {
        val r = rule(path = "/foo")
        assertTrue(r.matches(buildResponse(url = "https://example.com/foo/bar")))
        assertFalse(r.matches(buildResponse(url = "https://example.com/bar")))
    }

    @Test
    fun matches_andsHostAndPath() {
        val r = rule(host = "e.com", path = "/x")
        assertTrue(r.matches(buildResponse(url = "https://e.com/x")))
        assertFalse(r.matches(buildResponse(url = "https://e.com/y")))
        assertFalse(r.matches(buildResponse(url = "https://other.com/x")))
    }

    // ---- applyTo() -----------------------------------------------------

    @Test
    fun applyTo_overridesStatusAndDefaultMessage() {
        val rewritten = rule(status = 418).applyTo(buildResponse(code = 200))
        assertEquals(418, rewritten.code)
        assertEquals("Rewritten 418", rewritten.message)
    }

    @Test
    fun applyTo_customMessageIsPreserved() {
        val rewritten = rule(status = 418, message = "I'm a teapot").applyTo(buildResponse(code = 200))
        assertEquals("I'm a teapot", rewritten.message)
    }

    @Test
    fun applyTo_setsHeaders() {
        val rewritten = rule(
            status = 418,
            headers = mapOf("X-Forja" to "yes")
        ).applyTo(buildResponse(code = 200))
        assertEquals("yes", rewritten.header("X-Forja"))
    }

    @Test
    fun applyTo_replacesBody_withDefaultJsonContentType() {
        val rewritten = rule(status = 418, body = "{\"ok\":true}").applyTo(buildResponse(code = 200))
        val bodyStr = rewritten.body?.string()
        assertEquals("{\"ok\":true}", bodyStr)
        assertEquals("application/json; charset=utf-8", rewritten.body?.contentType().toString())
    }

    @Test
    fun applyTo_honorsExplicitContentTypeHeader() {
        val rewritten = rule(
            status = 200,
            headers = mapOf("Content-Type" to "text/plain; charset=utf-8"),
            body = "hello"
        ).applyTo(buildResponse(code = 200))
        assertEquals("text/plain; charset=utf-8", rewritten.body?.contentType().toString())
    }

    // ---- Test helpers --------------------------------------------------

    private fun rule(
        name: String = "test-rule",
        enabled: Boolean = true,
        host: String? = null,
        path: String? = null,
        status: Int? = null,
        message: String? = null,
        headers: Map<String, String> = emptyMap(),
        body: String? = null,
    ) = Rule(
        name = name,
        enabled = enabled,
        host = host,
        path = path,
        status = status,
        message = message,
        headers = headers,
        body = body,
    )

    private fun buildResponse(
        url: String = "https://example.com/",
        method: String = "GET",
        code: Int = 200,
        body: String = "",
    ): Response {
        val req = Request.Builder().url(url).method(method, null).build()
        return Response.Builder()
            .request(req)
            .protocol(Protocol.HTTP_1_1)
            .code(code)
            .message("OK")
            .body(body.toResponseBody("text/plain".toMediaTypeOrNull()))
            .build()
    }
}
