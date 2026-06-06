// Multi-project Gradle build for the forja tool. Only the JVMTI
// runtime-attach pipeline is delivered.
//
// Modules:
//   :runtime        ... RulesInterceptor / FileRulesProvider / Rule
//                       (bundled into agent-bundle.dex)
//   :jvmti-agent    ... C++ JVMTI agent + Bootstrap.kt + the bundleAgentDex task

pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.PREFER_PROJECT)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "forja"

include(":runtime")
include(":jvmti-agent")
