//go:build e2e

// Package e2e_test orchestrates end-to-end tests against a real Android
// emulator (or borrowed device). Build tag `e2e` keeps it out of the default
// `go test ./...` so unit tests stay fast.
//
// Run from the e2e/ directory:
//
//	go test -tags e2e -v ./...
//
// Pass FORJA_E2E_KEEP=1 to leave the emulator running after the suite.
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Paths the helpers operate against. Resolved once in TestMain so the rest of
// the file doesn't have to think about cwd.
var (
	repoRoot    string // forja repo root (absolute)
	forjaBinary string // path to the compiled `forja` binary (in $repoRoot/bin)
	flowsDir    string // e2e/flows
	fixturesDir string // e2e/fixtures
	scriptsDir  string // e2e/scripts
)

// resolvePaths is called from TestMain.
func resolvePaths() error {
	// We're in $repoRoot/e2e at test time.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	repoRoot = filepath.Dir(abs)
	forjaBinary = filepath.Join(repoRoot, "bin", "forja")
	flowsDir = filepath.Join(abs, "flows")
	fixturesDir = filepath.Join(abs, "fixtures")
	scriptsDir = filepath.Join(abs, "scripts")
	return nil
}

// --- process helpers ---------------------------------------------------

// runCmd runs a command and returns combined stdout/stderr. Useful for
// assertions on tool output. Fails the test on non-zero exit by default;
// pass allowFail=true to inspect the error without failing.
func runCmd(t *testing.T, allowFail bool, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = repoRoot
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil && !allowFail {
		t.Fatalf("%s %v failed: %v\n--- output ---\n%s", name, args, err, out)
	}
	return out, err
}

// runCmdWithTimeout is like runCmd but with a hard timeout so a stuck
// emulator boot doesn't hang the whole suite forever.
func runCmdWithTimeout(t *testing.T, timeout time.Duration, allowFail bool, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = repoRoot
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil && !allowFail {
		t.Fatalf("%s %v failed (timeout=%s): %v\n--- output ---\n%s", name, args, timeout, err, out)
	}
	return out, err
}

// runForja runs the compiled forja binary with $repoRoot as cwd. Tests should
// reset $repoRoot/forja directory via resetForjaState() between cases so
// state doesn't leak.
func runForja(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := runCmd(t, false, forjaBinary, args...)
	return out
}

// runForjaAllowingFailure variant for negative-path tests (e.g. duplicate add).
func runForjaAllowingFailure(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCmd(t, true, forjaBinary, args...)
}

// --- adb helpers -------------------------------------------------------

// adbShell runs `adb shell <args...>` as a single shell line, matching the
// way the forja CLI does it. Returns combined output.
func adbShell(t *testing.T, line string) string {
	t.Helper()
	out, _ := runCmd(t, false, "adb", "shell", line)
	return out
}

// adbShellAllowingFailure variant for commands expected to exit non-zero
// (e.g. `pidof nonexistent` returns 1).
func adbShellAllowingFailure(t *testing.T, line string) (string, error) {
	t.Helper()
	return runCmd(t, true, "adb", "shell", line)
}

// pidof returns the PID of the named app process, or 0 if not running.
func pidof(t *testing.T, pkg string) int {
	t.Helper()
	out, _ := adbShellAllowingFailure(t, "pidof "+pkg)
	s := strings.TrimSpace(out)
	if s == "" {
		return 0
	}
	fields := strings.Fields(s)
	var pid int
	if _, err := fmt.Sscanf(fields[0], "%d", &pid); err != nil {
		return 0
	}
	return pid
}

// forceStop kills the app (= simulates the user swiping it away).
func forceStop(t *testing.T, pkg string) {
	t.Helper()
	_, _ = adbShellAllowingFailure(t, "am force-stop "+pkg)
}

// startMainActivity launches the app's MainActivity. Waits briefly for
// the process to come up.
//
// Both fixture-app flavors (dev / staging) share the same source, so the
// activity FQN is always com.tkhskt.forja.sample.MainActivity. We pass the
// absolute class name to `am start -n` rather than the `.MainActivity`
// shorthand — the shorthand resolves against the applicationId, which differs
// from the namespace for the staging flavor (= staging would otherwise look
// for com.tkhskt.forja.sample.staging.MainActivity, which doesn't exist).
const MainActivityFQN = "com.tkhskt.forja.sample.MainActivity"

func startMainActivity(t *testing.T, pkg string) {
	t.Helper()
	startMainActivityWithPath(t, pkg, "")
}

// startMainActivityWithPath launches MainActivity, optionally overriding the
// request path via the `path` intent extra. An empty path leaves the extra off
// so the fixture uses its default ("/"), keeping every existing caller's
// behavior unchanged. Used by path-matching tests (e.g. wildcards) to drive an
// arbitrary endpoint like /users/42/posts.
func startMainActivityWithPath(t *testing.T, pkg, path string) {
	t.Helper()
	line := fmt.Sprintf("am start -n %s/%s", pkg, MainActivityFQN)
	if path != "" {
		line += fmt.Sprintf(" --es path '%s'", path)
	}
	_, _ = adbShellAllowingFailure(t, line)
	// Wait for a *stable* PID. A bare "pidof > 0" was the original gate, but
	// occasionally Android reports a PID for a process that's still in the
	// middle of its launch sequence and gets killed milliseconds later by
	// am / activity manager (e.g. when force-stop's cleanup hadn't fully
	// settled). Requiring the same non-zero PID for ~500ms eliminates the
	// race where a subsequent `forja apply` would fire its own pidof and
	// see 0, surfacing as a confusing "app not running" error in the test.
	const stableWindow = 500 * time.Millisecond
	deadline := time.Now().Add(10 * time.Second)
	var lastPid int
	var stableSince time.Time
	for time.Now().Before(deadline) {
		p := pidof(t, pkg)
		switch {
		case p == 0:
			lastPid = 0
		case p != lastPid:
			lastPid = p
			stableSince = time.Now()
		case time.Since(stableSince) >= stableWindow:
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("app %s failed to reach a stable PID within 10s (last pid=%d)", pkg, lastPid)
}

// readDeviceFile reads /data/data/<pkg>/<remoteRel> via run-as. Returns
// ("", false) when the file does not exist.
func readDeviceFile(t *testing.T, pkg, remoteRel string) (string, bool) {
	t.Helper()
	line := fmt.Sprintf("run-as %s sh -c 'test -e %s && cat %s; true'", pkg, remoteRel, remoteRel)
	out, _ := adbShellAllowingFailure(t, line)
	if strings.TrimSpace(out) == "" {
		return "", false
	}
	return out, true
}

// deviceListFiles returns the contents of run-as <pkg> ls <dir>.
func deviceListFiles(t *testing.T, pkg, dir string) string {
	t.Helper()
	out, _ := adbShellAllowingFailure(t, fmt.Sprintf("run-as %s ls %s", pkg, dir))
	return out
}

// --- logcat helpers ----------------------------------------------------

// clearLogcat wipes the device log buffer so subsequent reads only see new
// entries.
func clearLogcat(t *testing.T) {
	t.Helper()
	_, _ = runCmd(t, true, "adb", "logcat", "-c")
}

// dumpLogcat returns the current contents of the log buffer for the given
// tags (e.g. "ForjaAgent" "Forja" "SampleApp"). Used in assertions.
func dumpLogcat(t *testing.T, tags ...string) string {
	t.Helper()
	args := []string{"logcat", "-d"}
	for _, tag := range tags {
		args = append(args, "-s", tag)
	}
	out, _ := runCmd(t, true, "adb", args...)
	return out
}

// waitForLogcat polls dumpLogcat() until the substring appears or timeout.
// Returns the matching log lines on success; t.Fatalf on timeout.
func waitForLogcat(t *testing.T, substr string, timeout time.Duration, tags ...string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = dumpLogcat(t, tags...)
		if strings.Contains(last, substr) {
			return last
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("logcat never produced %q within %s\n--- last dump ---\n%s",
		substr, timeout, last)
	return ""
}

// --- maestro helpers ---------------------------------------------------

// maestroPath resolves the maestro binary by checking PATH first, then the
// installer's default location (~/.maestro/bin/maestro). Computed once so
// every flow doesn't repeat the lookup.
var maestroPath = func() string {
	if p, err := exec.LookPath("maestro"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".maestro", "bin", "maestro")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}()

// maestroFlow runs `maestro test e2e/flows/<name>.yaml`. The cwd is set to
// the e2e directory so relative paths in the flow work as expected.
func maestroFlow(t *testing.T, name string) string {
	t.Helper()
	if maestroPath == "" {
		t.Skip("maestro not found in PATH or ~/.maestro/bin — install via " +
			"`curl -Ls https://get.maestro.mobile.dev | bash` then re-run")
	}
	path := filepath.Join(flowsDir, name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("maestro flow %s not found: %v", path, err)
	}
	out, err := runMaestroWithRetry(t, path)
	if err != nil {
		t.Fatalf("maestro test %s failed: %v\n--- output ---\n%s", name, err, out)
	}
	return out
}

// --- forja state helpers -----------------------------------------------

// forjaCacheDir mirrors attach.DefaultDir: the OS user cache dir + "forja"
// (~/Library/Caches/forja on macOS, $XDG_CACHE_HOME/forja or ~/.cache/forja on
// Linux). The per-app attach state and the per-project status file both live
// under here.
func forjaCacheDir() string {
	base, _ := os.UserCacheDir()
	return filepath.Join(base, "forja")
}

// projectStatusPath mirrors rules.defaultStatusPath: status.json is machine-
// managed state kept in the user cache (NOT under .forja/), at
// <cache>/forja/status/<key>.json keyed by the forja project root. Every
// runForja uses cmd.Dir = repoRoot, so the key is derived from repoRoot — the
// same absolute path the CLI sees via os.Getwd(). The key formula is mirrored
// from rules.projectKey by hand; if it drifts, these tests fail loudly (the
// status file is read at the wrong path → looks empty).
func projectStatusPath() string {
	sum := sha256.Sum256([]byte(repoRoot))
	short := hex.EncodeToString(sum[:])[:12]
	base := sanitizeKeySegment(filepath.Base(repoRoot))
	key := short
	if base != "" {
		key = base + "-" + short
	}
	return filepath.Join(forjaCacheDir(), "status", key+".json")
}

// sanitizeKeySegment mirrors rules.sanitizeKeySegment.
func sanitizeKeySegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// resetForjaState wipes ./.forja/ in repo root, removes the per-project status
// cache and the attach cache for the given apps, then runs `forja init` to
// restore the canonical post-init state. Production forja refuses to auto-create
// .forja/ — calling init here keeps every test starting from the same hermetic
// baseline (clean dir + empty rules.yml + no enabled state) without each test
// having to remember the bootstrap step.
func resetForjaState(t *testing.T, pkgs ...string) {
	t.Helper()
	_ = os.RemoveAll(filepath.Join(repoRoot, ".forja"))
	_ = os.Remove(projectStatusPath())
	cacheDir := forjaCacheDir()
	for _, pkg := range pkgs {
		_ = os.Remove(filepath.Join(cacheDir, pkg+".json"))
	}
	runForja(t, "init")
}

// StatusJSON mirrors the on-disk shape of the cached status.json: a flat top-level
// map of app → {"enabled": [...rule names]}. A rule is "on" for an app iff
// its name appears in that app's enabled list (= absent means off).
type StatusJSON map[string]struct {
	Enabled []string `json:"enabled"`
}

// IsEnabled is the test-side mirror of config.Status.IsEnabled.
func (s StatusJSON) IsEnabled(app, name string) bool {
	ps, ok := s[app]
	if !ok {
		return false
	}
	for _, n := range ps.Enabled {
		if n == name {
			return true
		}
	}
	return false
}

// readStatusJSON returns the parsed status.json from the user cache (its new
// home — it no longer lives under .forja/). Returns nil (empty map) if the
// file doesn't exist. Top-level keys starting with `$` are silently skipped —
// forja embeds a "$comment" metadata key warning users that the file is
// CLI-managed, and that string value can't decode as {Enabled []string}.
func readStatusJSON(t *testing.T) StatusJSON {
	t.Helper()
	path := projectStatusPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StatusJSON{}
		}
		t.Fatalf("open %s: %v", path, err)
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode status.json: %v", err)
	}
	out := StatusJSON{}
	for k, v := range raw {
		if strings.HasPrefix(k, "$") {
			continue
		}
		var ps struct {
			Enabled []string `json:"enabled"`
		}
		if err := json.Unmarshal(v, &ps); err != nil {
			t.Fatalf("decode status.json[%s]: %v", k, err)
		}
		out[k] = ps
	}
	return out
}

// readRulesYml returns the contents of a yml file under forja/ as a string.
// The caller can grep with strings.Contains.
func readRulesYml(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRoot, ".forja", name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// --- local mock server (replaces the external network in fixtures) -------

// mockDevicePort is the fixed device-side loopback port the fixture app
// fetches (http://127.0.0.1:8080/). `adb reverse` bridges it to the
// in-process mock server below, so tests never depend on an external host
// like example.com — responses are instant and deterministic.
const mockDevicePort = 8080

var mockServer *http.Server

// startMockServer brings up an in-process HTTP server on a free host port and
// returns that port. The baseline response is a deterministic HTTP 200 that
// the "off"/no-rewrite tests assert on; rewrite tests replace it via a forja
// rule matching host 127.0.0.1.
func startMockServer() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "forja-mock-baseline\n")
	})
	mockServer = &http.Server{Handler: mux}
	go func() { _ = mockServer.Serve(ln) }()
	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Fprintf(os.Stderr, "[e2e] mock server listening on 127.0.0.1:%d\n", port)
	return port, nil
}

func stopMockServer() {
	if mockServer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = mockServer.Shutdown(ctx)
}

// setupAdbReverse maps the device's tcp:mockDevicePort to the host's mock
// server port, so the fixture's http://127.0.0.1:<mockDevicePort>/ reaches it.
func setupAdbReverse(hostPort int) error {
	dev := fmt.Sprintf("tcp:%d", mockDevicePort)
	_ = exec.Command("adb", "reverse", "--remove", dev).Run() // clear any stale mapping
	out, err := exec.Command("adb", "reverse", dev, fmt.Sprintf("tcp:%d", hostPort)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb reverse %s: %w\n%s", dev, err, out)
	}
	fmt.Fprintf(os.Stderr, "[e2e] adb reverse %s -> host tcp:%d\n", dev, hostPort)
	return nil
}

func teardownAdbReverse() {
	_ = exec.Command("adb", "reverse", "--remove", fmt.Sprintf("tcp:%d", mockDevicePort)).Run()
}

// --- TestMain ----------------------------------------------------------

var teardownOnce sync.Once

func TestMain(m *testing.M) {
	if err := resolvePaths(); err != nil {
		fmt.Fprintf(os.Stderr, "resolve paths: %v\n", err)
		os.Exit(2)
	}

	// 1) Build the forja binary so e2e exercises the actual CLI users would
	// run, not a fresh `go run` (which is slower and may diverge).
	if err := buildForja(); err != nil {
		fmt.Fprintf(os.Stderr, "build forja: %v\n", err)
		os.Exit(2)
	}

	// 2) Build the JVMTI agent bundle so attach works.
	if err := buildAgentBundle(); err != nil {
		fmt.Fprintf(os.Stderr, "build agent bundle: %v\n", err)
		os.Exit(2)
	}

	// 2.5) Pin forja to the *freshly built* bundle. Without this, forja's
	// ResolveBundleDir prefers an installed bundle ($HOME/.local/share/forja/
	// agent, etc.) over the repo build output — so on a machine where forja is
	// installed, e2e would silently push a STALE agent dex and test behavior
	// that no longer matches the source under test. FORJA_BUNDLE_DIR is the
	// highest-priority candidate, so this makes the suite hermetic.
	bundleDir := filepath.Join(repoRoot, "jvmti-agent", "build", "outputs", "agent")
	if err := os.Setenv("FORJA_BUNDLE_DIR", bundleDir); err != nil {
		fmt.Fprintf(os.Stderr, "set FORJA_BUNDLE_DIR: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "[e2e] FORJA_BUNDLE_DIR=%s\n", bundleDir)

	// 3) Boot the emulator (or borrow an existing device).
	if err := setupEmulator(); err != nil {
		fmt.Fprintf(os.Stderr, "setup emulator: %v\n", err)
		os.Exit(2)
	}

	// 3.5) Start the in-process mock HTTP server and bridge it to the device
	// with `adb reverse`, so the fixture's http://127.0.0.1:8080/ hits a
	// deterministic baseline (HTTP 200) instead of an external host. Must run
	// after the device is up (adb reverse needs it) and before fixture interaction.
	mockPort, err := startMockServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start mock server: %v\n", err)
		_ = teardownEmulator()
		os.Exit(2)
	}
	if err := setupAdbReverse(mockPort); err != nil {
		fmt.Fprintf(os.Stderr, "adb reverse: %v\n", err)
		stopMockServer()
		_ = teardownEmulator()
		os.Exit(2)
	}

	// Single teardown used everywhere (panic, install failure, normal exit).
	// teardownOnce guarantees it runs at most once.
	cleanup := func() {
		if os.Getenv("FORJA_E2E_KEEP") != "" {
			fmt.Fprintln(os.Stderr, "[e2e] FORJA_E2E_KEEP set, leaving emulator running")
			return
		}
		teardownAdbReverse()
		stopMockServer()
		_ = teardownEmulator()
	}

	// Always tear down even if a test panics.
	defer teardownOnce.Do(cleanup)

	// 4) Install both flavors of the fixture app.
	if err := installSampleApps(); err != nil {
		fmt.Fprintf(os.Stderr, "install fixture app: %v\n", err)
		teardownOnce.Do(cleanup)
		os.Exit(2)
	}

	code := m.Run()
	teardownOnce.Do(cleanup)
	os.Exit(code)
}

func buildForja() error {
	binDir := filepath.Join(repoRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, "forja"), ".")
	cmd.Dir = filepath.Join(repoRoot, "cli")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build cli: %w\n%s", err, out)
	}
	fmt.Fprintln(os.Stderr, "[e2e] built forja binary")
	return nil
}

func buildAgentBundle() error {
	cmd := exec.Command("./gradlew", ":jvmti-agent:bundleAgentDex")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gradlew bundleAgentDex: %w\n%s", err, out)
	}
	fmt.Fprintln(os.Stderr, "[e2e] built agent bundle")
	return nil
}

func setupEmulator() error {
	script := filepath.Join(scriptsDir, "setup_emulator.sh")
	cmd := exec.Command("bash", script)
	cmd.Stdout = os.Stderr // pass through so user sees progress
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup_emulator: %w", err)
	}
	return nil
}

func teardownEmulator() error {
	script := filepath.Join(scriptsDir, "teardown_emulator.sh")
	cmd := exec.Command("bash", script)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func installSampleApps() error {
	// The fixture app is an independent Gradle project with its own wrapper,
	// so we just run ./gradlew from inside e2e/fixtures/app/. The fixture
	// has two flavor dimensions (okhttp × env), so we install all four
	// combinations the e2e suite drives.
	cmd := exec.Command("./gradlew",
		"installOk4DevDebug", "installOk4StagingDebug",
		"installOk5DevDebug",
	)
	cmd.Dir = filepath.Join(repoRoot, "e2e", "fixtures", "app")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install fixture app: %w\n%s", err, out)
	}
	fmt.Fprintln(os.Stderr, "[e2e] installed fixture app (ok4 dev + ok4 staging + ok5 dev)")
	return nil
}

// --- app constants -------------------------------------------

const (
	AppDev     = "com.tkhskt.forja.sample"
	AppStaging = "com.tkhskt.forja.sample.staging"
	// AppOk5Dev is the OkHttp 5.x variant of the fixture app. It targets
	// `installOk5DevDebug` and is used to verify forja still rewrites
	// responses correctly against OkHttp 5.
	AppOk5Dev = "com.tkhskt.forja.sample.ok5"
)

// Ensure runtime is imported so the linter doesn't strip it when no test
// uses GOOS yet. Will be used by some host-conditional tests later.
var _ = runtime.GOOS
var _ io.Reader = (*bytes.Buffer)(nil)
