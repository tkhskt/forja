package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDiscoverFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverRuleFilesFindsNestedAndSetsRel: discovery picks up rules.yml /
// rules.local.yml at the root and in nested bundle directories, sets Rel to the
// bundle path, and ignores files that aren't named like rule files.
func TestDiscoverRuleFilesFindsNestedAndSetsRel(t *testing.T) {
	dir := t.TempDir()
	writeDiscoverFile(t, filepath.Join(dir, "rules.yml"), "rules:\n  - name: root\n")
	writeDiscoverFile(t, filepath.Join(dir, "rules.local.yml"), "rules:\n  - name: rootlocal\n")
	writeDiscoverFile(t, filepath.Join(dir, "rules", "hoge", "rules.yml"), "rules:\n  - name: hoge\n")
	// Must be ignored: a JSON asset and a yml file not named like a rule file.
	writeDiscoverFile(t, filepath.Join(dir, "rules", "hoge", "responses", "x.json"), `{}`)
	writeDiscoverFile(t, filepath.Join(dir, "notes.yml"), "rules:\n  - name: ignored\n")

	srcs, err := DiscoverRuleFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]RuleSource{}
	for _, s := range srcs {
		for _, r := range s.File.Rules {
			byName[r.Name] = s
		}
	}
	if _, ok := byName["ignored"]; ok {
		t.Error("notes.yml must not be discovered (only rules.yml / rules.local.yml count)")
	}
	if s := byName["root"]; s.Rel != "" || s.Scope != ScopeProject {
		t.Errorf("root: rel=%q scope=%q, want rel=\"\" scope=project", s.Rel, s.Scope)
	}
	if byName["rootlocal"].Scope != ScopeLocal {
		t.Errorf("rootlocal scope=%q, want local", byName["rootlocal"].Scope)
	}
	if s := byName["hoge"]; s.Rel != "rules/hoge" {
		t.Errorf("hoge rel=%q, want rules/hoge", s.Rel)
	}

	// Local sources sort before project sources (first-match: local wins).
	if len(srcs) > 0 && srcs[0].Scope != ScopeLocal {
		t.Errorf("first source should be local-scope, got %q", srcs[0].Scope)
	}
}

func TestDiscoverRuleFilesMissingDir(t *testing.T) {
	srcs, err := DiscoverRuleFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(srcs) != 0 {
		t.Errorf("missing dir should yield no sources, got %d", len(srcs))
	}
}
