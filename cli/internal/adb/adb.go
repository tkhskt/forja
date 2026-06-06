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

// ADB is a stateless handle. Construct via New or NewWithExecutor.
type ADB struct {
	exec Executor
}

// New returns an ADB backed by os/exec.
func New() *ADB { return &ADB{exec: defaultExecutor{}} }

// NewWithExecutor returns an ADB backed by the given Executor (use for tests).
func NewWithExecutor(e Executor) *ADB { return &ADB{exec: e} }

// packagePattern restricts inputs to valid Android package shapes so we can
// safely interpolate them into shell strings.
var packagePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+)+$`)

// ValidatePackage returns an error if name is not a recognizable Android
// package identifier. Callers that interpolate the package into a shell line
// MUST validate first.
func ValidatePackage(name string) error {
	if !packagePattern.MatchString(name) {
		return fmt.Errorf("invalid package name: %q", name)
	}
	return nil
}

// RunAsWrite writes `data` to /data/data/<pkg>/<remoteRel> on the device
// and chmods the result to 400. The whole sequence is forwarded as a single
// shell line so the inner quoting survives adb argv joining.
//
// chmod 400 is required because the device side may pass the file through
// ART integrity checks that reject anything with the write bit set. rm -f
// beforehand handles the case where a prior run left a read-only file.
func (a *ADB) RunAsWrite(ctx context.Context, pkg, remoteRel string, data []byte) error {
	if err := ValidatePackage(pkg); err != nil {
		return err
	}
	inner := fmt.Sprintf(
		"mkdir -p $(dirname %s) && rm -f %s && cat > %s && chmod 400 %s",
		remoteRel, remoteRel, remoteRel, remoteRel,
	)
	line := fmt.Sprintf("run-as %s sh -c '%s'", pkg, inner)
	_, stderr, err := a.exec.RunWithStdin(ctx, data, "adb", "shell", line)
	if err != nil {
		return fmt.Errorf("run-as write %s: %s: %w", remoteRel,
			strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// RunAsRead reads /data/data/<pkg>/<remoteRel> and returns its contents.
// A missing file returns (nil, nil) so callers can treat absence as a normal
// state without inspecting error strings.
func (a *ADB) RunAsRead(ctx context.Context, pkg, remoteRel string) ([]byte, error) {
	if err := ValidatePackage(pkg); err != nil {
		return nil, err
	}
	line := fmt.Sprintf("run-as %s cat %s 2>/dev/null; true", pkg, remoteRel)
	stdout, _, err := a.exec.Run(ctx, "adb", "shell", line)
	if err != nil {
		return nil, fmt.Errorf("run-as read %s: %w", remoteRel, err)
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, nil
	}
	return stdout, nil
}

// RunAsRemove deletes /data/data/<pkg>/<remoteRel>. Missing files do not error.
func (a *ADB) RunAsRemove(ctx context.Context, pkg, remoteRel string) error {
	if err := ValidatePackage(pkg); err != nil {
		return err
	}
	line := fmt.Sprintf("run-as %s rm -f %s", pkg, remoteRel)
	_, stderr, err := a.exec.Run(ctx, "adb", "shell", line)
	if err != nil {
		return fmt.Errorf("run-as rm: %s: %w",
			strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// Pidof returns the PID of the named process, or 0 if it's not running.
// On most Android shells `pidof` is available; we don't fall back further
// because every caller already handles 0 as "not running".
func (a *ADB) Pidof(ctx context.Context, pkg string) (int, error) {
	if err := ValidatePackage(pkg); err != nil {
		return 0, err
	}
	stdout, _, err := a.exec.Run(ctx, "adb", "shell", "pidof", pkg)
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
	stdout, _, err := a.exec.Run(ctx, "adb", "shell", "getprop", "ro.product.cpu.abi")
	if err != nil {
		return "", fmt.Errorf("getprop: %w", err)
	}
	s := strings.TrimSpace(string(stdout))
	if s == "" {
		return "", errors.New("getprop returned empty ABI")
	}
	return s, nil
}

// listDebugScript scans /proc cmdline for FQDN-shaped names then checks
// run-as for each. Reads /proc/PID/cmdline (not ps's comm column) because the
// latter truncates at 16 bytes on some kernels.
//
// Trailing `; true` is critical: the while-read pipeline's last iteration
// may end on a non-debuggable package, which would make run-as exit 1 and
// poison the pipeline's exit code. We rely on stdout content, not exit code.
const listDebugScript = `for d in /proc/[0-9]*; do tr -d '\0' < $d/cmdline 2>/dev/null; echo; done | grep -E '^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+)+$' | sort -u | while read p; do run-as "$p" true 2>/dev/null && echo "$p"; done; true`

// ListDebuggablePackages enumerates running app processes whose package is
// debuggable (= run-as works against them).
func (a *ADB) ListDebuggablePackages(ctx context.Context) ([]string, error) {
	stdout, _, _ := a.exec.Run(ctx, "adb", "shell", listDebugScript)
	var pkgs []string
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if err := ValidatePackage(line); err != nil {
			// Defensive: should not happen given the grep, but skip anything
			// that doesn't look like a package rather than poison the list.
			continue
		}
		pkgs = append(pkgs, line)
	}
	return pkgs, nil
}

// foregroundActivityRE matches "<pkg>/<.Activity>" inside dumpsys lines.
// The pattern is intentionally loose because dumpsys formatting varies by
// Android version (mResumedActivity / topResumedActivity / etc.).
var foregroundActivityRE = regexp.MustCompile(`\s([a-zA-Z][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_]+)+)/`)

// ForegroundPackage returns the currently-foreground package, or "" if
// nothing matches (e.g. lock screen, or dumpsys text changed shape).
// Used to highlight a sensible default in the package picker.
func (a *ADB) ForegroundPackage(ctx context.Context) (string, error) {
	stdout, _, err := a.exec.Run(ctx, "adb", "shell",
		"dumpsys activity activities | grep -E 'ResumedActivity' | tail -1")
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
// (typically /data/data/<pkg>/files/...). The single `<so>=<dex>` argument is
// the JVMTI agent options string our agent.cpp parses for the bundle DEX path.
func (a *ADB) AttachAgent(ctx context.Context, pkg, soPath, dexPath string) error {
	if err := ValidatePackage(pkg); err != nil {
		return err
	}
	arg := fmt.Sprintf("%s=%s", soPath, dexPath)
	stdout, stderr, err := a.exec.Run(ctx, "adb", "shell",
		"cmd", "activity", "attach-agent", pkg, arg)
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
