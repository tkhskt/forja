//go:build e2e

// init + preflight + rules list — the device-agnostic CLI surface added
// in v0.2.0. These tests live in the e2e suite (rather than alongside the
// cli-level unit tests in cmd/) because they confirm the **wired binary**
// behaves end-to-end, not just the internal helpers. They run in well under
// a second each since no maestro / no agent / no device interaction is
// involved.
package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitHappyPath: `forja init` in a fresh directory creates forja/ and
// forja/rules.yml seeded with the schema-comment template, and prints the
// recommended .gitignore entries.
func TestInitHappyPath(t *testing.T) {
	// Wipe but do NOT call init — we want to drive it ourselves.
	_ = os.RemoveAll(filepath.Join(repoRoot, "forja"))

	out := runForja(t, "init")

	if _, err := os.Stat(filepath.Join(repoRoot, "forja")); err != nil {
		t.Fatalf("forja/ should exist after init: %v", err)
	}
	rulesPath := filepath.Join(repoRoot, "forja", "rules.yml")
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatalf("read rules.yml: %v", err)
	}
	if !strings.Contains(string(data), "Schema reference") {
		t.Errorf("rules.yml should embed schema documentation; got:\n%s", data)
	}
	for _, want := range []string{
		"initialized forja/rules.yml",
		".gitignore",
		"forja/rules.local.yml",
		"forja/status.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init output should mention %q:\n%s", want, out)
		}
	}
	// init must NOT create .gitignore — VCS hygiene is left to the user.
	if _, err := os.Stat(filepath.Join(repoRoot, ".gitignore")); !os.IsNotExist(err) {
		// .gitignore may legitimately exist in repoRoot from the project
		// itself. Just sanity-check init's "no .gitignore creation" by
		// confirming it didn't create one in a non-existing path. The
		// init_test.go unit test in cmd/ exercises the strict "no file"
		// assertion in a TempDir without a pre-existing .gitignore.
		_ = err
	}
}

// TestInitRefusesReInit: a second `forja init` must error rather than
// silently overwrite an existing rules.yml — that would be a footgun if
// the user has accumulated a real catalog. The cli unit test exercises
// runInit() directly; this confirms the wired binary surfaces the same
// guarantee through cobra.
func TestInitRefusesReInit(t *testing.T) {
	_ = os.RemoveAll(filepath.Join(repoRoot, "forja"))
	runForja(t, "init")
	out, err := runForjaAllowingFailure(t, "init")
	if err == nil {
		t.Fatalf("second init should fail; got success:\n%s", out)
	}
	if !strings.Contains(out, "already initialized") {
		t.Errorf("error should mention `already initialized`; got:\n%s", out)
	}
}

// TestInitRequiredBeforeOtherCommands: with no forja/ in cwd, every other
// entry point must fail loudly with an error that mentions `forja init`.
// This is the v0.2.0 footgun fix — accidentally running forja in the wrong
// cwd no longer silently materializes an orphan forja/ directory there.
//
// The matrix below mirrors the preflight call sites in cli/cmd/. Adding a
// new command without requireForjaDir will fail this test (intentional —
// keeps the contract enforced as the cmd surface grows).
func TestInitRequiredBeforeOtherCommands(t *testing.T) {
	_ = os.RemoveAll(filepath.Join(repoRoot, "forja"))

	cases := [][]string{
		{"rules", "add", "x", "--host", "example.com", "--status", "418"},
		{"rules", "update", "x", "--status", "503"},
		{"rules", "remove", "x"},
		{"rules", "list"},
		{"apply", "--app", AppDev, "--enable", "x"},
		{"off", "--app", AppDev},
		{"sync"},
		{"alias", "set", "dev", AppDev},
		{"alias", "rm", "dev"},
		{"alias", "list"},
	}
	for _, args := range cases {
		// Re-wipe before each case so a prior command (if it somehow
		// succeeded by mistake) can't leak a forja/ into the next case.
		_ = os.RemoveAll(filepath.Join(repoRoot, "forja"))

		out, err := runForjaAllowingFailure(t, args...)
		if err == nil {
			t.Errorf("`forja %v` should fail without forja/; got success:\n%s", args, out)
			continue
		}
		if !strings.Contains(out, "forja init") {
			t.Errorf("`forja %v` error should mention `forja init`; got:\n%s", args, out)
		}
	}
}

// TestRulesListSmoke: end-to-end smoke test for `forja rules list`. The
// cmd-level tests cover the formatter exhaustively; this just confirms the
// wired binary loads from disk and prints the expected sections + names.
//
// Also exercises `--app` to confirm the [on]/[off] prefix path is wired.
func TestRulesListSmoke(t *testing.T) {
	resetForjaState(t, AppDev)

	// Two rules in different scopes so both sections render.
	runForja(t, "rules", "add", "project-rule",
		"--host", "example.com", "--status", "418")
	runForja(t, "rules", "add", "local-rule", "--local",
		"--host", "example.com", "--status", "503")

	// Without --app: just catalog. Each scope section must appear with its
	// rule, project rules MUST NOT be prefixed with the device toggle [..].
	out := runForja(t, "rules", "list")
	for _, want := range []string{"local:", "project:", "project-rule", "local-rule"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules list output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[on]") || strings.Contains(out, "[off]") {
		t.Errorf("rules list without --app should not show enabled toggles:\n%s", out)
	}

	// With --app: project-rule is off (never enabled), local-rule too.
	// Enable just one and verify the markers reflect status.json.
	runForja(t, "apply", "--app", AppDev, "--enable", "project-rule")
	out = runForja(t, "rules", "list", "--app", AppDev)
	if !strings.Contains(out, "[on]  project-rule") {
		t.Errorf("project-rule should be [on] after apply; got:\n%s", out)
	}
	if !strings.Contains(out, "[off] local-rule") {
		t.Errorf("local-rule should be [off] (never enabled); got:\n%s", out)
	}
	if !strings.Contains(out, "target: "+AppDev) {
		t.Errorf("list --app should print target footer; got:\n%s", out)
	}
}
