package com.tkhskt.forja

import android.util.Log
import okhttp3.Interceptor
import okhttp3.Response

/**
 * The OkHttp interceptor forja injects into every existing OkHttpClient.
 *
 * The no-arg constructor is required because the JVMTI agent invokes
 * `new RulesInterceptor()` directly via reflection.
 *
 * Installed as an application-layer interceptor, so it sees the final logical
 * response exactly once. Only rules that match are rewritten — the rest of the
 * response (including the body) is passed through untouched so streaming
 * responses and other non-matching traffic aren't disturbed.
 */
class RulesInterceptor : Interceptor {

    override fun intercept(chain: Interceptor.Chain): Response {
        val response = chain.proceed(chain.request())

        val rules = FileRulesProvider.rules()
        if (rules.isEmpty()) return response

        val rule = rules.firstOrNull { it.matches(response) } ?: return response

        return try {
            val rewritten = rule.applyTo(response)
            Log.d("Forja", "hit '${rule.name}' ${response.request.url}")
            rewritten
        } catch (t: Throwable) {
            Log.w("Forja", "rewrite failed for '${rule.name}': ${t.message}")
            response
        }
    }
}
