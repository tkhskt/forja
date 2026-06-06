// JVMTI runtime-attach package (zero-touch / DEX injection).
//
// Outputs under build/outputs/agent/ :
//   - libforja-agent-<abi>.so   ... JVMTI native agent
//   - agent-bundle.dex          ... fat-DEX of Bootstrap + :runtime
//                                  (RulesInterceptor, etc.)
//
// The host app needs no debugImplementation dependency and no AAR import.
// As long as the APK is debuggable, `forja` pushes the .so and .dex onto
// the device and attaches at runtime.

import org.gradle.api.file.ArchiveOperations
import org.gradle.api.file.FileSystemOperations
import org.gradle.process.ExecOperations
import javax.inject.Inject

plugins {
    // AGP 9.x supplies built-in Kotlin — no kotlin("android") needed.
    id("com.android.library")
}

android {
    namespace = "com.tkhskt.forja.agent"
    compileSdk = 34
    ndkVersion = "25.2.9519653"

    defaultConfig {
        // `am attach-agent` is stable on API 28+.
        minSdk = 28

        externalNativeBuild {
            cmake {
                cppFlags += "-std=c++17"
            }
        }

        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86", "x86_64")
        }
    }

    externalNativeBuild {
        cmake {
            path = file("src/main/cpp/CMakeLists.txt")
            version = "3.22.1"
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
    // Only :runtime (RulesInterceptor / FileRulesProvider / Rule) is bundled
    // into the fat DEX.
    implementation(project(":runtime"))
}

// =====================================================================
// fat-DEX bundling
// =====================================================================
//
// d8 fuses every class into a single classes.dex which is renamed to
// agent-bundle.dex. kotlin-stdlib gets DEX'd alongside so the host app
// runs Java-only just as well. The AAR is an intermediate artifact and is
// NOT a distribution target.

/** Preprocessing task: extract classes.jar from the AAR. */
val collectAgentInputs by tasks.registering(Copy::class) {
    dependsOn("assembleDebug")

    val aar = layout.buildDirectory.file("outputs/aar/jvmti-agent-debug.aar")
    from(zipTree(aar)) { include("classes.jar") }
    into(layout.buildDirectory.dir("intermediates/agent-inputs"))
    rename { "jvmti-agent-classes.jar" }
}

/**
 * Custom task that extracts the .so files and runs d8.
 *
 * In Gradle's Kotlin DSL, the Project extensions (`exec` / `copy` / `zipTree`)
 * aren't directly visible inside `doLast { ... }` (the receiver is the Task).
 * Using the injected service APIs (ExecOperations / FileSystemOperations /
 * ArchiveOperations) instead lets this compile under Android Studio's strict
 * Kotlin DSL and also work with config cache enabled.
 */
abstract class BundleAgentDexTask @Inject constructor(
    private val execOps: ExecOperations,
    private val fsOps: FileSystemOperations,
    private val archiveOps: ArchiveOperations,
) : DefaultTask() {

    @get:InputFile abstract val aarFile: RegularFileProperty
    @get:InputFile abstract val agentClassesJar: RegularFileProperty
    @get:InputFile abstract val runtimeJarFile: RegularFileProperty
    @get:InputFiles abstract val runtimeClasspath: ConfigurableFileCollection

    @get:Input abstract val sdkDirPath: Property<String>
    @get:Input abstract val compileSdkLevel: Property<Int>

    @get:OutputDirectory abstract val outputDir: DirectoryProperty
    @get:Internal abstract val intermediateExtractDir: DirectoryProperty

    @TaskAction
    fun run() {
        val out = outputDir.get().asFile
        out.mkdirs()
        out.listFiles()?.forEach { it.deleteRecursively() }

        // ---- 1. Extract per-ABI .so files from the AAR and rename them. ----
        val aar = aarFile.get().asFile
        val tmpExtract = intermediateExtractDir.get().asFile
        tmpExtract.deleteRecursively()
        tmpExtract.mkdirs()
        fsOps.copy {
            from(archiveOps.zipTree(aar)) {
                include("jni/**/libforja-agent.so")
            }
            into(tmpExtract)
        }
        tmpExtract.walkTopDown()
            .filter { it.isFile && it.name == "libforja-agent.so" }
            .forEach { so ->
                val abi = so.parentFile.name
                so.copyTo(out.resolve("libforja-agent-${abi}.so"), overwrite = true)
            }

        // ---- 2. Gather the dependency JARs. ---------------------------------
        val dexInputs = mutableListOf<File>()
        val provided = mutableListOf<File>()

        dexInputs += agentClassesJar.get().asFile      // Bootstrap
        dexInputs += runtimeJarFile.get().asFile       // RulesInterceptor / FileRulesProvider / Rule

        for (f in runtimeClasspath) {
            if (!f.exists() || f.extension != "jar") continue
            val name = f.name.lowercase()
            when {
                name.startsWith("android.jar") || name == "android.jar" -> provided += f
                name.contains("androidx") || name.contains("annotation") -> provided += f
                name.contains("gradle-api") -> { /* skip */ }
                name.startsWith("okhttp") -> provided += f
                name.startsWith("okio") -> provided += f
                else -> dexInputs += f
            }
        }
        val dexInputsUniq = dexInputs.distinctBy { it.absolutePath }
        val providedUniq = provided.distinctBy { it.absolutePath }

        val sdkRoot = File(sdkDirPath.get())
        val androidJar = sdkRoot.resolve("platforms/android-${compileSdkLevel.get()}/android.jar")
        require(androidJar.exists()) { "android.jar not found at $androidJar" }

        // ---- 3. Resolve the d8 path. ------------------------------------------
        val buildToolsDir = sdkRoot.resolve("build-tools")
        val d8 = buildToolsDir.listFiles()
            ?.sortedByDescending { it.name }
            ?.asSequence()
            ?.map { it.resolve("d8") }
            ?.firstOrNull { it.exists() && it.canExecute() }
            ?: error("could not find executable d8 under $buildToolsDir")

        // ---- 4. Run d8. ------------------------------------------------
        val cmd = mutableListOf(
            d8.absolutePath, "--min-api", "28",
            "--output", out.absolutePath,
            "--lib", androidJar.absolutePath,
        )
        for (p in providedUniq) cmd += listOf("--lib", p.absolutePath)
        for (i in dexInputsUniq) cmd += i.absolutePath

        logger.lifecycle("[bundleAgent] d8 inputs (DEX): ${dexInputsUniq.size} jar(s)")
        logger.lifecycle("[bundleAgent] d8 provided   : ${providedUniq.size + 1} jar(s)")
        execOps.exec { commandLine(cmd) }

        // ---- 5. Rename classes.dex to agent-bundle.dex. ----------------
        val classesDex = out.resolve("classes.dex")
        val bundleDex = out.resolve("agent-bundle.dex")
        require(classesDex.exists()) { "d8 did not produce classes.dex in $out" }
        bundleDex.delete()
        classesDex.renameTo(bundleDex)

        logger.lifecycle("[bundleAgent] wrote ${out.listFiles()?.joinToString(", ") { it.name }}")
    }
}

val bundleAgentDex by tasks.registering(BundleAgentDexTask::class) {
    group = "build"
    description = "Produce libforja-agent-<abi>.so + agent-bundle.dex into build/outputs/agent/"

    dependsOn(collectAgentInputs)
    dependsOn("assembleDebug")
    dependsOn(":runtime:jar")

    aarFile.set(layout.buildDirectory.file("outputs/aar/jvmti-agent-debug.aar"))
    agentClassesJar.set(
        layout.buildDirectory.file("intermediates/agent-inputs/jvmti-agent-classes.jar")
    )
    runtimeJarFile.set(
        project(":runtime").tasks.named("jar", Jar::class).flatMap { it.archiveFile }
    )
    runtimeClasspath.from(configurations.named("debugRuntimeClasspath"))

    sdkDirPath.set(
        androidComponents.sdkComponents.sdkDirectory.map { it.asFile.absolutePath }
    )
    compileSdkLevel.set(android.compileSdk!!)
    outputDir.set(layout.buildDirectory.dir("outputs/agent"))
    intermediateExtractDir.set(layout.buildDirectory.dir("intermediates/agent-jni"))
}
