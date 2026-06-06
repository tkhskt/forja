//go:build e2e

// Core e2e scenarios — the contract for end-to-end behavior:
//
//   - basic rewrite (rules add → device sees status/body)
//   - self-destruct (rules.json is read-and-deleted by the agent)
//   - process kill → rules vanish (no persistence across app restart)
//   - forja off (device empty + status.json[pkg].enabled emptied)
//   - bodyFile (external response payload injected on push)
package e2e_test

import (
	"strings"
	"testing"
	"time"
)

// TestCoreBasicRewrite — basic rewrite via singleton OkHttpClient.
func TestCoreBasicRewrite(t *testing.T) {
	resetForjaState(t, PkgDev)
	forceStop(t, PkgDev)
	startMainActivity(t, PkgDev)
	clearLogcat(t)

	runForja(t, "rules", "add", "mock-teapot",
		"--pkg", PkgDev,
		"--host", "example.com", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)

	// The auto-push during `rules add` should attach + push. Wait for the
	// JVMTI attach log line as a stable signal that the agent is live.
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	waitForLogcat(t, "self-destruct mode enabled", 5*time.Second, "ForjaAgent")

	// We rewrote body too, so assert both status AND body content here.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 15000
- assertVisible:
    text: ".*rewritten.*"
`)
	waitForLogcat(t, "hit 'mock-teapot'", 10*time.Second, "Forja")
}

// TestCoreSelfDestruct — after push, rules.json should be absent on the
// device because the agent reads it and deletes it.
func TestCoreSelfDestruct(t *testing.T) {
	resetForjaState(t, PkgDev)
	forceStop(t, PkgDev)
	startMainActivity(t, PkgDev)

	runForja(t, "rules", "add", "x",
		"--pkg", PkgDev,
		"--host", "example.com", "--status", "418",
	)
	waitForLogcat(t, "self-destruct mode enabled", 30*time.Second, "ForjaAgent")

	// The agent reads + deletes on the first interceptor call. To make that
	// happen we need an HTTP request to fly. Trigger one via the button.
	maestroFlow(t, "tap_singleton.yaml")
	// Give the agent a moment to ingest + delete.
	time.Sleep(1 * time.Second)

	out, exists := readDeviceFile(t, PkgDev, "files/rules.json")
	if exists {
		t.Errorf("rules.json should have been self-destructed; still present:\n%s", out)
	}

	// Sibling agent files SHOULD still be there.
	ls := deviceListFiles(t, PkgDev, "files/")
	for _, want := range []string{"libforja-agent.so", "agent-bundle.dex"} {
		if !strings.Contains(ls, want) {
			t.Errorf("expected %s to still be present in files/, got:\n%s", want, ls)
		}
	}
}

// TestCoreProcessKillClearsRules — kill the app and verify the next launch
// no longer sees the rewrites. Nothing persists across an app restart.
func TestCoreProcessKillClearsRules(t *testing.T) {
	resetForjaState(t, PkgDev)
	forceStop(t, PkgDev)
	startMainActivity(t, PkgDev)

	runForja(t, "rules", "add", "kill-test",
		"--pkg", PkgDev,
		"--host", "example.com", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	// Confirm baseline behavior first: the rule IS in effect.
	clearLogcat(t)
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	// Now kill + relaunch.
	forceStop(t, PkgDev)
	startMainActivity(t, PkgDev)
	clearLogcat(t)

	// Verify the response is back to baseline (200). The HTTP call to
	// example.com returns 200 OK with a simple HTML page.
	maestroFlow(t, "tap_singleton_assert_200.yaml")

	// And rules.json is not on the device (agent is dead, never re-attached).
	if _, exists := readDeviceFile(t, PkgDev, "files/rules.json"); exists {
		t.Error("rules.json should not exist after process kill; agent should be dead")
	}
}

// TestCoreOff — forja off pushes [] AND empties the package's enabled list in
// status.json.
func TestCoreOff(t *testing.T) {
	resetForjaState(t, PkgDev)
	forceStop(t, PkgDev)
	startMainActivity(t, PkgDev)

	// Both rules added with --pkg so they're enabled on PkgDev via the sugar
	// path (yml + status.enable + push).
	runForja(t, "rules", "add", "a",
		"--pkg", PkgDev,
		"--host", "example.com", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "rules", "add", "b",
		"--pkg", PkgDev,
		"--host", "example.com", "--path", "/other",
		"--status", "503",
	)
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Sanity: before off, both should be in PkgDev's enabled list.
	if st := readStatusJSON(t); !st.IsEnabled(PkgDev, "a") || !st.IsEnabled(PkgDev, "b") {
		t.Fatalf("status.json before off should have a,b enabled on %s, got %+v", PkgDev, st)
	}

	// Off.
	out := runForja(t, "off", "--pkg", PkgDev)
	if !strings.Contains(out, "cleared rules") {
		t.Errorf("expected off output to mention cleared rules, got: %s", out)
	}

	// Status.json[PkgDev].enabled should now be empty.
	st := readStatusJSON(t)
	if st.IsEnabled(PkgDev, "a") || st.IsEnabled(PkgDev, "b") {
		t.Errorf("status.json after off: want a,b disabled on %s, got %+v", PkgDev, st)
	}

	// Tapping the button now returns 200 (no rewrite).
	clearLogcat(t)
	maestroFlow(t, "tap_singleton_assert_200.yaml")
}

// TestCoreBodyFile — bodyFile is read at push time and the response body is
// the file's content.
func TestCoreBodyFile(t *testing.T) {
	resetForjaState(t, PkgDev)
	forceStop(t, PkgDev)
	startMainActivity(t, PkgDev)

	// Copy the fixture into the forja directory so its bodyFile path resolves
	// relative to forja/rules.local.yml.
	mkForjaResponsesDir(t, "teapot_response.json")

	runForja(t, "rules", "add", "bodyfile-rule",
		"--pkg", PkgDev,
		"--host", "example.com", "--path", "/",
		"--status", "418",
		"--body-file", "responses/teapot_response.json",
	)
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// The fixture contains the string "forja-e2e"; assert UI shows it.
	clearLogcat(t)
	flow := `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 15000
- assertVisible:
    text: ".*forja-e2e.*"
`
	runInlineMaestro(t, flow)
}
