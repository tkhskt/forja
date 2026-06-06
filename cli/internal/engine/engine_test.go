package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tkhskt/forja/internal/adb"
	"github.com/tkhskt/forja/internal/attach"
	"github.com/tkhskt/forja/internal/config"
)

// recordingExecutor records each adb invocation and returns canned responses
// based on a substring match against the joined command line.
type recordingExecutor struct {
	calls  []string // joined "name arg arg" for assertions
	stdins [][]byte // matches order of calls; nil when no stdin
	canned []recCanned
}

type recCanned struct {
	contains string
	stdout   string
	err      error
}

func (r *recordingExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	return r.dispatch(name, args, nil)
}

func (r *recordingExecutor) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	return r.dispatch(name, args, stdin)
}

func (r *recordingExecutor) dispatch(name string, args []string, stdin []byte) ([]byte, []byte, error) {
	joined := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	r.stdins = append(r.stdins, stdin)
	for _, c := range r.canned {
		if strings.Contains(joined, c.contains) {
			return []byte(c.stdout), nil, c.err
		}
	}
	return nil, nil, nil // default: empty stdout, no error
}

// newEngineWith returns an Engine wired to a fake ADB + tempdir cache + bundle.
// The bundle directory contains stub .so and .dex files so attach() can read them.
func newEngineWith(t *testing.T, fx *recordingExecutor) *Engine {
	t.Helper()
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "libforja-agent-arm64-v8a.so"), []byte("SO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, AgentDexName), []byte("DEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := attach.NewCache(t.TempDir())
	return &Engine{
		ADB:    adb.NewWithExecutor(fx),
		Cache:  cache,
		Bundle: bundle,
		Now:    func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
}

func TestEnsureAttachedNotRunning(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			// pidof fails (= not running)
			{contains: "pidof com.example.app", err: errors.New("exit 1")},
		},
	}
	e := newEngineWith(t, fx)
	err := e.EnsureAttached(context.Background(), "com.example.app")
	if err == nil {
		t.Fatal("expected error when app is not running")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error should mention not running, got: %v", err)
	}
}

func TestEnsureAttachedFirstTime(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof com.example.app", stdout: "12345\n"},
			{contains: "getprop ro.product.cpu.abi", stdout: "arm64-v8a\n"},
			{contains: "run-as com.example.app sh -c"}, // .so / .dex pushes match here
			{contains: "attach-agent com.example.app"},
		},
	}
	e := newEngineWith(t, fx)
	if err := e.EnsureAttached(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	// We should have seen pidof + getprop + 2x run-as write + attach-agent
	wantSubstrings := []string{"pidof", "getprop ro.product.cpu.abi", "attach-agent"}
	for _, w := range wantSubstrings {
		found := false
		for _, c := range fx.calls {
			if strings.Contains(c, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing call containing %q\nall calls: %v", w, fx.calls)
		}
	}
	// And the cache should now have a record so subsequent EnsureAttached skips.
	s, err := e.Cache.Load("com.example.app")
	if err != nil || s == nil {
		t.Fatalf("cache not saved: state=%v err=%v", s, err)
	}
	if s.Pid != 12345 {
		t.Errorf("cache pid wrong: %d", s.Pid)
	}
}

func TestEnsureAttachedSkipsWhenFresh(t *testing.T) {
	// Save a fresh state.
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 12345, e.now())

	if err := e.EnsureAttached(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	// Should have ONLY called pidof, nothing else.
	for _, c := range fx.calls {
		if !strings.Contains(c, "pidof") {
			t.Errorf("unexpected call when cache is fresh: %s", c)
		}
	}
}

func TestEnsureAttachedReattachesWhenPidChanged(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "99999\n"}, // process restarted, new PID
			{contains: "getprop", stdout: "arm64-v8a\n"},
			{contains: "run-as"},
			{contains: "attach-agent"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 12345, e.now()) // old PID

	if err := e.EnsureAttached(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	gotAttach := false
	for _, c := range fx.calls {
		if strings.Contains(c, "attach-agent") {
			gotAttach = true
		}
	}
	if !gotAttach {
		t.Error("expected re-attach when PID changes")
	}
	// Cache should now reflect new PID.
	s, _ := e.Cache.Load("com.example.app")
	if s.Pid != 99999 {
		t.Errorf("cache pid not updated: got %d", s.Pid)
	}
}

func TestPushSendsEnabledRulesAsJson(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
		},
	}
	e := newEngineWith(t, fx)
	// Pre-populate cache so we skip attach (focus this test on the rules push).
	_ = e.Cache.Save("com.example.app", 12345, e.now())

	rf := &config.RulesFile{
		Rules: []config.Rule{
			{Name: "a", Enabled: true, Host: "x.com", Status: 500},
			{Name: "b", Enabled: false, Host: "y.com", Status: 200},
		},
	}
	if err := e.Push(context.Background(), "com.example.app", rf); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// Find the run-as write for rules.json
	var stdin []byte
	for i, c := range fx.calls {
		if strings.Contains(c, "rules.json") {
			stdin = fx.stdins[i]
			break
		}
	}
	if stdin == nil {
		t.Fatalf("no rules.json write seen; calls: %v", fx.calls)
	}
	s := string(stdin)
	if !strings.Contains(s, `"name": "a"`) {
		t.Errorf("enabled rule 'a' missing from push: %s", s)
	}
	if strings.Contains(s, `"name": "b"`) {
		t.Errorf("disabled rule 'b' should not appear in push: %s", s)
	}
}

func TestPushFailsWithEmptyPackage(t *testing.T) {
	fx := &recordingExecutor{}
	e := newEngineWith(t, fx)
	rf := &config.RulesFile{}
	if err := e.Push(context.Background(), "", rf); err == nil {
		t.Error("expected Push to reject empty package")
	}
	if len(fx.calls) != 0 {
		t.Errorf("should not have called adb: %v", fx.calls)
	}
}

func TestQueryAttachStatusAppNotRunning(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", err: errors.New("exit 1")},
		},
	}
	e := newEngineWith(t, fx)
	s := e.QueryAttachStatus(context.Background(), "com.example.app")
	if s.Kind != StatusAppNotRunning {
		t.Errorf("want AppNotRunning, got %v", s.Kind)
	}
	if s.Live() {
		t.Error("AppNotRunning should not be Live()")
	}
}

func TestQueryAttachStatusNoCache(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
		},
	}
	e := newEngineWith(t, fx)
	s := e.QueryAttachStatus(context.Background(), "com.example.app")
	if s.Kind != StatusNoAttachRecord {
		t.Errorf("want NoAttachRecord, got %v", s.Kind)
	}
	if s.CurrentPid != 12345 {
		t.Errorf("CurrentPid: want 12345, got %d", s.CurrentPid)
	}
}

func TestQueryAttachStatusStale(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "99999\n"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 11111, e.now())

	s := e.QueryAttachStatus(context.Background(), "com.example.app")
	if s.Kind != StatusAgentStale {
		t.Errorf("want AgentStale, got %v", s.Kind)
	}
	if s.CurrentPid != 99999 || s.CachedPid != 11111 {
		t.Errorf("PIDs: %+v", s)
	}
	if s.Live() {
		t.Error("Stale should not be Live()")
	}
	// The user-facing message should mention both PIDs.
	msg := s.Message()
	if !strings.Contains(msg, "11111") || !strings.Contains(msg, "99999") {
		t.Errorf("message should include both pids: %q", msg)
	}
}

func TestQueryAttachStatusLive(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 12345, e.now())

	s := e.QueryAttachStatus(context.Background(), "com.example.app")
	if s.Kind != StatusAgentLive {
		t.Errorf("want AgentLive, got %v", s.Kind)
	}
	if !s.Live() {
		t.Error("Live status should report Live() == true")
	}
}

func TestQueryAttachStatusLiveButOffAfterOff(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
			{contains: "run-as"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 12345, e.now())

	// Off should both write [] to device AND mark the cached state as off.
	if err := e.Off(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("Off: %v", err)
	}
	s := e.QueryAttachStatus(context.Background(), "com.example.app")
	if s.Kind != StatusAgentLiveButOff {
		t.Errorf("after Off: want AgentLiveButOff, got %v", s.Kind)
	}
	if s.Live() {
		t.Error("LiveButOff should report Live() == false (rules aren't effective)")
	}
	if !strings.Contains(s.Message(), "forja off") {
		t.Errorf("message should mention forja off: %q", s.Message())
	}
}

func TestQueryAttachStatusLiveAfterPushFollowingOff(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
			{contains: "run-as"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 12345, e.now())

	// Off then push: the second action should win, restoring Live().
	if err := e.Off(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("Off: %v", err)
	}
	rf := &config.RulesFile{
		Rules: []config.Rule{{Name: "a", Enabled: true, Host: "x.com", Status: 500}},
	}
	if err := e.Push(context.Background(), "com.example.app", rf); err != nil {
		t.Fatalf("Push after Off: %v", err)
	}
	s := e.QueryAttachStatus(context.Background(), "com.example.app")
	if s.Kind != StatusAgentLive {
		t.Errorf("after Push following Off: want AgentLive, got %v", s.Kind)
	}
}

func TestOffWritesEmptyArray(t *testing.T) {
	fx := &recordingExecutor{
		canned: []recCanned{
			{contains: "pidof", stdout: "12345\n"},
		},
	}
	e := newEngineWith(t, fx)
	_ = e.Cache.Save("com.example.app", 12345, e.now())

	if err := e.Off(context.Background(), "com.example.app"); err != nil {
		t.Fatalf("Off: %v", err)
	}
	for i, c := range fx.calls {
		if strings.Contains(c, "rules.json") {
			if strings.TrimSpace(string(fx.stdins[i])) != "[]" {
				t.Errorf("Off should write '[]', got %q", fx.stdins[i])
			}
			return
		}
	}
	t.Errorf("no rules.json write found")
}
