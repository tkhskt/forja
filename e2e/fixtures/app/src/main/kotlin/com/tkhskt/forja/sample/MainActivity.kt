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

// The e2e harness runs an in-process mock server and bridges it to the device
// with `adb reverse tcp:8080`, so this fixed loopback base hits a deterministic
// baseline (HTTP 200) with no external network dependency — fast and
// reproducible. forja rewrites the response anyway, so the baseline shape
// doesn't matter. (Cleartext to 127.0.0.1 is allowed via usesCleartextTraffic.)
private const val BASE_URL = "http://127.0.0.1:8080"
// The request path is overridable via an `am start ... --es path <path>` extra
// so path-matching tests (e.g. wildcards) can drive an arbitrary endpoint like
// /users/42/posts. Defaults to "/" so every existing flow is unchanged.
private const val EXTRA_PATH = "path"
private const val TAG = "SampleApp"

/**
 * Minimal UI for verifying forja behavior.
 *
 * Two buttons exercise two separate paths:
 *
 *  - **A: fetch_singleton**: uses `Http.client`, the singleton built in
 *      Application.onCreate (i.e. before the agent attached).
 *  - **B: fetch_new**: builds a fresh `OkHttpClient.Builder().build()` per
 *      click (i.e. after attach).
 *
 * Both exercise the same hook: agent.cpp instruments
 * `OkHttpClient.interceptors()` via RetransformClasses, and OkHttp reads it
 * per request, so existing and new clients alike get the RulesInterceptor.
 *
 * Both buttons hit the same endpoint and expect the same outcome. With a
 * forja rule pushed, end-to-end success means both buttons return the
 * rewritten response (e.g. HTTP 418 + injected body).
 */
class MainActivity : Activity() {

    private val scope = CoroutineScope(Dispatchers.Main + SupervisorJob())

    // Resolved once in onCreate from the launch intent's `path` extra (default
    // "/"). A force-stop + relaunch with a different extra picks up a new path.
    private lateinit var requestUrl: String

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        val path = intent?.getStringExtra(EXTRA_PATH) ?: "/"
        requestUrl = BASE_URL + path
        Log.i(TAG, "request URL: $requestUrl")

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
        output.text = "[$label] fetching $requestUrl ..."
        scope.launch {
            val text = runCatching {
                withContext(Dispatchers.IO) {
                    val client = clientProvider()
                    Log.i(TAG, "[$label] using client $client")
                    val req = Request.Builder().url(requestUrl).build()
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
