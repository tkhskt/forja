package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkhskt/forja/internal/config"
)

// writeBundleFile hand-authors a rule file under .forja/ (mkdir -p + write).
func writeBundleFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findEff(eff []config.EffectiveRule, name string) *config.EffectiveRule {
	for i := range eff {
		if eff[i].Name == name {
			return &eff[i]
		}
	}
	return nil
}

// TestAddDirWritesBundle: `rules add --dir rules/hoge` writes
// .forja/rules/hoge/rules.yml (not the root file) and the rule is discoverable.
func TestAddDirWritesBundle(t *testing.T) {
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "boom", Status: 500, Dir: "rules/hoge"}); err != nil {
		t.Fatalf("add --dir: %v", err)
	}
	want := filepath.Join(p.forjaDir(), "rules", "hoge", "rules.yml")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected bundle file at %s: %v", want, err)
	}
	if _, err := os.Stat(p.Project); !os.IsNotExist(err) {
		t.Errorf("root rules.yml must not be created by a --dir add; stat err=%v", err)
	}
	eff, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if findEff(eff, "boom") == nil {
		t.Errorf("bundle rule not discovered: %+v", eff)
	}
}

// TestAddDirRejectsEscape: --dir cannot point outside .forja/.
func TestAddDirRejectsEscape(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeProject, AddOptions{Name: "x", Status: 500, Dir: "../escape"})
	if err == nil || !strings.Contains(err.Error(), "inside .forja/") {
		t.Errorf("expected an escape error, got %v", err)
	}
}

// TestDiscoverMergesRootAndBundles: a root rule + a hand-authored bundle rule
// are both returned by LoadEffective.
func TestDiscoverMergesRootAndBundles(t *testing.T) {
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "root-rule", Status: 418}); err != nil {
		t.Fatal(err)
	}
	writeBundleFile(t, filepath.Join(p.forjaDir(), "rules", "pay", "rules.yml"),
		"rules:\n  - name: bundle-rule\n    response:\n      status: 503\n")
	eff, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if findEff(eff, "root-rule") == nil || findEff(eff, "bundle-rule") == nil {
		t.Errorf("expected both root-rule and bundle-rule; got %+v", eff)
	}
}

// TestBundleBodyFileResolvesRelativeToBundle: a bundle rule's bodyFile is read
// from the bundle's own directory (per-file BaseDir), so a bundle is
// self-contained and shareable by copying the directory.
func TestBundleBodyFileResolvesRelativeToBundle(t *testing.T) {
	p := pathsIn(t)
	bundle := filepath.Join(p.forjaDir(), "rules", "hoge")
	writeBundleFile(t, filepath.Join(bundle, "rules.yml"),
		"rules:\n  - name: bf\n    response:\n      status: 200\n      bodyFile: responses/hoge.json\n")
	writeBundleFile(t, filepath.Join(bundle, "responses", "hoge.json"), `{"by":"hoge"}`)
	eff, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	er := findEff(eff, "bf")
	if er == nil {
		t.Fatal("rule bf not found")
	}
	body, err := er.ResolveBody()
	if err != nil {
		t.Fatalf("resolve body: %v", err)
	}
	if body == nil || body.Object["by"] != "hoge" {
		t.Errorf("bodyFile not resolved from the bundle dir; got %+v", body)
	}
}

func hasStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestDupNamesAcrossBundlesAllowed: the same bare name in two different bundles
// is allowed; both load, are addressable by their qualified handles, and a bare
// reference is reported as ambiguous.
func TestDupNamesAcrossBundlesAllowed(t *testing.T) {
	p := pathsIn(t)
	writeBundleFile(t, filepath.Join(p.forjaDir(), "rules", "a", "rules.yml"),
		"rules:\n  - name: dup\n    response:\n      status: 501\n")
	writeBundleFile(t, filepath.Join(p.forjaDir(), "rules", "b", "rules.yml"),
		"rules:\n  - name: dup\n    response:\n      status: 502\n")

	app := "com.example.app"
	eff, err := LoadEffective(p, app)
	if err != nil {
		t.Fatalf("dup names across bundles should be allowed: %v", err)
	}
	var handles []string
	for _, e := range eff {
		handles = append(handles, e.Handle)
	}
	if !hasStr(handles, "rules/a/dup") || !hasStr(handles, "rules/b/dup") {
		t.Fatalf("want both qualified handles; got %v", handles)
	}

	// Enable by qualified handle → only that one is on.
	if err := Enable(p, app, []string{"rules/a/dup"}); err != nil {
		t.Fatalf("enable by handle: %v", err)
	}
	eff, _ = LoadEffective(p, app)
	for _, e := range eff {
		want := e.Handle == "rules/a/dup"
		if e.Enabled != want {
			t.Errorf("handle %s enabled=%v, want %v", e.Handle, e.Enabled, want)
		}
	}

	// A bare reference is ambiguous and must error with the candidates.
	if err := Enable(p, app, []string{"dup"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("bare ambiguous name should error; got %v", err)
	}
}

// TestSameBundleProjectLocalSameNameRejected: the same name in one bundle's
// rules.yml and rules.local.yml collides on handle and is rejected.
func TestSameBundleProjectLocalSameNameRejected(t *testing.T) {
	p := pathsIn(t)
	dir := filepath.Join(p.forjaDir(), "rules", "x")
	writeBundleFile(t, filepath.Join(dir, "rules.yml"),
		"rules:\n  - name: dup\n    response:\n      status: 1\n")
	writeBundleFile(t, filepath.Join(dir, "rules.local.yml"),
		"rules:\n  - name: dup\n    response:\n      status: 2\n")
	if _, err := LoadEffective(p, "com.example.app"); err == nil || !strings.Contains(err.Error(), "same handle") {
		t.Errorf("same-bundle project/local same name should collide; got %v", err)
	}
}

// TestUpdateAmbiguousBareNameErrors: a bare update with the same name in two
// bundles errors; the qualified form works.
func TestUpdateAmbiguousBareNameErrors(t *testing.T) {
	p := pathsIn(t)
	writeBundleFile(t, filepath.Join(p.forjaDir(), "rules", "a", "rules.yml"),
		"rules:\n  - name: dup\n    response:\n      status: 1\n")
	writeBundleFile(t, filepath.Join(p.forjaDir(), "rules", "b", "rules.yml"),
		"rules:\n  - name: dup\n    response:\n      status: 2\n")
	st := 9
	if err := Update(p, "dup", nil, UpdateOptions{Status: &st}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous bare update should error; got %v", err)
	}
	if err := Update(p, "rules/a/dup", nil, UpdateOptions{Status: &st}); err != nil {
		t.Fatalf("qualified update should succeed: %v", err)
	}
}

// TestRootRuleLegacyBareStatusStillEnables: a status.json entry written before
// bundles existed (a bare name) still enables its root rule, because a root
// rule's handle equals its name — so no status migration is needed.
func TestRootRuleLegacyBareStatusStillEnables(t *testing.T) {
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "legacy", Status: 418}); err != nil {
		t.Fatal(err)
	}
	writeBundleFile(t, p.Status, `{"com.example.app":{"enabled":["legacy"]}}`)
	eff, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	er := findEff(eff, "legacy")
	if er == nil || !er.Enabled {
		t.Errorf("legacy bare status entry should enable the root rule; got %+v", er)
	}
}

// TestUpdateRemoveLocateBundleFile: update/remove operate on the bundle file
// that declares the rule (located by name), not the root.
func TestUpdateRemoveLocateBundleFile(t *testing.T) {
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "b", Status: 418, Dir: "rules/x"}); err != nil {
		t.Fatal(err)
	}
	st := 503
	if err := Update(p, "b", nil, UpdateOptions{Status: &st}); err != nil {
		t.Fatalf("update: %v", err)
	}
	bundleFile := filepath.Join(p.forjaDir(), "rules", "x", "rules.yml")
	data, _ := os.ReadFile(bundleFile)
	if !strings.Contains(string(data), "503") {
		t.Errorf("update should patch the bundle file; got:\n%s", data)
	}
	if err := Remove(p, "b", nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	eff, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if findEff(eff, "b") != nil {
		t.Errorf("rule should be gone after remove; got %+v", eff)
	}
}
