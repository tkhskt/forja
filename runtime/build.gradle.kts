// On-device runtime library bundled into agent-bundle.dex.
//
// Built as a Kotlin JVM JAR. Every external symbol it touches is expected
// to already be on the host Android app's classpath, so they're all
// compileOnly:
//   - android.* (the app's compileSdk)
//   - okhttp3.* (the host app uses OkHttp / Retrofit — that's the premise
//     of this tool)
//   - org.json.* (Android standard)
//
// Packaged as a JAR (not an AAR) so the build doesn't require AGP, which
// keeps the agent build lighter. Android apps consume JAR deps just fine.

plugins {
    kotlin("jvm")
}

dependencies {
    // Stubs for android.content.Context / android.util.Log resolution.
    compileOnly("com.google.android:android:4.1.1.4")
    // OkHttp 4.x API (toMediaTypeOrNull / toResponseBody).
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")

    // ---- Test dependencies ------------------------------------------
    // The android.jar stub throws RuntimeException("Stub!") for every
    // method body, which makes it useless at test runtime. We exclude it
    // entirely from the test classpaths below and provide tiny test-only
    // shims for the android.* symbols we touch (Log, Context, File).
    testImplementation(kotlin("test"))
    testImplementation("org.junit.jupiter:junit-jupiter:5.10.2")
    testImplementation("com.squareup.okhttp3:okhttp:4.12.0")
    testImplementation("com.squareup.okhttp3:mockwebserver:4.12.0")
    // Real org.json, not the throws-Stub stub from android.jar.
    testImplementation("org.json:json:20231013")
}

// Strip the android.jar stub from test classpaths so our hand-written shims
// in src/test/kotlin/android/util/Log.kt take effect at compile and runtime.
configurations.named("testCompileClasspath") {
    exclude(group = "com.google.android", module = "android")
}
configurations.named("testRuntimeClasspath") {
    exclude(group = "com.google.android", module = "android")
}

kotlin {
    jvmToolchain(17)
}

tasks.named<Test>("test") {
    useJUnitPlatform()
}
