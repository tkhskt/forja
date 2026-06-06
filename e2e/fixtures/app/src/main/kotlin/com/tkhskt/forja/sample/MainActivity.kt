package com.tkhskt.forja.sample

import android.app.Activity
import android.os.Bundle
import android.util.Log
import android.widget.Button
import android.widget.TextView
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request

// example.com is an IANA-reserved domain with extremely stable responses
// (httpbin.org occasionally turns slow, so we avoid it here). forja rewrites
// the response anyway, so the original being HTML vs JSON doesn't matter.
private const val URL = "https://example.com/"
private const val TAG = "SampleApp"

/**
 * Minimal UI for verifying forja behavior.
 *
 * Two buttons exercise two separate paths:
 *
 *  - **A: fetch_singleton**:
 *      Uses `Http.client` (the singleton built in Application.onCreate).
 *      → Verifies that agent.cpp's `IterateOverInstancesOfClass` path
 *        actually rewrote the already-built instance.
 *
 *  - **B: fetch_new**:
 *      Builds a fresh `OkHttpClient.Builder().build()` per click.
 *      → Verifies that agent.cpp's Breakpoint + per-thread MethodExit
 *        catches build() calls made after attach.
 *
 * Both buttons hit the same endpoint and expect the same outcome. With a
 * forja rule pushed, end-to-end success means both buttons return the
 * rewritten response (e.g. HTTP 418 + injected body).
 */
class MainActivity : Activity() {

    private val scope = CoroutineScope(Dispatchers.Main + SupervisorJob())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        val output = findViewById<TextView>(R.id.output)

        findViewById<Button>(R.id.fetch_singleton).setOnClickListener {
            fetch(output, "singleton") { Http.client }
        }
        findViewById<Button>(R.id.fetch_new).setOnClickListener {
            fetch(output, "new client") { OkHttpClient.Builder().build() }
        }
    }

    /**
     * Shared fetch routine. Pulling the client from a provider per call lets
     * scenarios A and B drive the same UI path without any branching.
     */
    private fun fetch(output: TextView, label: String, clientProvider: () -> OkHttpClient) {
        output.text = "[$label] fetching $URL ..."
        scope.launch {
            val text = runCatching {
                withContext(Dispatchers.IO) {
                    val client = clientProvider()
                    Log.i(TAG, "[$label] using client $client")
                    val req = Request.Builder().url(URL).build()
                    client.newCall(req).execute().use { resp ->
                        val body = resp.body?.string().orEmpty()
                        Log.i(TAG, "[$label] HTTP ${resp.code} (${body.length} bytes)")
                        buildString {
                            append("[").append(label).append("]\n")
                            append("HTTP ").append(resp.code).append('\n')
                            append("--- headers ---\n")
                            resp.headers.forEach { (k, v) ->
                                append(k).append(": ").append(v).append('\n')
                            }
                            append("--- body ---\n")
                            append(body)
                        }
                    }
                }
            }.getOrElse { "[$label] error: $it" }
            output.text = text
        }
    }

    override fun onDestroy() {
        scope.coroutineContext[Job]?.cancel()
        super.onDestroy()
    }
}
