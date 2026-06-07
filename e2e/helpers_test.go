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
	"encoding/json"
	"fmt"
	"io"
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
	_, _ = adbShellAllowingFailure(t, fmt.Sprintf("am start -n %s/%s", pkg, MainActivityFQN))
	// Wait up to 5s for pidof to come back non-zero.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pidof(t, pkg) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("app %s failed to start within 5s", pkg)
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
	cmd := exec.Command(maestroPath, "test", path)
	cmd.Dir = filepath.Dir(flowsDir) // e2e/
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("maestro test %s failed: %v\n--- output ---\n%s", name, err, buf.String())
	}
	return buf.String()
}

// --- forja state helpers -----------------------------------------------

// resetForjaState wipes ./forja/ in repo root and the attach cache for the
// given apps. Call this at the start of each test for hermetic state.
func resetForjaState(t *testing.T, pkgs ...string) {
	t.Helper()
	_ = os.RemoveAll(filepath.Join(repoRoot, "forja"))
	home, _ := os.UserHomeDir()
	cacheDir := filepath.Join(home, ".cache", "forja")
	for _, pkg := range pkgs {
		_ = os.Remove(filepath.Join(cacheDir, pkg+".json"))
	}
}

// StatusJSON mirrors the on-disk shape of forja/status.json: a flat top-level
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

// readStatusJSON returns the parsed forja/status.json. Returns nil (empty
// map) if the file doesn't exist. Top-level keys starting with `$` are
// silently skipped — forja embeds a "$comment" metadata key warning users
// that the file is CLI-managed, and that string value can't decode as
// {Enabled []string}.
func readStatusJSON(t *testing.T) StatusJSON {
	t.Helper()
	path := filepath.Join(repoRoot, "forja", "status.json")
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
	path := filepath.Join(repoRoot, "forja", name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
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

	// 3) Boot the emulator (or borrow an existing device).
	if err := setupEmulator(); err != nil {
		fmt.Fprintf(os.Stderr, "setup emulator: %v\n", err)
		os.Exit(2)
	}

	// Always tear down even if a test panics. The teardown script no-ops on
	// borrowed devices.
	defer teardownOnce.Do(func() {
		if os.Getenv("FORJA_E2E_KEEP") != "" {
			fmt.Fprintln(os.Stderr, "[e2e] FORJA_E2E_KEEP set, leaving emulator running")
			return
		}
		_ = teardownEmulator()
	})

	// 4) Install both flavors of the fixture app.
	if err := installSampleApps(); err != nil {
		fmt.Fprintf(os.Stderr, "install fixture app: %v\n", err)
		teardownOnce.Do(func() { _ = teardownEmulator() })
		os.Exit(2)
	}

	code := m.Run()
	teardownOnce.Do(func() {
		if os.Getenv("FORJA_E2E_KEEP") != "" {
			fmt.Fprintln(os.Stderr, "[e2e] FORJA_E2E_KEEP set, leaving emulator running")
			return
		}
		_ = teardownEmulator()
	})
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
