//go:build e2e

package e2e_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mkForjaResponsesDir copies one or more fixtures into ./forja/responses/
// (relative to repoRoot) so a `bodyFile: responses/X` reference resolves
// against the yml file's directory.
func mkForjaResponsesDir(t *testing.T, fixtures ...string) {
	t.Helper()
	dir := filepath.Join(repoRoot, "forja", "responses")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range fixtures {
		src := filepath.Join(fixturesDir, name)
		dst := filepath.Join(dir, name)
		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copy fixture %s: %v", name, err)
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// stripYmlRuleByName removes one rule entry from a forja-generated yml string.
// The writer emits rules as `    - name: X\n      key: value\n...` (4-space
// list marker indent, 6-space continuation indent). Scan line-by-line: once
// we see `- name: <target>`, skip subsequent indented continuation lines
// until we hit either another list marker (`- ...`) or a de-indented line.
func stripYmlRuleByName(yml, name string) string {
	target := "- name: " + name
	lines := strings.Split(yml, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		isListItem := strings.HasPrefix(trimmed, "- ")
		isIndented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')

		if skipping {
			if isListItem || !isIndented {
				skipping = false
				// Fall through to handle this line normally.
			} else {
				continue // Continuation of the entry being stripped.
			}
		}

		if isListItem && trimmed == target {
			skipping = true
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// runInlineMaestro writes the flow yaml to a temp file under /tmp and runs
// maestro test on it. Convenient when a one-off assertion doesn't merit a
// permanent flow file.
func runInlineMaestro(t *testing.T, flow string) string {
	t.Helper()
	if maestroPath == "" {
		t.Skip("maestro not found")
	}
	f, err := os.CreateTemp("", "forja-e2e-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(flow); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := runMaestroWithRetry(t, f.Name())
	if err != nil {
		t.Fatalf("inline maestro flow failed: %v\n--- flow ---\n%s--- output ---\n%s",
			err, flow, out)
	}
	return out
}

// runMaestroWithRetry runs `maestro test <path>` and retries ONLY when the
// failure is Maestro's own Android-driver startup timeout — a transient
// infrastructure hiccup that has nothing to do with the flow's assertions.
// It surfaces as "Maestro Android driver did not start up in time" when the
// driver's dadb port handshake loses a race, and tends to appear after many
// back-to-back sessions on one long-lived emulator. Retrying re-establishes
// a fresh driver session; a genuine assertion failure (e.g. expected 418 but
// saw 200) does NOT carry this signature and is returned immediately so real
// regressions still fail fast.
func runMaestroWithRetry(t *testing.T, flowPath string) (string, error) {
	t.Helper()
	const maxAttempts = 3
	var lastOut string
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command(maestroPath, "test", flowPath)
		cmd.Dir = filepath.Dir(flowsDir) // e2e/
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		lastOut, lastErr = buf.String(), err
		if err == nil {
			return lastOut, nil
		}
		if !isMaestroDriverStartupFailure(lastOut) || attempt == maxAttempts {
			return lastOut, err
		}
		t.Logf("maestro driver startup timed out (attempt %d/%d) — retrying", attempt, maxAttempts)
	}
	return lastOut, lastErr
}

// isMaestroDriverStartupFailure detects the driver-startup-timeout signature
// in Maestro's combined stdout/stderr. Matching on the message (rather than
// the exit code) keeps the retry narrowly scoped to this one transient class
// — every other non-zero exit (assertion failed, element not found, app not
// installed) falls through to an immediate failure.
func isMaestroDriverStartupFailure(output string) bool {
	return strings.Contains(output, "Android driver did not start up in time") ||
		strings.Contains(output, "AndroidDriverTimeoutException")
}
