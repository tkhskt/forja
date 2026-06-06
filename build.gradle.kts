// Centralizes the plugin versions shared across modules.
// :runtime uses `kotlin("jvm")` (still a separate plugin), while the Android
// modules (:jvmti-agent and e2e/fixtures/app) rely on AGP 9.x's built-in
// Kotlin support — no explicit `kotlin("android")` plugin needed there.

plugins {
    kotlin("jvm") version "2.2.0" apply false
    id("com.android.library") version "9.2.1" apply false
}

allprojects {
    group = "com.tkhskt.forja"
    version = "0.1.0"
}
