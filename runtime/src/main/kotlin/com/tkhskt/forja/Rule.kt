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
    val path: String?,        // substring of encodedPath
    // --- replacement payload ---
    val status: Int?,
    val message: String?,
    val headers: Map<String, String>,
    val body: String?
) {
    fun matches(response: Response): Boolean {
        if (!enabled) return false
        val url = response.request.url
        if (host != null && url.host != host) return false
        if (path != null && !url.encodedPath.contains(path)) return false
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
