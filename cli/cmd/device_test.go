package cmd

import (
	"strings"
	"testing"

	"github.com/tkhskt/forja/internal/adb"
)

func TestChooseDeviceSingle(t *testing.T) {
	got, err := chooseDevice("", []adb.Device{{Serial: "emulator-5554", State: "device"}})
	if err != nil {
		t.Fatalf("single usable device should resolve, got %v", err)
	}
	if got != "emulator-5554" {
		t.Errorf("got %q", got)
	}
}

func TestChooseDeviceNoneConnected(t *testing.T) {
	if _, err := chooseDevice("", nil); err == nil {
		t.Fatal("no devices should error")
	}
	// An unauthorized device is present but not usable → still an error.
	if _, err := chooseDevice("", []adb.Device{{Serial: "x", State: "unauthorized"}}); err == nil {
		t.Fatal("no *usable* device should error")
	}
}

func TestChooseDeviceAmbiguous(t *testing.T) {
	devices := []adb.Device{
		{Serial: "emulator-5554", State: "device"},
		{Serial: "RZ8N70ABCDE", State: "device", Model: "Pixel_7"},
	}
	_, err := chooseDevice("", devices)
	if err == nil {
		t.Fatal("multiple usable devices with no preference should error")
	}
	// The error must list both serials so the user can pick.
	if !strings.Contains(err.Error(), "emulator-5554") || !strings.Contains(err.Error(), "RZ8N70ABCDE") {
		t.Errorf("ambiguity error should list devices, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--device") {
		t.Errorf("ambiguity error should mention --device, got: %v", err)
	}
}

func TestChooseDeviceExplicitDisambiguates(t *testing.T) {
	devices := []adb.Device{
		{Serial: "emulator-5554", State: "device"},
		{Serial: "RZ8N70ABCDE", State: "device"},
	}
	got, err := chooseDevice("RZ8N70ABCDE", devices)
	if err != nil {
		t.Fatalf("explicit serial should resolve among several, got %v", err)
	}
	if got != "RZ8N70ABCDE" {
		t.Errorf("got %q", got)
	}
}

func TestChooseDeviceExplicitNotConnected(t *testing.T) {
	_, err := chooseDevice("ghost", []adb.Device{{Serial: "emulator-5554", State: "device"}})
	if err == nil {
		t.Fatal("a serial that isn't connected should error")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("want a 'not connected' error, got: %v", err)
	}
}

func TestChooseDeviceExplicitNotReady(t *testing.T) {
	// The requested device exists but is unauthorized — a distinct, clearer
	// error than "not connected".
	_, err := chooseDevice("RZ8N70ABCDE", []adb.Device{{Serial: "RZ8N70ABCDE", State: "unauthorized"}})
	if err == nil {
		t.Fatal("an unauthorized target should error")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("want a 'not ready' error, got: %v", err)
	}
}

func TestDeviceLabel(t *testing.T) {
	if got := deviceLabel(adb.Device{Serial: "emulator-5554"}); got != "emulator-5554" {
		t.Errorf("no-model label should be the bare serial, got %q", got)
	}
	got := deviceLabel(adb.Device{Serial: "RZ8N70ABCDE", Model: "Pixel_7"})
	if !strings.Contains(got, "Pixel_7") || !strings.Contains(got, "RZ8N70ABCDE") {
		t.Errorf("label should include model and serial, got %q", got)
	}
}
