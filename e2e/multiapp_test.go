//go:build e2e

// Multi-debuggable-app tests. The fixture app exposes two flavors
// (dev / staging) producing two applicationIds:
//
//	com.tkhskt.forja.sample
//	com.tkhskt.forja.sample.staging
//
// These tests confirm forja behaves correctly when multiple debuggable
// apps are installed and running simultaneously: detection lists both,
// attach targets just one, and operations on one don't leak into the other.
package e2e_test

import (
	"strings"
	"testing"
	"time"
)

// TestMultiAppListDebuggable: with both flavors running, the
// internal ListDebuggableApps logic should expose both. We don't have a
// direct CLI surface for listing, but we can prove the underlying mechanism
// by attaching to each one in turn — both attaches should succeed.
func TestMultiAppListBothRunning(t *testing.T) {
	// Start both apps.
	forceStop(t, AppDev)
	forceStop(t, AppStaging)
	startMainActivity(t, AppDev)
	startMainActivity(t, AppStaging)

	if pidof(t, AppDev) == 0 || pidof(t, AppStaging) == 0 {
		t.Fatal("both flavors should be running")
	}

	// Both should respond to run-as (= the canonical debuggable check forja
	// uses in its ListDebuggableApps script).
	out, err := adbShellAllowingFailure(t, "run-as "+AppDev+" true && echo dev-ok")
	if err != nil || !strings.Contains(out, "dev-ok") {
		t.Errorf("dev flavor failed run-as check: %v / %q", err, out)
	}
	out, err = adbShellAllowingFailure(t, "run-as "+AppStaging+" true && echo staging-ok")
	if err != nil || !strings.Contains(out, "staging-ok") {
		t.Errorf("staging flavor failed run-as check: %v / %q", err, out)
	}
}

// TestMultiAppAttachIsolated: attach forja to one flavor and verify the
// other flavor sees no agent and continues to behave normally.
func TestMultiAppAttachIsolated(t *testing.T) {
	resetForjaState(t, AppDev, AppStaging)
	forceStop(t, AppDev)
	forceStop(t, AppStaging)
	startMainActivity(t, AppDev)
	startMainActivity(t, AppStaging)
	clearLogcat(t)

	// Add a rule targeting only dev.
	runForja(t, "rules", "add", "dev-only",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "dev-only")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Bring dev to the foreground before tapping (both flavors are running;
	// startMainActivity above queued staging on top of dev).
	startMainActivity(t, AppDev)
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	// Staging should be unaffected. We verify behaviorally — tap its button
	// and check the response is 200, not 418. (Checking for absence of
	// /files/libforja-agent.so is unreliable: prior test runs may have
	// targeted staging too, leaving the .so on disk. What matters here is
	// whether *this* invocation attached to staging, which a 200 response
	// proves it didn't.)
	startMainActivity(t, AppStaging)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.staging
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 200.*"
    timeout: 30000
`)
}

// TestMultiAppOffDevDoesNotAffectStaging: forja off targets only the named
// app — its sibling's rules (if any) must survive untouched.
func TestMultiAppOffOnlyAffectsTarget(t *testing.T) {
	resetForjaState(t, AppDev, AppStaging)
	forceStop(t, AppDev)
	forceStop(t, AppStaging)
	startMainActivity(t, AppDev)
	startMainActivity(t, AppStaging)

	// Attach to dev.
	runForja(t, "rules", "add", "dev-rule",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "dev-rule")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	startMainActivity(t, AppDev) // both flavors started → ensure dev is on top
	maestroFlow(t, "tap_singleton_assert_418.yaml")

	// Off the dev app.
	runForja(t, "off", "--app", AppDev)
	startMainActivity(t, AppDev)
	maestroFlow(t, "tap_singleton_assert_200.yaml")

	// Staging never had forja attached — verify behaviorally. Checking for
	// absence of /files/rules.json is unreliable: prior test runs may have
	// targeted staging on the same emulator and left runtime files behind
	// that aren't cleaned up by `pm clear`. What matters here is whether
	// *this* invocation pushed a rule to staging, which a 200 response
	// proves it didn't.
	startMainActivity(t, AppStaging)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.staging
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 200.*"
    timeout: 30000
`)
}

// TestMultiAppSharedRuleAppliesToBothFlavors: a single rule definition in yml
// can be independently enabled on multiple pkgs. Disabling on one leaves the
// other intact. This is the headline feature of the multi-app refactor.
func TestMultiAppSharedRuleAppliesToBothFlavors(t *testing.T) {
	resetForjaState(t, AppDev, AppStaging)
	forceStop(t, AppDev)
	forceStop(t, AppStaging)
	startMainActivity(t, AppDev)
	startMainActivity(t, AppStaging)

	// Define the rule once in yml (no --app → catalog-only).
	runForja(t, "rules", "add", "shared",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"by":"shared-rule"}`,
	)

	// Enable on dev → attach + push.
	runForja(t, "apply", "--app", AppDev, "--enable", "shared")
	// Enable on staging → attach + push (independent agent, same rule def).
	runForja(t, "apply", "--app", AppStaging, "--enable", "shared")

	// Both flavors should now return 418 with the shared body.
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
    text: ".*shared-rule.*"
`)

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
    text: ".*shared-rule.*"
`)

	// Now disable on dev only. Staging should keep the rewrite.
	runForja(t, "apply", "--app", AppDev, "--disable", "shared")

	startMainActivity(t, AppDev)
	maestroFlow(t, "tap_singleton_assert_200.yaml")

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
`)

	// status.json should reflect: shared off for dev, on for staging.
	st := readStatusJSON(t)
	if st.IsEnabled(AppDev, "shared") {
		t.Errorf("after --disable: shared should be off for dev, got %+v", st)
	}
	if !st.IsEnabled(AppStaging, "shared") {
		t.Errorf("staging's shared was collateral-disabled: %+v", st)
	}
}

// TestMultiAppUpdatePropagatesToAllEnabledPkgs: a single `forja rules update`
// auto-pushes the new definition to every pkg where the rule is enabled.
func TestMultiAppUpdatePropagatesToAllEnabledPkgs(t *testing.T) {
	resetForjaState(t, AppDev, AppStaging)
	forceStop(t, AppDev)
	forceStop(t, AppStaging)
	startMainActivity(t, AppDev)
	startMainActivity(t, AppStaging)

	runForja(t, "rules", "add", "shared",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "shared")
	runForja(t, "apply", "--app", AppStaging, "--enable", "shared")

	// Sanity: both at 418.
	startMainActivity(t, AppDev)
	maestroFlow(t, "tap_singleton_assert_418.yaml")
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
`)

	// Single update should auto-push to BOTH pkgs.
	runForja(t, "rules", "update", "shared", "--status", "503")

	startMainActivity(t, AppDev)
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
	startMainActivity(t, AppStaging)
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.staging
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 503.*"
    timeout: 30000
`)
}

// TestMultiAppAttachBothCanSucceedSeparately: attach forja independently to
// each flavor, one after the other. Each should get its own attach state
// recorded in ~/.cache/forja, and operations on one don't disturb the other.
func TestMultiAppAttachBothSequentially(t *testing.T) {
	resetForjaState(t, AppDev, AppStaging)
	forceStop(t, AppDev)
	forceStop(t, AppStaging)
	startMainActivity(t, AppDev)
	startMainActivity(t, AppStaging)

	// 1) Attach to dev.
	runForja(t, "rules", "add", "dev-x",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "apply", "--app", AppDev, "--enable", "dev-x")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Reset local state but keep dev attach cache; switch app focus to
	// staging by passing --app explicitly.
	resetForjaState(t) // wipes ./forja but NOT ~/.cache/forja entries

	// 2) Attach to staging via a rules add on the same forja/ dir, but
	// targeted at staging.
	clearLogcat(t)
	runForja(t, "rules", "add", "staging-y",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "503",
	)
	runForja(t, "apply", "--app", AppStaging, "--enable", "staging-y")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// Tap staging's button and assert 503 — proves staging got the rule
	// independently of dev's earlier 418 rule.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample.staging
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 503.*"
    timeout: 30000
`)
}
