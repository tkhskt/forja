//go:build e2e

// Multi-device plumbing coverage. The suite runs against a single emulator, so
// we can't exercise the "pick between two devices" path here, but we CAN prove
// the --device flag threads a serial all the way through apply/off to the
// device (attach + push + status), which is the load-bearing part of the
// multi-device change.
package e2e_test

import (
	"strings"
	"testing"
	"time"
)

// firstDeviceSerial returns the serial of the first ready device from
// `adb devices`, failing the test if none is usable.
func firstDeviceSerial(t *testing.T) string {
	t.Helper()
	out, _ := runCmd(t, false, "adb", "devices")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "device" {
			return fields[0]
		}
	}
	t.Fatal("no ready device found via `adb devices`")
	return ""
}

// TestDeviceExplicitFlag drives apply + off with an explicit --device <serial>
// and asserts the rewrite lands on the device and clears again — proving the
// serial is threaded through the engine/adb/status layers end to end.
func TestDeviceExplicitFlag(t *testing.T) {
	serial := firstDeviceSerial(t)
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)
	clearLogcat(t)

	runForja(t, "rules", "add", "dev-flag-teapot",
		"--host", "127.0.0.1", "--path", "/",
		"--status", "418",
		"--body", `{"rewritten":true}`,
	)
	runForja(t, "apply", "--device", serial, "--app", AppDev, "--enable", "dev-flag-teapot")
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")

	// status.json is now keyed by serial; the rule must be enabled for AppDev.
	if st := readStatusJSON(t); !st.IsEnabled(AppDev, "dev-flag-teapot") {
		t.Errorf("status.json should have dev-flag-teapot enabled for %s", AppDev)
	}

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
    text: ".*rewritten.*"
`)

	// off with --device too → back to baseline.
	runForja(t, "off", "--device", serial, "--app", AppDev)
	if st := readStatusJSON(t); st.IsEnabled(AppDev, "dev-flag-teapot") {
		t.Errorf("status.json should have dev-flag-teapot disabled after off")
	}
	clearLogcat(t)
	maestroFlow(t, "tap_singleton_assert_200.yaml")
}
