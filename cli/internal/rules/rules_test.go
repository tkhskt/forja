package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tkhskt/forja/internal/config"
)

// pathsIn returns a Paths struct rooted under a fresh tempdir, mirroring the
// production .forja/ layout. The .forja/ subdirectory is created up front to
// match the post-`forja init` state — production Save/SaveStatus/SaveAliases
// no longer mkdir on demand (the cmd-layer requireForjaDir preflight is the
// authoritative gate for directory existence).
func pathsIn(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	forjaDir := filepath.Join(dir, config.DefaultDir)
	if err := os.MkdirAll(forjaDir, 0o755); err != nil {
		t.Fatalf("setup forja dir: %v", err)
	}
	return Paths{
		Project:      filepath.Join(forjaDir, "rules.yml"),
		Local:        filepath.Join(forjaDir, "rules.local.yml"),
		Status:       filepath.Join(forjaDir, "status.json"),
		Aliases:      filepath.Join(forjaDir, "aliases.yml"),
		AliasesLocal: filepath.Join(forjaDir, "aliases.local.yml"),
	}
}

func TestAddYmlOnlyByDefault(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{Name: "mock", Host: "example.com", Status: 500})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	rf, _ := config.Load(p.Local)
	if rf == nil || len(rf.Rules) != 1 || rf.Rules[0].Name != "mock" {
		t.Errorf("user file unexpected: %+v", rf)
	}
	// Status.json should NOT be created for a plain add — the rule is off
	// on every app until explicitly enabled.
	st, _ := config.LoadStatus(p.Status)
	if len(st) != 0 {
		t.Errorf("status.json should be empty after plain Add, got: %+v", st)
	}
}

func TestAddProjectScope(t *testing.T) {
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "team"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rf, _ := config.Load(p.Project)
	if rf == nil || rf.FindRule("team") == nil {
		t.Errorf("project file missing team rule: %+v", rf)
	}
	if uf, _ := config.Load(p.Local); uf != nil {
		t.Errorf("user file should not be created when adding to project: %+v", uf)
	}
}

func TestAddDuplicateInSameScope(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "x"})
	err := Add(p, ScopeLocal, AddOptions{Name: "x"})
	if err == nil {
		t.Error("expected duplicate-add to fail in same scope")
	}
}

// TestAddRejectsSameNameAcrossScopes: rule names are unique across both
// scopes. An Add that would introduce a cross-scope duplicate must be
// rejected with an actionable error (= naming the scope that already
// owns the name).
func TestAddRejectsSameNameAcrossScopes(t *testing.T) {
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "x", Status: 500}); err != nil {
		t.Fatal(err)
	}
	err := Add(p, ScopeLocal, AddOptions{Name: "x", Status: 999})
	if err == nil {
		t.Fatal("cross-scope same-name add should be rejected")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should say the name already exists; got: %v", err)
	}
}

func TestRemoveFindsAcrossScopes(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeProject, AddOptions{Name: "team-rule"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "user-rule"})

	if err := Remove(p, "user-rule", nil); err != nil {
		t.Fatalf("Remove user-rule: %v", err)
	}
	uf, _ := config.Load(p.Local)
	if uf.FindRule("user-rule") != nil {
		t.Errorf("user-rule should be gone from user file")
	}
	pf, _ := config.Load(p.Project)
	if pf.FindRule("team-rule") == nil {
		t.Errorf("team-rule should still be in project file")
	}

	if err := Remove(p, "team-rule", nil); err != nil {
		t.Fatalf("Remove team-rule: %v", err)
	}
	pf, _ = config.Load(p.Project)
	if pf.FindRule("team-rule") != nil {
		t.Errorf("team-rule should be gone from project file")
	}
}

func TestRemoveDropsAcrossAllPkgStatusEntries(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "tmp"})
	// Enable on two pkgs.
	if err := Enable(p, "com.a", []string{"tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := Enable(p, "com.b", []string{"tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p, "tmp", nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.a", "tmp") || st.IsEnabled("com.b", "tmp") {
		t.Errorf("status entries for removed rule should be cleaned up: %+v", st)
	}
}

func TestUpdatePatchAcrossScopes(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "x", Host: "a.com", Status: 200})
	newStatus := 503
	if err := Update(p, "x", nil, UpdateOptions{Status: &newStatus}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	uf, _ := config.Load(p.Local)
	r := uf.FindRule("x")
	if r.Response.Status != 503 {
		t.Errorf("status not updated: %d", r.Response.Status)
	}
	if r.Match.Host != "a.com" {
		t.Errorf("host should be preserved, got %q", r.Match.Host)
	}
}

func TestUpdateNotFound(t *testing.T) {
	p := pathsIn(t)
	newStatus := 503
	err := Update(p, "missing", nil, UpdateOptions{Status: &newStatus})
	if err == nil {
		t.Error("expected error for missing rule")
	}
}

func TestEnableAddsToPkgEnabledList(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "foo"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "bar"})
	if err := Enable(p, "com.example.app", []string{"foo", "bar"}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if !st.IsEnabled("com.example.app", "foo") || !st.IsEnabled("com.example.app", "bar") {
		t.Errorf("Enable did not record entries: %+v", st)
	}
}

func TestEnableRejectsUnknownRuleNames(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "real"})
	err := Enable(p, "com.example.app", []string{"real", "typo"})
	if err == nil {
		t.Error("expected error for unknown rule 'typo'")
	}
	// Real should not have been added either (early reject).
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.example.app", "real") {
		t.Errorf("Enable should be atomic — 'real' should not be set when 'typo' is bogus")
	}
}

func TestDisableRemovesFromPkgEnabledList(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "foo"})
	_ = Enable(p, "com.example.app", []string{"foo"})
	if err := Disable(p, "com.example.app", []string{"foo"}); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.example.app", "foo") {
		t.Errorf("foo should be disabled: %+v", st)
	}
}

func TestDisableIgnoresUnknownRuleNames(t *testing.T) {
	p := pathsIn(t)
	// No yml entries at all — Disable should silently no-op for typo scrubbing.
	if err := Disable(p, "com.example.app", []string{"never-existed"}); err != nil {
		t.Errorf("Disable of unknown name should not error: %v", err)
	}
}

func TestClearAppEmptiesEnabledList(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "foo"})
	_ = Enable(p, "com.example.app", []string{"foo"})
	if err := ClearApp(p, "com.example.app"); err != nil {
		t.Fatalf("ClearApp: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.example.app", "foo") {
		t.Errorf("foo should be cleared: %+v", st)
	}
}

func TestSetEnabledForAppOverwrites(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "a"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "b"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "c"})
	_ = Enable(p, "com.example.app", []string{"a", "b"})
	// Overwrite with a new set — b should go away, c should appear.
	if err := SetEnabledForApp(p, "com.example.app", []string{"a", "c"}); err != nil {
		t.Fatalf("SetEnabledForApp: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if !st.IsEnabled("com.example.app", "a") || !st.IsEnabled("com.example.app", "c") {
		t.Errorf("a and c should be enabled: %+v", st)
	}
	if st.IsEnabled("com.example.app", "b") {
		t.Errorf("b should have been removed: %+v", st)
	}
}

func TestLoadEffectiveMergesAndOverridesPerApp(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeProject, AddOptions{Name: "team-a", Status: 200})
	_ = Add(p, ScopeProject, AddOptions{Name: "team-b", Status: 200})
	_ = Add(p, ScopeLocal, AddOptions{Name: "personal-fast", Status: 418})
	_ = Add(p, ScopeLocal, AddOptions{Name: "personal", Status: 418})
	// Enable on com.example.app: personal-fast + personal — leave team rules off.
	_ = Enable(p, "com.example.app", []string{"personal-fast", "personal"})

	rules, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	// Expected order: local rules first (personal-fast, personal), then
	// project rules in declaration order (team-a, team-b). Cross-scope
	// name duplicates are forbidden by loadBothScopes, so the merged list
	// is a plain concat with no shadow filtering.
	if len(rules) != 4 {
		t.Fatalf("want 4 effective rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].Name != "personal-fast" || rules[0].Scope != config.ScopeLocal || !rules[0].Enabled {
		t.Errorf("rules[0]: %+v", rules[0])
	}
	if rules[1].Name != "personal" || rules[1].Scope != config.ScopeLocal || !rules[1].Enabled {
		t.Errorf("rules[1]: %+v", rules[1])
	}
	if rules[2].Name != "team-a" || rules[2].Scope != config.ScopeProject || rules[2].Enabled {
		t.Errorf("rules[2]: %+v", rules[2])
	}
	if rules[3].Name != "team-b" || rules[3].Scope != config.ScopeProject || rules[3].Enabled {
		t.Errorf("rules[3]: %+v", rules[3])
	}
}

// TestAddRejectsCrossScopeDuplicate: adding a rule whose name already
// exists in the other scope must error out before touching disk. The
// rejection diagnostic must name the scope where the existing rule lives
// so the user can find it.
func TestAddRejectsCrossScopeDuplicate(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeProject, AddOptions{Name: "shared", Status: 418})

	err := Add(p, ScopeLocal, AddOptions{Name: "shared", Status: 503})
	if err == nil {
		t.Fatal("expected Add(local, shared) to be rejected after a project rule of the same name exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should say the name already exists; got: %v", err)
	}
	// And the inverse — pre-existing local with same name should block a
	// new project add.
	p2 := pathsIn(t)
	_ = Add(p2, ScopeLocal, AddOptions{Name: "shared", Status: 418})
	err = Add(p2, ScopeProject, AddOptions{Name: "shared", Status: 503})
	if err == nil {
		t.Fatal("expected Add(project, shared) to be rejected after a local rule of the same name exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should say the name already exists; got: %v", err)
	}
}

// TestLoadEffectiveRejectsCrossScopeDuplicates: hand-edited yml files
// that introduce a cross-scope duplicate (= bypassing Add) must surface
// as a clear load-time error. status.json's name-keyed enabled list
// becomes ambiguous when two rules share a name, and the push path can't
// safely pick one.
func TestLoadEffectiveRejectsCrossScopeDuplicates(t *testing.T) {
	p := pathsIn(t)
	const shared = "shared"
	if err := os.WriteFile(p.Project, []byte("rules:\n  - name: "+shared+"\n    response:\n      status: 418\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Local, []byte("rules:\n  - name: "+shared+"\n    response:\n      status: 503\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadEffective(p, "com.example.app")
	if err == nil {
		t.Fatal("LoadEffective should reject cross-scope duplicates")
	}
	if !strings.Contains(err.Error(), "same handle") {
		t.Errorf("error should explain the handle collision; got: %v", err)
	}
}

func TestLoadEffectiveDifferentPkgsHaveDifferentEnabledState(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "shared"})
	_ = Enable(p, "com.a", []string{"shared"})
	// com.b has not enabled shared.

	a, _ := LoadEffective(p, "com.a")
	if len(a) != 1 || !a[0].Enabled {
		t.Errorf("com.a should have shared enabled: %+v", a)
	}
	b, _ := LoadEffective(p, "com.b")
	if len(b) != 1 || b[0].Enabled {
		t.Errorf("com.b should have shared disabled (= absent): %+v", b)
	}
}

func TestEffectiveOrderGivesUserPrecedenceOnMatchCollision(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeProject, AddOptions{Name: "team-override", Host: "example.com", Status: 418})
	_ = Add(p, ScopeLocal, AddOptions{Name: "user-override", Host: "example.com", Status: 999})

	rules, err := LoadEffective(p, "com.example.app")
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}
	if rules[0].Name != "user-override" {
		t.Errorf("user-override should be at index 0, got order: %v / %v",
			rules[0].Name, rules[1].Name)
	}
}

func TestAddWithBodyFile(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{
		Name: "x", Host: "example.com", Status: 418, BodyFile: "responses/x.json",
	})
	if err != nil {
		t.Fatalf("Add with bodyFile: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("x")
	if r.Response.BodyFile != "responses/x.json" {
		t.Errorf("bodyFile not stored: %q", r.Response.BodyFile)
	}
}

func TestAddRejectsBothBodyAndBodyFile(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{
		Name: "x", Body: &config.BodyValue{String: "inline"}, BodyFile: "ext.json",
	})
	if err == nil {
		t.Error("expected error when both body and bodyFile are set")
	}
}

// TestUpdateSurfacesLoadErrorOnDuplicateYml: a hand-edited yml with a
// duplicate rule name fails Load with a specific diagnostic. The Update
// path must propagate that diagnostic verbatim — an earlier implementation
// silently swallowed Load errors inside findRule, surfacing a misleading
// "rule not found in either scope" instead of the real cause. This test
// pins the propagation contract so a future findRule refactor can't
// regress it.
func TestUpdateSurfacesLoadErrorOnDuplicateYml(t *testing.T) {
	p := pathsIn(t)
	corruptYml := []byte(`
rules:
  - name: foo
    response: { status: 418 }
  - name: foo
    response: { status: 500 }
`)
	if err := os.WriteFile(p.Project, corruptYml, 0o644); err != nil {
		t.Fatal(err)
	}
	err := Update(p, "anything", nil, UpdateOptions{Host: ptrString("x")})
	if err == nil {
		t.Fatal("Update on a yml with duplicate names should fail")
	}
	if !strings.Contains(err.Error(), "duplicate rule name") {
		t.Errorf("error should propagate Load's duplicate-name diagnostic; got: %v", err)
	}
}

// TestRemoveSurfacesLoadErrorOnDuplicateYml: same contract as the Update
// case above but on the Remove path (which also threads through findRule).
func TestRemoveSurfacesLoadErrorOnDuplicateYml(t *testing.T) {
	p := pathsIn(t)
	corruptYml := []byte(`
rules:
  - name: foo
    response: { status: 418 }
  - name: foo
    response: { status: 500 }
`)
	if err := os.WriteFile(p.Project, corruptYml, 0o644); err != nil {
		t.Fatal(err)
	}
	err := Remove(p, "anything", nil)
	if err == nil {
		t.Fatal("Remove on a yml with duplicate names should fail")
	}
	if !strings.Contains(err.Error(), "duplicate rule name") {
		t.Errorf("error should propagate Load's duplicate-name diagnostic; got: %v", err)
	}
}

// ptrString is a tiny helper for building UpdateOptions field pointers in
// tests where the actual value doesn't matter (we only care that the load
// step fails before any field is consulted).
func ptrString(s string) *string { return &s }

func TestUpdateSwapsBodyToBodyFile(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{
		Name: "x", Body: &config.BodyValue{Object: map[string]any{"original": true}},
	})
	bf := "external.json"
	if err := Update(p, "x", nil, UpdateOptions{BodyFile: &bf}); err != nil {
		t.Fatalf("Update bodyFile: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("x")
	if r.Response.BodyFile != "external.json" {
		t.Errorf("bodyFile not set: %q", r.Response.BodyFile)
	}
	if r.Response.Body != nil {
		t.Errorf("inline body should be cleared: %+v", r.Response.Body)
	}
}

func TestUpdateSwapsBodyFileToBody(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "x", BodyFile: "external.json"})
	newBody := &config.BodyValue{String: "inline now"}
	if err := Update(p, "x", nil, UpdateOptions{Body: newBody}); err != nil {
		t.Fatalf("Update body: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("x")
	if r.Response.BodyFile != "" {
		t.Errorf("bodyFile should be cleared: %q", r.Response.BodyFile)
	}
	if r.Response.Body == nil || r.Response.Body.String != "inline now" {
		t.Errorf("body not updated: %+v", r.Response.Body)
	}
}

// TestAddPersistsHeaders: --header values reach disk so subsequent loads
// observe the same map, and the device JSON view emits a headers key.
func TestAddPersistsHeaders(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{
		Name:   "html",
		Host:   "example.com",
		Status: 200,
		Body:   &config.BodyValue{String: "<h1>hi</h1>"},
		Headers: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
			"X-Forja":      "1",
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("html")
	if r == nil {
		t.Fatal("rule not persisted")
	}
	if r.Response.Headers["Content-Type"] != "text/html; charset=utf-8" {
		t.Errorf("Content-Type not persisted: %+v", r.Response.Headers)
	}
	if r.Response.Headers["X-Forja"] != "1" {
		t.Errorf("X-Forja not persisted: %+v", r.Response.Headers)
	}
}

// TestUpdateReplacesHeaders: passing a non-nil headers patch replaces the
// whole map (no per-key merging — this matches the patch semantics of
// status/body/etc.).
func TestUpdateReplacesHeaders(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{
		Name: "x",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"X-Old":        "remove-me",
		},
	})
	replacement := map[string]string{"Content-Type": "text/html"}
	if err := Update(p, "x", nil, UpdateOptions{Headers: &replacement}); err != nil {
		t.Fatalf("Update headers: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("x")
	if r.Response.Headers["Content-Type"] != "text/html" {
		t.Errorf("Content-Type not updated: %+v", r.Response.Headers)
	}
	if _, has := r.Response.Headers["X-Old"]; has {
		t.Errorf("X-Old should have been dropped by replacement: %+v", r.Response.Headers)
	}
}

// TestUpdateClearsHeadersWithEmptyMap: passing &(empty map) clears all
// headers, while passing nil leaves them untouched.
func TestUpdateClearsHeadersWithEmptyMap(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{
		Name:    "x",
		Headers: map[string]string{"X-Keep": "1"},
	})

	// nil → leave as-is
	if err := Update(p, "x", nil, UpdateOptions{}); err != nil {
		t.Fatalf("Update no-op: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("x")
	if r.Response.Headers["X-Keep"] != "1" {
		t.Errorf("nil Headers patch should leave map untouched: %+v", r.Response.Headers)
	}

	// &empty → clear
	empty := map[string]string{}
	if err := Update(p, "x", nil, UpdateOptions{Headers: &empty}); err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	rf, _ = config.Load(p.Local)
	r = rf.FindRule("x")
	if len(r.Response.Headers) != 0 {
		t.Errorf("Headers should be cleared, got %+v", r.Response.Headers)
	}
}

// TestAddExplicitEmptyBody: an explicit empty body must survive yaml
// round-trip — the disk shape is `body: ""` (non-nil, empty string) which
// the device receives as a force-empty-body override.
func TestAddExplicitEmptyBody(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{
		Name:   "empty",
		Status: 204,
		Body:   &config.BodyValue{String: ""},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	rf, _ := config.Load(p.Local)
	r := rf.FindRule("empty")
	if r.Response.Body == nil {
		t.Fatalf("explicit empty body should round-trip as non-nil; got nil")
	}
	if r.Response.Body.String != "" {
		t.Errorf("body should be empty string, got %q", r.Response.Body.String)
	}
}

func TestLoadStatusReturnsCurrentState(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "shared"})
	_ = Enable(p, "com.a", []string{"shared"})
	_ = Enable(p, "com.b", []string{"shared"})
	st, err := LoadStatus(p)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	apps := st.AppsEnabling("shared")
	want := []string{"com.a", "com.b"}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("AppsEnabling: want %v, got %v", want, apps)
	}
}

func TestResolveAliasExpandsAndPassesThrough(t *testing.T) {
	p := pathsIn(t)
	if err := SaveAliasesScope(p, ScopeProject, map[string]string{
		"dev": "com.tkhskt.forja.sample",
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ResolveAlias(p, "dev"); got != "com.tkhskt.forja.sample" {
		t.Errorf("dev should resolve, got %q", got)
	}
	// Unknown name passes through so literal applicationIds keep working.
	if got, _ := ResolveAlias(p, "com.something"); got != "com.something" {
		t.Errorf("unknown should pass through, got %q", got)
	}
	// Empty input → empty.
	if got, _ := ResolveAlias(p, ""); got != "" {
		t.Errorf("empty input should be empty, got %q", got)
	}
}

func TestAliasScopesMergeWithLocalOverride(t *testing.T) {
	p := pathsIn(t)
	if err := SaveAliasesScope(p, ScopeProject, map[string]string{
		"dev":     "com.team.dev",
		"staging": "com.team.staging",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SaveAliasesScope(p, ScopeLocal, map[string]string{
		"dev":   "com.me.dev", // shadows the project "dev"
		"extra": "com.me.extra",
	}); err != nil {
		t.Fatal(err)
	}

	// Merged view: local wins on conflict, both scopes contribute.
	merged, err := LoadAliases(p)
	if err != nil {
		t.Fatal(err)
	}
	if merged["dev"] != "com.me.dev" {
		t.Errorf("local should override project for dev, got %q", merged["dev"])
	}
	if merged["staging"] != "com.team.staging" || merged["extra"] != "com.me.extra" {
		t.Errorf("merged map missing scoped entries: %+v", merged)
	}

	// Resolution goes through the merged map.
	if got, _ := ResolveAlias(p, "dev"); got != "com.me.dev" {
		t.Errorf("ResolveAlias(dev) = %q, want local override", got)
	}
	if got, _ := ResolveAlias(p, "staging"); got != "com.team.staging" {
		t.Errorf("ResolveAlias(staging) = %q, want project value", got)
	}

	// Per-scope reads stay unmerged (each file keeps its own entries).
	proj, _ := LoadAliasesScope(p, ScopeProject)
	if proj["dev"] != "com.team.dev" || len(proj) != 2 {
		t.Errorf("project scope file mutated unexpectedly: %+v", proj)
	}
	loc, _ := LoadAliasesScope(p, ScopeLocal)
	if _, ok := loc["staging"]; ok {
		t.Errorf("local scope file should not contain project-only entries: %+v", loc)
	}
}

func TestResolveAliasWithNoFileWorks(t *testing.T) {
	// No aliases file at all — the resolver should silently treat every
	// input as a literal applicationId.
	p := pathsIn(t)
	got, err := ResolveAlias(p, "com.example.app")
	if err != nil {
		t.Fatalf("ResolveAlias without file: %v", err)
	}
	if got != "com.example.app" {
		t.Errorf("want com.example.app, got %q", got)
	}
}

func TestRemoveUnknownRuleErrors(t *testing.T) {
	p := pathsIn(t)
	scope := ScopeProject
	err := Remove(p, "x", &scope)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want a not-found error, got %v", err)
	}
}

// ----------------------------------------------------------------------
// Rule name validation (ValidateRuleName / Add)
// ----------------------------------------------------------------------

func TestValidateRuleNameAcceptsCommonShapes(t *testing.T) {
	cases := []string{
		"simple",
		"hyphen-name",
		"snake_case",
		"dot.style",
		"with space inside",
		"トレーリング 日本語",
		"漢字とひらがなのカタカナ",
		"絵文字も🚀",
		"123-starts-with-digit",
		"single", // anything non-trivial UTF-8 / ASCII without comma / control
	}
	for _, name := range cases {
		if err := ValidateRuleName(name); err != nil {
			t.Errorf("ValidateRuleName(%q) unexpectedly rejected: %v", name, err)
		}
	}
}

func TestValidateRuleNameRejectsEmptyOrWhitespace(t *testing.T) {
	for _, bad := range []string{"", "   ", "\t", "\n"} {
		if err := ValidateRuleName(bad); err == nil {
			t.Errorf("ValidateRuleName(%q) should be rejected", bad)
		}
	}
}

func TestValidateRuleNameRejectsComma(t *testing.T) {
	for _, bad := range []string{",foo", "foo,", "foo,bar", "a, b"} {
		err := ValidateRuleName(bad)
		if err == nil {
			t.Errorf("ValidateRuleName(%q) should be rejected (comma collides with --enable splitting)", bad)
			continue
		}
		if !strings.Contains(err.Error(), ",") {
			t.Errorf("error for %q should mention the comma, got %v", bad, err)
		}
	}
}

func TestValidateRuleNameRejectsControlChars(t *testing.T) {
	for _, bad := range []string{"foo\nbar", "x\ty", "with\x00null", "tail-\r-cr"} {
		if err := ValidateRuleName(bad); err == nil {
			t.Errorf("ValidateRuleName(%q) should be rejected (control char)", bad)
		}
	}
}

func TestAddAcceptsMultiByteName(t *testing.T) {
	p := pathsIn(t)
	name := "認証 エラー 401"
	if err := Add(p, ScopeLocal, AddOptions{Name: name, Status: 401}); err != nil {
		t.Fatalf("Add multi-byte name: %v", err)
	}
	rf, err := config.Load(p.Local)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := rf.FindRule(name)
	if r == nil {
		t.Fatalf("rule %q not found after Add", name)
	}
	if r.Response.Status != 401 {
		t.Errorf("status not stored: %d", r.Response.Status)
	}
	// Enable / Disable round-trip preserves the multi-byte name through
	// status.json without normalization.
	if err := Enable(p, "com.example.app", []string{name}); err != nil {
		t.Fatalf("Enable multi-byte name: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if !st.IsEnabled("com.example.app", name) {
		t.Errorf("multi-byte name %q should be in com.example.app.s enabled list, got %+v", name, st)
	}
}

func TestAddRejectsCommaName(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{Name: "a,b"})
	if err == nil {
		t.Fatal("Add should reject comma in name")
	}
	if !strings.Contains(err.Error(), ",") {
		t.Errorf("error should mention comma, got %v", err)
	}
}
