// Minimal Android app that serves as forja's e2e test fixture.
//
// Two flavor dimensions:
//   okhttp: ok4 (OkHttp 4.12.0) / ok5 (OkHttp 5.x)
//   env:    dev (no suffix)     / staging (.staging)
//
// Combined applicationIds (built by the e2e suite):
//   ok4Dev      → com.tkhskt.forja.sample
//   ok4Staging  → com.tkhskt.forja.sample.staging
//   ok5Dev      → com.tkhskt.forja.sample.ok5
//   ok5Staging  → com.tkhskt.forja.sample.ok5.staging
//
// The ok4 + dev / ok4 + staging variants are the canonical multi-package
// fixtures the e2e tests use. The ok5 variants verify forja keeps
// working against OkHttp 5.x without source changes.
//
// At runtime the app eagerly builds an OkHttpClient singleton in
// Application.onCreate, and MainActivity's buttons fire a single HTTP
// request and display the response. Successful rewrites show through as
// the status and body changing to whatever the rule asks for.

plugins {
    // AGP 9.x supplies built-in Kotlin — no kotlin("android") needed.
    id("com.android.application") version "9.2.1"
}

android {
    namespace = "com.tkhskt.forja.sample"
    compileSdk = 34
    ndkVersion = "25.0.8355429"

    defaultConfig {
        applicationId = "com.tkhskt.forja.sample"
        minSdk = 28
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        debug {
            isMinifyEnabled = false
        }
    }

    flavorDimensions += listOf("okhttp", "env")
    productFlavors {
        // ---- okhttp dimension --------------------------------------------
        create("ok4") {
            dimension = "okhttp"
            // No suffix — the existing e2e tests rely on the base id.
        }
        create("ok5") {
            dimension = "okhttp"
            applicationIdSuffix = ".ok5"
            versionNameSuffix = "-ok5"
        }
        // ---- env dimension -----------------------------------------------
        create("dev") {
            dimension = "env"
            // No suffix.
        }
        create("staging") {
            dimension = "env"
            applicationIdSuffix = ".staging"
            versionNameSuffix = "-staging"
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    // AGP 9's built-in Kotlin auto-derives jvmTarget from
    // compileOptions.targetCompatibility, so no separate Kotlin block needed.
}

dependencies {
    // Flavor-specific OkHttp. Other deps are shared across all variants.
    "ok4Implementation"("com.squareup.okhttp3:okhttp:4.12.0")
    "ok5Implementation"("com.squareup.okhttp3:okhttp:5.3.0")

    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.0")
}
