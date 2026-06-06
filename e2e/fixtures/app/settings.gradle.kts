// Minimal Android app used as a fixture by forja's e2e suite. The agent is
// merged in via JVMTI attach, so this build has no compile-time dependency
// on the parent repo.

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

rootProject.name = "sample-app"
