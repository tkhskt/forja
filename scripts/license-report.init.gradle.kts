// Gradle init script — applies jk1 Gradle License Report plugin to every
// subproject of the current build, so we can audit the resolved dependency
// licenses without permanently adding the plugin to the project's build.gradle.kts.
//
// Usage:
//   ./gradlew --init-script scripts/license-report.init.gradle.kts \
//       :runtime:generateLicenseReport :jvmti-agent:generateLicenseReport
//
// Reports land under <subproject>/build/reports/dependency-license/.
// Inspect index.html or licenses.json there.
import com.github.jk1.license.LicenseReportExtension
import com.github.jk1.license.render.InventoryHtmlReportRenderer
import com.github.jk1.license.render.JsonReportRenderer
import com.github.jk1.license.render.ReportRenderer

initscript {
    repositories {
        gradlePluginPortal()
    }
    dependencies {
        classpath("com.github.jk1:gradle-license-report:3.1.2")
    }
}

allprojects {
    apply<com.github.jk1.license.LicenseReportPlugin>()

    extensions.configure<LicenseReportExtension> {
        // jk1's renderers implement ReportRenderer but also expose
        // GroovyObject (the plugin is written in Groovy), so Kotlin's reified
        // array inference picks the intersection. From Kotlin 2.3 onward
        // this is a hard error — pin the element type explicitly.
        renderers = arrayOf<ReportRenderer>(
            InventoryHtmlReportRenderer("index.html", "Forja dependency licenses"),
            JsonReportRenderer("licenses.json", false),
        )
        // The distributed artifact only ships what's on debugRuntimeClasspath.
        // Restricting the report to that configuration (where applicable)
        // avoids inflating it with test / annotation processor noise.
        configurations = arrayOf("runtimeClasspath", "debugRuntimeClasspath")
        allowedLicensesFile = File(rootDir, "scripts/allowed-licenses.json")
    }
}
