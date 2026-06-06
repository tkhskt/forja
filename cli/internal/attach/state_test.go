package attach

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testTTL = 12 * time.Hour

func TestLoadMissingReturnsNilNil(t *testing.T) {
	c := NewCache(t.TempDir())
	s, err := c.Load("com.example.app")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s != nil {
		t.Errorf("want nil for missing, got %+v", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	now := time.Unix(1_700_000_000, 0)
	if err := c.Save("com.example.app", 12345, now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s, err := c.Load("com.example.app")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s == nil {
		t.Fatal("want state, got nil")
	}
	if s.Package != "com.example.app" {
		t.Errorf("package mismatch: %q", s.Package)
	}
	if s.Pid != 12345 {
		t.Errorf("pid mismatch: %d", s.Pid)
	}
	if s.AttachedAt != now.Unix() {
		t.Errorf("attached_at mismatch: %d vs %d", s.AttachedAt, now.Unix())
	}
}

func TestSaveCreatesCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "nested")
	c := NewCache(dir)
	if err := c.Save("com.example.app", 1, time.Now()); err != nil {
		t.Fatalf("Save in non-existing dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("cache dir not created: %v", err)
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	c := NewCache(t.TempDir())
	if err := c.Save("", 1, time.Now()); err == nil {
		t.Error("want error for empty pkg")
	}
	if err := c.Save("com.example.app", 0, time.Now()); err == nil {
		t.Error("want error for pid 0")
	}
	if err := c.Save("com.example.app", -1, time.Now()); err == nil {
		t.Error("want error for negative pid")
	}
}

func TestLoadCorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	if err := os.WriteFile(c.path("com.example.app"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := c.Load("com.example.app")
	if err == nil {
		t.Error("want error on corrupt cache")
	}
}

func TestClearRemovesFile(t *testing.T) {
	c := NewCache(t.TempDir())
	_ = c.Save("com.example.app", 1, time.Now())
	if err := c.Clear("com.example.app"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(c.path("com.example.app")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err=%v", err)
	}
	// Clearing a non-existent file should not error
	if err := c.Clear("com.example.app"); err != nil {
		t.Errorf("Clear on missing should be noop: %v", err)
	}
}

func TestShouldReattachProcessNotRunning(t *testing.T) {
	now := time.Now()
	cached := &State{Package: "p", Pid: 100, AttachedAt: now.Unix()}
	d := ShouldReattach(cached, 0, now, testTTL)
	if d.Reattach {
		t.Errorf("want no reattach when process is gone")
	}
}

func TestRecordActionUpdatesOnlyActionFields(t *testing.T) {
	c := NewCache(t.TempDir())
	now := time.Unix(1_700_000_000, 0)
	if err := c.Save("com.example.app", 12345, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(5 * time.Minute)
	if err := c.RecordAction("com.example.app", ActionOff, later); err != nil {
		t.Fatalf("RecordAction: %v", err)
	}
	s, err := c.Load("com.example.app")
	if err != nil || s == nil {
		t.Fatalf("Load after RecordAction: %v %v", s, err)
	}
	if s.Pid != 12345 || s.AttachedAt != now.Unix() {
		t.Errorf("attach fields mutated unexpectedly: %+v", s)
	}
	if s.LastAction != ActionOff || s.LastActionAt != later.Unix() {
		t.Errorf("action fields not set: %+v", s)
	}
}

func TestRecordActionNoopWhenNoPriorState(t *testing.T) {
	c := NewCache(t.TempDir())
	// No Save call → no prior state. RecordAction should not error.
	if err := c.RecordAction("com.example.app", ActionPush, time.Now()); err != nil {
		t.Errorf("RecordAction on missing state should be noop: %v", err)
	}
	s, _ := c.Load("com.example.app")
	if s != nil {
		t.Errorf("RecordAction should not create a state from nothing: %+v", s)
	}
}

func TestShouldReattachNoCache(t *testing.T) {
	d := ShouldReattach(nil, 12345, time.Now(), testTTL)
	if !d.Reattach {
		t.Errorf("want reattach when no cache")
	}
}

func TestShouldReattachPidChanged(t *testing.T) {
	now := time.Now()
	cached := &State{Package: "p", Pid: 100, AttachedAt: now.Unix()}
	d := ShouldReattach(cached, 200, now, testTTL)
	if !d.Reattach {
		t.Errorf("want reattach on PID change")
	}
}

func TestShouldReattachWithinTTL(t *testing.T) {
	now := time.Now()
	cached := &State{Package: "p", Pid: 100, AttachedAt: now.Add(-1 * time.Hour).Unix()}
	d := ShouldReattach(cached, 100, now, testTTL)
	if d.Reattach {
		t.Errorf("want no reattach within TTL, reason=%s", d.Reason)
	}
}

func TestShouldReattachExpiredTTL(t *testing.T) {
	now := time.Now()
	cached := &State{Package: "p", Pid: 100, AttachedAt: now.Add(-24 * time.Hour).Unix()}
	d := ShouldReattach(cached, 100, now, testTTL)
	if !d.Reattach {
		t.Errorf("want reattach when older than TTL")
	}
}
