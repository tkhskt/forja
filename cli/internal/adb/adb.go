// Package adb is forja's thin wrapper around the adb CLI. It captures the
// shell-argument quirks (single-line forms for `run-as sh -c`, etc.) once so
// the rest of forja can speak in higher-level operations.
//
// The Executor indirection exists so commands can be tested without an actual
// device. Production code uses defaultExecutor, which is just exec.Command.
package adb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Executor abstracts subprocess execution.
type Executor interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
	RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) (stdout, stderr []byte, err error)
}

// defaultExecutor uses os/exec.
type defaultExecutor struct{}

func (defaultExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (defaultExecutor) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// ADB is a handle for talking to one device. Construct via New (default
// target), NewWithSerial (a specific device), or NewWithExecutor (tests).
//
// serial, when non-empty, is passed to every invocation as `-s <serial>` so
// the call targets that device even when several are connected. An empty
// serial means "let adb pick" — correct only when exactly one device is
// attached. Devices() is the exception: it always lists the global device
// table and ignores serial.
type ADB struct {
	exec   Executor
	serial string
}

// New returns an ADB backed by os/exec with no explicit device target.
func New() *ADB { return &ADB{exec: defaultExecutor{}} }

// NewWithSerial returns an ADB that targets the given device serial (as shown
// by `adb devices`). An empty serial behaves like New.
func NewWithSerial(serial string) *ADB { return &ADB{exec: defaultExecutor{}, serial: serial} }

// NewWithExecutor returns an ADB backed by the given Executor (use for tests).
func NewWithExecutor(e Executor) *ADB { return &ADB{exec: e} }

// NewWithExecutorSerial returns an ADB backed by the given Executor and
// targeting a specific serial (use for tests that assert `-s` threading).
func NewWithExecutorSerial(e Executor, serial string) *ADB { return &ADB{exec: e, serial: serial} }

// args prepends `-s <serial>` to the adb argument list when a device target
// is set, so every call site can build its args without repeating the serial
// plumbing. With no serial it returns the args unchanged.
func (a *ADB) args(rest ...string) []string {
	if a.serial == "" {
		return rest
	}
	return append([]string{"-s", a.serial}, rest...)
}

// appIdPattern restricts inputs to valid Android applicationId shapes so we
// can safely interpolate them into shell strings.
var appIdPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+)+$`)

// ValidateApp returns an error if name is not a recognizable Android
// applicationId. Callers that interpolate the value into a shell line MUST
// validate first.
func ValidateApp(name string) error {
	if !appIdPattern.MatchString(name) {
		return fmt.Errorf("invalid applicationId: %q", name)
	}
	return nil
}

// RunAsWrite writes `data` to /data/data/<app>/<remoteRel> on the device
// and chmods the result to 400. The whole sequence is forwarded as a single
// shell line so the inner quoting survives adb argv joining.
//
// chmod 400 is required because the device side may pass the file through
// ART integrity checks that reject anything with the write bit set. rm -f
// beforehand handles the case where a prior run left a read-only file.
func (a *ADB) RunAsWrite(ctx context.Context, app, remoteRel string, data []byte) error {
	if err := ValidateApp(app); err != nil {
		return err
	}
	inner := fmt.Sprintf(
		"mkdir -p $(dirname %s) && rm -f %s && cat > %s && chmod 400 %s",
		remoteRel, remoteRel, remoteRel, remoteRel,
	)
	line := fmt.Sprintf("run-as %s sh -c '%s'", app, inner)
	_, stderr, err := a.exec.RunWithStdin(ctx, data, "adb", a.args("shell", line)...)
	if err != nil {
		return fmt.Errorf("run-as write %s: %s: %w", remoteRel,
			strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// RunAsRead reads /data/data/<app>/<remoteRel> and returns its contents.
// A missing file returns (nil, nil) so callers can treat absence as a normal
// state without inspecting error strings.
func (a *ADB) RunAsRead(ctx context.Context, app, remoteRel string) ([]byte, error) {
	if err := ValidateApp(app); err != nil {
		return nil, err
	}
	line := fmt.Sprintf("run-as %s cat %s 2>/dev/null; true", app, remoteRel)
	stdout, _, err := a.exec.Run(ctx, "adb", a.args("shell", line)...)
	if err != nil {
		return nil, fmt.Errorf("run-as read %s: %w", remoteRel, err)
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, nil
	}
	return stdout, nil
}

// RunAsRemove deletes /data/data/<app>/<remoteRel>. Missing files do not error.
func (a *ADB) RunAsRemove(ctx context.Context, app, remoteRel string) error {
	if err := ValidateApp(app); err != nil {
		return err
	}
	line := fmt.Sprintf("run-as %s rm -f %s", app, remoteRel)
	_, stderr, err := a.exec.Run(ctx, "adb", a.args("shell", line)...)
	if err != nil {
		return fmt.Errorf("run-as rm: %s: %w",
			strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// Pidof returns the PID of the named process, or 0 if it's not running.
// On most Android shells `pidof` is available; we don't fall back further
// because every caller already handles 0 as "not running".
func (a *ADB) Pidof(ctx context.Context, app string) (int, error) {
	if err := ValidateApp(app); err != nil {
		return 0, err
	}
	stdout, _, err := a.exec.Run(ctx, "adb", a.args("shell", "pidof", app)...)
	if err != nil {
		// pidof exits non-zero when not found
		return 0, nil
	}
	fields := strings.Fields(strings.TrimSpace(string(stdout)))
	if len(fields) == 0 {
		return 0, nil
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("pidof: bad pid %q: %w", fields[0], err)
	}
	return pid, nil
}

// PrimaryABI returns ro.product.cpu.abi (e.g. "arm64-v8a"). Used to pick the
// agent .so to push.
func (a *ADB) PrimaryABI(ctx context.Context) (string, error) {
	stdout, _, err := a.exec.Run(ctx, "adb", a.args("shell", "getprop", "ro.product.cpu.abi")...)
	if err != nil {
		return "", fmt.Errorf("getprop: %w", err)
	}
	s := strings.TrimSpace(string(stdout))
	if s == "" {
		return "", errors.New("getprop returned empty ABI")
	}
	return s, nil
}

// listDebugScript collects the applicationIds of running processes and keeps
// the ones run-as can act on (= debuggable). It reads /proc/PID/cmdline
// (not ps's comm column, which truncates at 16 bytes on some kernels).
//
// The cmdline harvest is a single `cat ... | tr` rather than a per-process
// loop. The obvious form — `for d in /proc/[0-9]*; do tr -d '\0' <
// $d/cmdline; echo; done` — forks `tr` once PER PROCESS, and a busy device
// (the kind with lots of apps installed/running) easily has 300+ processes,
// so that loop alone cost ~1s and dominated `forja rules` startup. Reading
// every cmdline with one `cat` and splitting NULs with one `tr` drops that
// to ~0.05s. Each cmdline is NUL-terminated, so concatenating them and
// turning NULs into newlines yields one argv token per line; the strict
// FQDN grep then keeps only package-name-shaped tokens (stray argv entries
// like flags or paths don't match, and any that slip through are harmless
// since run-as rejects them).
//
// The run-as probe is then run concurrently, throttled to 16 in flight
// (`& ... [ i%16 ] && wait`) so a device with many candidates doesn't pay a
// sequential fork-per-package penalty. The consumer side is wrapped in a
// `{ ...; wait; }` group so the trailing wait reaps every backgrounded probe
// before the group's stdout closes; a closing `| sort -u` restores the
// deterministic ordering the parallel echoes would otherwise scramble.
//
// Trailing `; true` keeps the overall exit code 0 regardless of the last
// run-as result; the Go caller relies on stdout content, not exit status.
const listDebugScript = `cat /proc/[0-9]*/cmdline 2>/dev/null | tr '\0' '\n' | grep -E '^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+)+$' | sort -u | { i=0; while read p; do (run-as "$p" true 2>/dev/null && echo "$p") & i=$((i+1)); [ $((i%16)) -eq 0 ] && wait; done; wait; } | sort -u; true`

// ListDebuggableApps enumerates running app processes whose applicationId is
// debuggable (= run-as works against them).
func (a *ADB) ListDebuggableApps(ctx context.Context) ([]string, error) {
	stdout, _, _ := a.exec.Run(ctx, "adb", a.args("shell", listDebugScript)...)
	var apps []string
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := ValidateApp(line); err != nil {
			// Defensive: should not happen given the grep, but skip anything
			// that doesn't look like an applicationId rather than poison the list.
			continue
		}
		apps = append(apps, line)
	}
	return apps, nil
}

// Device is one entry from `adb devices -l`. Model is best-effort (absent for
// some emulators/older adb) and is display sugar only; Serial is the stable
// identifier used everywhere else. State is adb's connection state, e.g.
// "device" (usable), "offline", or "unauthorized".
type Device struct {
	Serial string
	Model  string
	State  string
}

// Devices lists the attached devices via `adb devices -l`. It always queries
// the global device table and ignores this handle's serial target, so it's
// safe to call on an ADB built with NewWithSerial. The returned slice keeps
// adb's ordering.
func (a *ADB) Devices(ctx context.Context) ([]Device, error) {
	stdout, stderr, err := a.exec.Run(ctx, "adb", "devices", "-l")
	if err != nil {
		return nil, fmt.Errorf("adb devices: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return parseDevices(string(stdout)), nil
}

// parseDevices turns `adb devices -l` output into Device entries. Split out
// from Devices so the (fiddly) line format has a pure, unit-testable core.
//
// The output looks like:
//
//	List of devices attached
//	emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 ...
//	RZ8N70ABCDE            device usb:... product:... model:Pixel_7 ...
//	00fabc                 unauthorized
//
// The first column is the serial, the second is the state, and the remaining
// `key:value` tokens carry extras — we lift `model:` when present.
func parseDevices(out string) []Device {
	var devices []Device
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		d := Device{Serial: fields[0], State: fields[1]}
		for _, tok := range fields[2:] {
			if m, ok := strings.CutPrefix(tok, "model:"); ok {
				d.Model = m
			}
		}
		devices = append(devices, d)
	}
	return devices
}

// foregroundActivityRE matches "<app>/<.Activity>" inside dumpsys lines.
// The pattern is intentionally loose because dumpsys formatting varies by
// Android version (mResumedActivity / topResumedActivity / etc.).
var foregroundActivityRE = regexp.MustCompile(`\s([a-zA-Z][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_]+)+)/`)

// ForegroundApp returns the currently-foreground applicationId, or "" if
// nothing matches (e.g. lock screen, or dumpsys text changed shape).
// Used to highlight a sensible default in the app picker.
func (a *ADB) ForegroundApp(ctx context.Context) (string, error) {
	stdout, _, err := a.exec.Run(ctx, "adb", a.args("shell",
		"dumpsys activity activities | grep -E 'ResumedActivity' | tail -1")...)
	if err != nil {
		return "", nil // foreground hint is best-effort
	}
	m := foregroundActivityRE.FindStringSubmatch(string(stdout))
	if len(m) < 2 {
		return "", nil
	}
	return m[1], nil
}

// AttachAgent invokes the JVMTI attach mechanism.
//
// soPath and dexPath are absolute device paths
// (typically /data/data/<app>/files/...). The single `<so>=<dex>` argument is
// the JVMTI agent options string our agent.cpp parses for the bundle DEX path.
func (a *ADB) AttachAgent(ctx context.Context, app, soPath, dexPath string) error {
	if err := ValidateApp(app); err != nil {
		return err
	}
	arg := fmt.Sprintf("%s=%s", soPath, dexPath)
	stdout, stderr, err := a.exec.Run(ctx, "adb", a.args("shell",
		"cmd", "activity", "attach-agent", app, arg)...)
	if err != nil {
		return fmt.Errorf("attach-agent: %s: %w",
			strings.TrimSpace(string(stderr)), err)
	}
	combined := strings.TrimSpace(string(stdout) + string(stderr))
	if strings.Contains(combined, "Exception") ||
		strings.Contains(strings.ToLower(combined), "failed") {
		return fmt.Errorf("attach-agent: %s", combined)
	}
	return nil
}
