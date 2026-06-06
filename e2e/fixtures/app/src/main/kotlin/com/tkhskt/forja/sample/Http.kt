package com.tkhskt.forja.sample

import okhttp3.OkHttpClient
import java.util.concurrent.TimeUnit

/**
 * Singleton OkHttpClient. Touched from Application.onCreate so it's
 * eagerly built().
 *
 * Built early on purpose so we can verify that forja's rewrite still
 * works on clients that were build() before attach — i.e. the agent's
 * existing-instance rewrite path's happy case.
 *
 * Timeouts are set to 20s so public-service latency jitter doesn't fail
 * the e2e suite. The actual RTT is irrelevant to a rewrite test, so big
 * margins are fine.
 */
object Http {
    val client: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(20, TimeUnit.SECONDS)
        .readTimeout(20, TimeUnit.SECONDS)
        .writeTimeout(20, TimeUnit.SECONDS)
        .build()
}
