package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// chdir is a tiny test helper for the init flow, which is inherently
// cwd-sensitive (it creates ./forja/ relative to wherever it runs). t.Chdir
// would be cleaner but isn't available in all Go versions we support — wrap
// os.Chdir with a t.Cleanup to restore.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// captureStdout swaps os.Stdout for a pipe, runs fn, then restores stdout
// and returns whatever fn wrote. runInit prints directly via fmt.Println,
// so we exercise the user-visible output exactly as it would appear in a
// real terminal.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	fnErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), fnErr
}

// TestInitCreatesForjaDirAndRulesYml: the happy path. init produces a
// forja/ dir + a forja/rules.yml containing the schema-comment template.
// The template is intentionally comment-only — no `rules: []` placeholder
// (yaml.v3 parses an all-comments file into a zero-value RulesFile, and
// the first `rules add` materializes the `rules:` key naturally).
func TestInitCreatesForjaDirAndRulesYml(t *testing.T) {
	chdir(t, t.TempDir())
	if _, err := captureStdout(t, runInit); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if _, err := os.Stat("forja"); err != nil {
		t.Errorf("forja/ should exist: %v", err)
	}
	data, err := os.ReadFile("forja/rules.yml")
	if err != nil {
		t.Fatalf("read rules.yml: %v", err)
	}
	if !strings.Contains(string(data), "Schema reference") {
		t.Errorf("rules.yml should include schema documentation; got:\n%s", data)
	}
	// No `rules:` key should appear in the initial template — every non-
	// comment line would be a regression of the comment-only contract.
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		t.Errorf("initial template should be comment-only; found non-comment line: %q", line)
	}
}

// TestInitRefusesToOverwrite: a second `init` on an already-initialized
// directory must error out so a populated rule catalog isn't silently
// destroyed by an accidental re-init.
func TestInitRefusesToOverwrite(t *testing.T) {
	chdir(t, t.TempDir())
	if _, err := captureStdout(t, runInit); err != nil {
		t.Fatalf("first init: %v", err)
	}
	_, err := captureStdout(t, runInit)
	if err == nil {
		t.Fatal("expected error on re-init, got nil")
	}
	if !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("error should mention already initialized; got: %v", err)
	}
}

// TestInitPrintsGitignoreRecommendation: init does NOT edit .gitignore (VCS
// hygiene is the user's call), but it must surface the recommended entries
// so the user knows which files are safe to commit and which aren't. This
// is the substitute for the dropped --gitignore flag.
func TestInitPrintsGitignoreRecommendation(t *testing.T) {
	chdir(t, t.TempDir())
	out, err := captureStdout(t, runInit)
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if !strings.Contains(out, ".gitignore") {
		t.Errorf("init output should mention .gitignore: %s", out)
	}
	for _, want := range recommendedGitignoreEntries {
		if !strings.Contains(out, want) {
			t.Errorf("init output should list %q as a recommended .gitignore entry:\n%s", want, out)
		}
	}
	// And critically: NO .gitignore file was created.
	if _, err := os.Stat(".gitignore"); !os.IsNotExist(err) {
		t.Errorf("init must not create .gitignore; got stat err=%v", err)
	}
}

// TestRequireForjaDirRejectsMissingDir: the preflight catches the
// "you ran forja in the wrong cwd" footgun before it can spawn an orphan
// forja/ directory. The error message must point users at `forja init`.
func TestRequireForjaDirRejectsMissingDir(t *testing.T) {
	chdir(t, t.TempDir())
	err := requireForjaDir()
	if err == nil {
		t.Fatal("requireForjaDir should error when forja/ is absent")
	}
	if !strings.Contains(err.Error(), "forja init") {
		t.Errorf("error should mention `forja init`; got: %v", err)
	}
}

// TestRequireForjaDirPassesAfterInit: the canonical post-init state.
func TestRequireForjaDirPassesAfterInit(t *testing.T) {
	chdir(t, t.TempDir())
	if _, err := captureStdout(t, runInit); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := requireForjaDir(); err != nil {
		t.Errorf("requireForjaDir after init: %v", err)
	}
}

// TestRequireForjaDirRejectsForjaFile: extremely rare but worth covering —
// if `forja` exists in cwd but is a regular file (not a directory), bail
// rather than risk an os.Stat-based false positive elsewhere.
func TestRequireForjaDirRejectsForjaFile(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile("forja", []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := requireForjaDir()
	if err == nil {
		t.Fatal("requireForjaDir should error when `forja` is not a directory")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should explain the file-vs-dir mismatch; got: %v", err)
	}
}
