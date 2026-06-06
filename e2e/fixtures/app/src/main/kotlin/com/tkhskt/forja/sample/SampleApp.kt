package com.tkhskt.forja.sample

import android.app.Application
import android.util.Log

class SampleApp : Application() {
    override fun onCreate() {
        super.onCreate()
        // Eagerly build the OkHttpClient singleton at startup.
        // The JVMTI-attach path is verified against this already-built client
        // (= the "existing instance rewrite" code path).
        Log.i(TAG, "OkHttp singleton ready: ${Http.client}")
    }

    private companion object {
        const val TAG = "SampleApp"
    }
}
