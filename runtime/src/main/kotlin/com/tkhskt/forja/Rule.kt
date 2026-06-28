package com.tkhskt.forja

import okhttp3.MediaType.Companion.toMediaTypeOrNull
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.json.JSONArray

/**
 * A single rewrite rule: matchers + the replacement payload.
 * The schema mirrors the CLI's rules.json on disk.
 */
data class Rule(
    val name: String,
    val enabled: Boolean,
    // --- match conditions ---
    val host: String?,        // exact host match
    val path: String?,        // encodedPath matcher: plain substring, or a glob when it contains `*`
    // --- replacement payload ---
    val status: Int?,
    val message: String?,
    val headers: Map<String, String>,
    val body: String?
) {
    // Precompiled matcher for `path`. When the pattern contains a `*`
    // wildcard it is compiled to a regex (each `*` matches one path segment,
    // i.e. any run of characters except '/') and matched unanchored, mirroring
    // the looseness of the plain-substring case. When there is no `*` this is
    // null and matches() falls back to a plain substring check.
    private val pathRegex: Regex? = path?.let(::compileGlobPath)

    fun matches(response: Response): Boolean {
        if (!enabled) return false
        val url = response.request.url
        if (host != null && url.host != host) return false
        if (path != null) {
            val regex = pathRegex
            if (regex != null) {
                if (!regex.containsMatchIn(url.encodedPath)) return false
            } else if (!url.encodedPath.contains(path)) {
                return false
            }
        }
        return true
    }

    fun applyTo(response: Response): Response {
        val builder = response.newBuilder()
        if (status != null) {
            builder.code(status)
            builder.message(message ?: "Rewritten $status")
        }
        for ((k, v) in headers) builder.header(k, v)
        if (body != null) {
            val ct = headers["Content-Type"]
                ?: headers["content-type"]
                ?: "application/json; charset=utf-8"
            builder.body(body.toResponseBody(ct.toMediaTypeOrNull()))
        }
        return builder.build()
    }

    companion object {
        /**
         * Compiles a `path` pattern to a [Regex], or returns null when the
         * pattern has no `*` wildcard (in which case callers use a plain
         * substring match). Each `*` becomes `[^/]*` — one path segment —
         * and every other character is matched literally, so regex
         * metacharacters in the path (`.`, `+`, `(`, …) are never special.
         */
        private fun compileGlobPath(pattern: String): Regex? {
            if (!pattern.contains('*')) return null
            val regex = pattern.split("*").joinToString("[^/]*") { Regex.escape(it) }
            return Regex(regex)
        }

        fun parseList(text: String): List<Rule> {
            val arr = JSONArray(text)
            return (0 until arr.length()).map { i ->
                val o = arr.getJSONObject(i)
                val headers = mutableMapOf<String, String>()
                o.optJSONObject("headers")?.let { h ->
                    val keys = h.keys()
                    while (keys.hasNext()) {
                        // JSONObject.keys() returns a raw Iterator on some
                        // Android versions (and on the older Java stubs), so
                        // cast to String explicitly.
                        val k = keys.next() as String
                        headers[k] = h.getString(k)
                    }
                }
                val body = when {
                    o.has("bodyObject") -> o.get("bodyObject").toString() // object → JSON text
                    o.has("body") -> o.optString("body")
                    else -> null
                }
                Rule(
                    name = o.optString("name", "(unnamed)"),
                    enabled = o.optBoolean("enabled", true),
                    host = o.optStringOrNull("host"),
                    path = o.optStringOrNull("path"),
                    status = if (o.has("status")) o.getInt("status") else null,
                    message = o.optStringOrNull("message"),
                    headers = headers,
                    body = body
                )
            }
        }

        private fun org.json.JSONObject.optStringOrNull(key: String): String? =
            if (has(key) && !isNull(key)) getString(key) else null
    }
}
