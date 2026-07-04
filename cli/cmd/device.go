package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tkhskt/forja/internal/adb"
)

// resolveDevice picks the adb device serial a device-facing command should
// target. Precedence:
//
//   - explicit (a per-call value, e.g. an MCP tool's device argument), if set
//   - globals.Device (the persistent --device flag), if explicit was empty
//   - the sole connected device, when exactly one is usable
//
// It errors — listing the connected devices — when the requested serial isn't
// connected, when none are connected, or when several are connected and none
// was chosen. That way an ambiguous target never silently lands on the wrong
// device; the caller must disambiguate with --device.
func resolveDevice(explicit string) (string, error) {
	want := explicit
	if want == "" {
		want = globals.Device
	}
	devices, err := adb.New().Devices(context.Background())
	if err != nil {
		return "", err
	}
	return chooseDevice(want, devices)
}

// chooseDevice is the pure selection logic behind resolveDevice, split out so
// the 0 / 1 / many / explicit-match / explicit-miss cases are unit-testable
// without a real adb. want is the already-resolved preference ("" = none).
func chooseDevice(want string, devices []adb.Device) (string, error) {
	usable := make([]adb.Device, 0, len(devices))
	for _, d := range devices {
		if d.State == "device" {
			usable = append(usable, d)
		}
	}

	if want != "" {
		for _, d := range usable {
			if d.Serial == want {
				return want, nil
			}
		}
		// Present but not ready (offline/unauthorized) vs. not seen at all.
		for _, d := range devices {
			if d.Serial == want {
				return "", fmt.Errorf("device %q is %q, not ready%s", want, d.State, deviceListHint(devices))
			}
		}
		return "", fmt.Errorf("device %q is not connected%s", want, deviceListHint(devices))
	}

	switch len(usable) {
	case 0:
		return "", fmt.Errorf("no device connected%s", deviceListHint(devices))
	case 1:
		return usable[0].Serial, nil
	default:
		return "", fmt.Errorf(
			"multiple devices connected — choose one with --device SERIAL%s",
			deviceListHint(usable))
	}
}

// deviceLabel is the human-facing one-line label for a device in the TUI
// picker: model name with the serial in parens, or the bare serial when no
// model is reported (common for emulators).
func deviceLabel(d adb.Device) string {
	if d.Model != "" {
		return d.Model + "  (" + d.Serial + ")"
	}
	return d.Serial
}

// deviceListHint renders a devices block suitable for appending to an error
// message. Returns "" for an empty list so callers can concatenate freely.
func deviceListHint(devices []adb.Device) string {
	if len(devices) == 0 {
		return ""
	}
	sorted := append([]adb.Device(nil), devices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Serial < sorted[j].Serial })

	var b strings.Builder
	b.WriteString(":\n")
	for _, d := range sorted {
		b.WriteString("  " + d.Serial)
		if d.Model != "" {
			b.WriteString("  " + d.Model)
		}
		b.WriteString("  (" + d.State + ")\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
