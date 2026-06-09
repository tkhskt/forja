//go:build e2e

// OkHttp 5 compatibility checks. The fixture app's `ok5` flavor uses
// com.squareup.okhttp3:okhttp:5.x against the same RulesInterceptor /
// JVMTI agent paths. These tests verify forja's rewrite still fires on an
// OkHttp 5 app via the slicer exit-hook on OkHttpClient.interceptors()
// (applied with RetransformClasses). Because interceptors() is read per
// request, this covers both the singleton and new-client buttons.
package e2e_test

import (
	"testing"
	"time"
)

// TestOkHttp5BasicRewrite — install the ok5 + dev variant, push a rule via
// the sugar path, and verify the device sees HTTP 418. Mirrors
// TestCoreBasicRewrite but against the OkHttp 5 fixture.
func TestOkHttp5BasicRewrite(t *testing.T) {
	resetForjaState(t, AppOk5Dev)
	forceStop(t, AppOk5Dev)
	startMainActivity(t, AppOk5Dev)
	clearLogcat(t)

	runForja(t, "rules", "add", "ok5-mock",
		"--host", "example.com", "--path", "/",
		"--status", "418",
		"--body", `{"by":"forja-ok5"}`,
	)
	runForja(t, "apply", "--app", AppOk5Dev, "--enable", "ok5-mock")

	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	waitForLogcat(t, "self-destruct mode enabled", 5*time.Second, "ForjaAgent")

	// Both buttons (singleton + new-client) should return 418 + the injected
	// body. The singleton path exercises IterateOverInstancesOfClass; the
	// new-client path exercises the Builder.build() breakpoint.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.ok5
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*forja-ok5.*"
`)

	startMainActivity(t, AppOk5Dev)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.ok5
---
- tapOn:
    id: "fetch_new"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*forja-ok5.*"
`)

	waitForLogcat(t, "hit 'ok5-mock'", 10*time.Second, "Forja")
}

// TestOkHttp5OffRestoresOriginalResponse — `forja off --app X` empties the
// per-pkg enabled list and pushes [] so the device falls back to the real
// HTTP response. Verifies the off path works against OkHttp 5 too.
func TestOkHttp5OffRestoresOriginalResponse(t *testing.T) {
	resetForjaState(t, AppOk5Dev)
	forceStop(t, AppOk5Dev)
	startMainActivity(t, AppOk5Dev)

	runForja(t, "rules", "add", "ok5-off",
		"--host", "example.com", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppOk5Dev, "--enable", "ok5-off")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.ok5
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
`)

	runForja(t, "off", "--app", AppOk5Dev)
	startMainActivity(t, AppOk5Dev)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.ok5
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 200.*"
    timeout: 30000
`)
}
