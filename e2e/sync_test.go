//go:build e2e

// Device-sync coverage for `forja rules`: ensures the add / update / remove
// / off flow keeps status.json, the yml catalog, and the device's effective
// state coherent across every state transition users hit.
//
// Tests drive the non-interactive paths (rules add / update / remove,
// apply, off) so they can run without a TTY. The TUI's interactive surface
// is unit-tested via bubbletea models in internal/tui.
package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSyncAfterAddImmediatelyEffective: add a rule and the device must see it
// without an explicit `forja rules` (= auto-push works end-to-end).
func TestSyncAfterAddImmediatelyEffective(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)
	clearLogcat(t)

	runForja(t, "rules", "add", "immediate",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "immediate")
	waitForLogcat(t, "self-destruct mode enabled", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")
}

// TestSyncUpdatePatchAppliesToDevice: update a rule's status code and the
// device should reflect the new value immediately.
func TestSyncUpdatePatchAppliesToDevice(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "iter",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "iter")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	runForja(t, "rules", "update", "iter", "--status", "503")
	// Status code in the response depends on the next push being effective.
	// The agent uses lazy ingestion on rules() call → we trigger a request
	// to force the read.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 503.*"
    timeout: 30000
`)
}

// TestSyncRemoveDropsFromDevice: removing a rule should drop it from the
// device immediately.
func TestSyncRemoveDropsFromDevice(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "ephemeral",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "ephemeral")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	runForja(t, "rules", "remove", "ephemeral")
	// After remove, the next request should see 200.
	maestroFlow(t, "tap_singleton_assert_200.yaml")
}

// TestSyncOffStatusDisablesAllInJSON: forja off pushes [] AND empties the
// app's enabled list in status.json (= view-truth invariant).
func TestSyncOffStatusDisablesAllInJSON(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	// All three rules added then enabled on AppDev. x and y live in the
	// project catalog (rules.yml — the default), z is added with --local so
	// the off command's "yml files must not be touched" assertion exercises
	// both files. After this, status.json[AppDev].enabled = [x, y, z].
	runForja(t, "rules", "add", "x", "--host", "127.0.0.1", "--status", "418")
	runForja(t, "apply", "--app", AppDev, "--enable", "x")
	runForja(t, "rules", "add", "y", "--host", "127.0.0.1", "--path", "/x", "--status", "503")
	runForja(t, "apply", "--app", AppDev, "--enable", "y")
	runForja(t, "rules", "add", "z", "--local",
		"--host", "127.0.0.1", "--path", "/y", "--status", "401")
	runForja(t, "apply", "--app", AppDev, "--enable", "z")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	runForja(t, "off", "--app", AppDev)
	st := readStatusJSON(t)
	for _, name := range []string{"x", "y", "z"} {
		if st.IsEnabled(AppDev, name) {
			t.Errorf("after off: %q should be disabled on %s, got %+v", name, AppDev, st)
		}
	}
	// yml files must not be touched — x lives in rules.yml (project default),
	// z lives in rules.local.yml. Both should survive `forja off`.
	if !strings.Contains(readRulesYml(t, "rules.yml"), "name: x") {
		t.Errorf("rules.yml lost rule x after off")
	}
	if !strings.Contains(readRulesYml(t, "rules.local.yml"), "name: z") {
		t.Errorf("rules.local.yml lost rule z after off")
	}
}

// TestSyncProcessKillThenNewPushReAttaches: kill the app so the cached PID
// goes stale, then trigger any push (here via `rules update --status`) and
// verify the cmd layer re-attaches to the new PID and the new rule definition
// takes effect on device.
func TestSyncProcessKillThenNewPushReAttaches(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "rk",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "rk")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	pidBefore := pidof(t, AppDev)
	if pidBefore == 0 {
		t.Fatal("app should be running before kill")
	}

	forceStop(t, AppDev)
	startMainActivity(t, AppDev)
	pidAfter := pidof(t, AppDev)
	if pidAfter == pidBefore {
		t.Fatalf("PID should change after force-stop+start: %d → %d", pidBefore, pidAfter)
	}

	// Now run an update to trigger re-attach.
	clearLogcat(t)
	runForja(t, "rules", "update", "rk", "--status", "503")
	// Re-attach should happen → ForjaAgent attached again on the new PID.
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 503.*"
    timeout: 30000
`)
}

// TestSyncNoOpAfterIdenticalState: an `update` that doesn't change anything
// should still be idempotent (no error, push works, no churn).
func TestSyncNoOpUpdateIsIdempotent(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "noop",
		"--host", "127.0.0.1", "--status", "418")
	runForja(t, "apply", "--app", AppDev, "--enable", "noop")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Calling update with no fields shouldn't change anything but also
	// shouldn't error.
	out, err := runForjaAllowingFailure(t, "rules", "update", "noop")
	if err != nil {
		t.Fatalf("empty update should succeed: %v\n%s", err, out)
	}
	// Yml should look the same as right after add (no enabled field, etc.).
	yml := readRulesYml(t, "rules.yml")
	if !strings.Contains(yml, "name: noop") || !strings.Contains(yml, "status: 418") {
		t.Errorf("yml content changed unexpectedly after no-op update:\n%s", yml)
	}
}

// TestSyncProjectAndUserBothPushed: rules in both scopes are merged and the
// device gets all enabled ones.
func TestSyncProjectAndUserBothPushed(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	// team-rule lives in project yml (committed) — the default scope. Without
	// an `apply --enable` it's a pure catalog entry, not yet pushed to any
	// device.
	runForja(t, "rules", "add", "team-rule",
		"--host", "team.local", "--status", "200")
	// user-rule lives in local yml (--local opt-in) so the test exercises
	// project + local both being merged at push time.
	runForja(t, "rules", "add", "user-rule", "--local",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "user-rule")
	// Enable team-rule on AppDev too so both scopes' rules are effective.
	runForja(t, "apply", "--app", AppDev, "--enable", "team-rule")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// The hit should come from user-rule (it matches the sample app's URL).
	maestroFlow(t, "tap_singleton_assert_418.yaml")
	waitForLogcat(t, "hit 'user-rule'", 10*time.Second, "Forja")
}

// TestSyncManualYmlEditTakesEffectOnNextCommand: a user edits forja/rules.yml
// in their editor (instead of `forja rules update`). Any subsequent CLI command
// that touches the same scope re-reads the yml from disk and propagates the
// manual change to the device — yml is the source of truth, the CLI is just
// one way of mutating it.
func TestSyncManualYmlEditTakesEffectOnNextCommand(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "hand-edit",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "hand-edit")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	// Hand-edit: swap status: 418 → 503 directly in the yml.
	ymlPath := filepath.Join(repoRoot, "forja", "rules.yml")
	raw, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Fatalf("read yml: %v", err)
	}
	edited := strings.Replace(string(raw), "status: 418", "status: 503", 1)
	if edited == string(raw) {
		t.Fatalf("expected to replace status: 418 → 503; yml was:\n%s", raw)
	}
	if err := os.WriteFile(ymlPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("write yml: %v", err)
	}

	// Trigger re-sync via a no-op `rules update`. The engine re-loads the yml
	// from disk and re-pushes, so the hand-edited status code goes to the device.
	runForja(t, "rules", "update", "hand-edit")

	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 503.*"
    timeout: 30000
`)
}

// TestSyncManualYmlAddNewRuleIsPicked: appending a brand-new rule entry
// directly in yml is picked up by the next `apply --enable`, which sees the
// fresh catalog entry and pushes the rule to the device.
func TestSyncManualYmlAddNewRuleIsPicked(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	// A stub rule on /unused — its only job is to bring up the agent. The
	// hand-added rule on / is what should actually take effect.
	runForja(t, "rules", "add", "stub",
		"--host", "127.0.0.1", "--path", "/unused", "--status", "200",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "stub")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Append a fresh catalog entry to rules.yml by hand. Indentation
	// matches what the writer emits (2-space list items under rules:).
	ymlPath := filepath.Join(repoRoot, "forja", "rules.yml")
	raw, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Fatalf("read yml: %v", err)
	}
	appended := string(raw) + `  - name: hand-added
    match:
      host: 127.0.0.1
      path: /
    response:
      status: 418
      body: '{"by":"manual-yml"}'
`
	if err := os.WriteFile(ymlPath, []byte(appended), 0o644); err != nil {
		t.Fatalf("write yml: %v", err)
	}

	// Apply: enable hand-added on AppDev → reads updated yml → pushes.
	runForja(t, "apply", "--app", AppDev, "--enable", "hand-added")

	// Tapping / should hit the hand-added rule.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*manual-yml.*"
`)
}

// TestSyncManualYmlRemoveRuleDropsFromDevice: deleting a rule entry directly in
// yml (without `forja rules remove`) drops it from the device on next sync.
func TestSyncManualYmlRemoveRuleDropsFromDevice(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "doomed",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "doomed")
	// A second rule (also enabled on AppDev) we can `update --no-op` against
	// to trigger re-sync after the doomed entry is gone. Without an apply
	// on AppDev, keep would be yml-only and update wouldn't auto-propagate
	// anywhere.
	runForja(t, "rules", "add", "keep",
		"--host", "127.0.0.1", "--path", "/unused",
		"--status", "200",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "keep")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	// Strip the `doomed` entry from yml.
	ymlPath := filepath.Join(repoRoot, "forja", "rules.yml")
	raw, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Fatalf("read yml: %v", err)
	}
	cleaned := stripYmlRuleByName(string(raw), "doomed")
	if cleaned == string(raw) {
		t.Fatalf("expected to remove `doomed` entry; yml was:\n%s", raw)
	}
	if err := os.WriteFile(ymlPath, []byte(cleaned), 0o644); err != nil {
		t.Fatalf("write yml: %v", err)
	}

	// Trigger re-sync against the remaining rule.
	runForja(t, "rules", "update", "keep")

	maestroFlow(t, "tap_singleton_assert_200.yaml")
}

// TestSyncOffPushesEmptyArray: device's rules.json should be replaced with
// [] (= the agent reads, finds empty, removes the file, behavior reverts).
func TestSyncOffPushesEmptyArray(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)
	// Clear logcat + wait on "self-destruct mode enabled" instead of the
	// earlier "agent attached" signal. The attach line fires before the
	// reflective interceptor injection finishes, so a maestro tap that
	// races right after can hit OkHttp before the RulesInterceptor is in
	// the chain — producing the original 200 instead of the rewritten 418.
	clearLogcat(t)

	runForja(t, "rules", "add", "before-off",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "before-off")
	waitForLogcat(t, "self-destruct mode enabled", 30*time.Second, "ForjaAgent")
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	runForja(t, "off", "--app", AppDev)
	maestroFlow(t, "tap_singleton_assert_200.yaml")
}

// ============================================================================
// `forja sync` command
// ============================================================================
//
// sync is the explicit "re-push the current effective state" entry point.
// Use case: hand-edit forja/rules.yml, then run `forja sync` to make the
// change visible on every app that already has the affected rule enabled.
// sync NEVER writes status.json — only reads.

// TestSyncCommandReflectsManualBodyEdit: hand-edit the body of an enabled
// rule and verify that `forja sync` (with no args) propagates the change to
// the device.
func TestSyncCommandReflectsManualBodyEdit(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)

	runForja(t, "rules", "add", "synced-body",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"before":"sync"}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "synced-body")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*before.*"
`)

	// Hand-edit: replace the body JSON directly in the yml.
	ymlPath := filepath.Join(repoRoot, "forja", "rules.yml")
	raw, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Fatalf("read yml: %v", err)
	}
	edited := strings.Replace(string(raw), `{"before":"sync"}`, `{"after":"sync"}`, 1)
	if edited == string(raw) {
		t.Fatalf("expected to replace body in yml; raw was:\n%s", raw)
	}
	if err := os.WriteFile(ymlPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("write yml: %v", err)
	}

	// Push the hand edit via the explicit sync command.
	runForja(t, "sync")

	startMainActivity(t, AppDev)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*after.*"
`)
}

// TestSyncCommandPkgFilterOnlyAffectsTarget: when both AppDev and AppStaging
// have the same rule enabled, `forja sync --app AppDev` must update AppDev
// only — AppStaging keeps the pre-edit body.
func TestSyncCommandPkgFilterOnlyAffectsTarget(t *testing.T) {
	resetForjaState(t, AppDev, AppStaging)
	for _, p := range []string{AppDev, AppStaging} {
		forceStop(t, p)
		startMainActivity(t, p)
	}

	// Enable a single rule on both apps with the same starting body.
	runForja(t, "rules", "add", "filter-rule",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"phase":"before"}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "filter-rule")
	runForja(t, "apply", "--app", AppStaging, "--enable", "filter-rule")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Confirm baseline: both apps see "before".
	for _, app := range []string{"com.tkhskt.forja.sample", "com.tkhskt.forja.sample.staging"} {
		runInlineMaestro(t, "appId: "+app+`
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*before.*"
`)
	}

	// Hand-edit body, then sync ONLY AppDev.
	ymlPath := filepath.Join(repoRoot, "forja", "rules.yml")
	raw, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Fatalf("read yml: %v", err)
	}
	edited := strings.Replace(string(raw), `{"phase":"before"}`, `{"phase":"after"}`, 1)
	if edited == string(raw) {
		t.Fatalf("expected to replace body in yml; raw was:\n%s", raw)
	}
	if err := os.WriteFile(ymlPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("write yml: %v", err)
	}
	runForja(t, "sync", "--app", AppDev)

	// AppDev should now show "after".
	startMainActivity(t, AppDev)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*after.*"
`)

	// AppStaging should still show "before" (untouched by --app-filtered sync).
	startMainActivity(t, AppStaging)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.staging
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*before.*"
`)
}

// TestSyncCommandRejectsUnknownPkg: `forja sync --app X` must fail when X has
// no status.json entry, telling the user to run `forja apply` first.
func TestSyncCommandRejectsUnknownPkg(t *testing.T) {
	resetForjaState(t, AppDev)

	// Seed status.json with a different app so the file isn't empty.
	runForja(t, "rules", "add", "stub", "--host", "127.0.0.1", "--path", "/foo", "--status", "418")
	runForja(t, "apply", "--app", AppDev, "--enable", "stub")

	out, err := runForjaAllowingFailure(t, "sync", "--app", "com.no.such.pkg")
	if err == nil {
		t.Fatalf("expected sync of an unknown pkg to fail; got success. output:\n%s", out)
	}
	if !strings.Contains(out, "no status.json entry") {
		t.Errorf("expected error to mention 'no status.json entry'; got:\n%s", out)
	}
}

// TestSyncCommandWithEmptyStatusErrors: `forja sync` with no status entries
// at all has nothing to do — it should error rather than silently no-op.
func TestSyncCommandWithEmptyStatusErrors(t *testing.T) {
	resetForjaState(t, AppDev)

	// Don't add anything; status.json never gets created.
	out, err := runForjaAllowingFailure(t, "sync")
	if err == nil {
		t.Fatalf("expected sync with empty status to fail; got success. output:\n%s", out)
	}
	if !strings.Contains(out, "nothing to sync") {
		t.Errorf("expected error to mention 'nothing to sync'; got:\n%s", out)
	}
}
