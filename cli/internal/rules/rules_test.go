package rules

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tkhskt/forja/internal/config"
)

// pathsIn returns a Paths struct rooted under a fresh tempdir, mirroring the
// production forja/ layout.
func pathsIn(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Project: filepath.Join(dir, "forja", "rules.yml"),
		Local:   filepath.Join(dir, "forja", "rules.local.yml"),
		Status:  filepath.Join(dir, "forja", "status.json"),
		Aliases: filepath.Join(dir, "forja", "aliases.local.yml"),
	}
}

func TestAddYmlOnlyByDefault(t *testing.T) {
	p := pathsIn(t)
	err := Add(p, ScopeLocal, AddOptions{Name: "mock", Host: "x.com", Status: 500})
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

func TestAddSameNameInDifferentScopesIsAllowed(t *testing.T) {
	// Shadow rules: same name in project + user. User wins at Effective time.
	p := pathsIn(t)
	if err := Add(p, ScopeProject, AddOptions{Name: "x", Status: 500}); err != nil {
		t.Fatal(err)
	}
	if err := Add(p, ScopeLocal, AddOptions{Name: "x", Status: 999}); err != nil {
		t.Errorf("shadow rule should be allowed: %v", err)
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

func TestRemoveExplicitScopeOnShadow(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeProject, AddOptions{Name: "x"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "x"})

	scope := ScopeProject
	if err := Remove(p, "x", &scope); err != nil {
		t.Fatalf("Remove explicit project: %v", err)
	}
	pf, _ := config.Load(p.Project)
	if pf.FindRule("x") != nil {
		t.Errorf("project x should be removed")
	}
	uf, _ := config.Load(p.Local)
	if uf.FindRule("x") == nil {
		t.Errorf("user x should be untouched")
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
	if err := Enable(p, "com.x", []string{"foo", "bar"}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if !st.IsEnabled("com.x", "foo") || !st.IsEnabled("com.x", "bar") {
		t.Errorf("Enable did not record entries: %+v", st)
	}
}

func TestEnableRejectsUnknownRuleNames(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "real"})
	err := Enable(p, "com.x", []string{"real", "typo"})
	if err == nil {
		t.Error("expected error for unknown rule 'typo'")
	}
	// Real should not have been added either (early reject).
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.x", "real") {
		t.Errorf("Enable should be atomic — 'real' should not be set when 'typo' is bogus")
	}
}

func TestDisableRemovesFromPkgEnabledList(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "foo"})
	_ = Enable(p, "com.x", []string{"foo"})
	if err := Disable(p, "com.x", []string{"foo"}); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.x", "foo") {
		t.Errorf("foo should be disabled: %+v", st)
	}
}

func TestDisableIgnoresUnknownRuleNames(t *testing.T) {
	p := pathsIn(t)
	// No yml entries at all — Disable should silently no-op for typo scrubbing.
	if err := Disable(p, "com.x", []string{"never-existed"}); err != nil {
		t.Errorf("Disable of unknown name should not error: %v", err)
	}
}

func TestClearAppEmptiesEnabledList(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "foo"})
	_ = Enable(p, "com.x", []string{"foo"})
	if err := ClearApp(p, "com.x"); err != nil {
		t.Fatalf("ClearApp: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("com.x", "foo") {
		t.Errorf("foo should be cleared: %+v", st)
	}
}

func TestSetEnabledForAppOverwrites(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeLocal, AddOptions{Name: "a"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "b"})
	_ = Add(p, ScopeLocal, AddOptions{Name: "c"})
	_ = Enable(p, "com.x", []string{"a", "b"})
	// Overwrite with a new set — b should go away, c should appear.
	if err := SetEnabledForApp(p, "com.x", []string{"a", "c"}); err != nil {
		t.Fatalf("SetEnabledForApp: %v", err)
	}
	st, _ := config.LoadStatus(p.Status)
	if !st.IsEnabled("com.x", "a") || !st.IsEnabled("com.x", "c") {
		t.Errorf("a and c should be enabled: %+v", st)
	}
	if st.IsEnabled("com.x", "b") {
		t.Errorf("b should have been removed: %+v", st)
	}
}

func TestLoadEffectiveMergesAndOverridesPerApp(t *testing.T) {
	p := pathsIn(t)
	_ = Add(p, ScopeProject, AddOptions{Name: "team-a", Status: 200})
	_ = Add(p, ScopeProject, AddOptions{Name: "team-b", Status: 200})
	_ = Add(p, ScopeLocal, AddOptions{Name: "team-b", Status: 999})
	_ = Add(p, ScopeLocal, AddOptions{Name: "personal", Status: 418})
	// Enable on com.x: team-b (shadowed user) + personal only — leave team-a off.
	_ = Enable(p, "com.x", []string{"team-b", "personal"})

	rules, err := LoadEffective(p, "com.x")
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	// Expected order: user rules first (team-b, personal), then un-shadowed
	// project rules (team-a).
	if len(rules) != 3 {
		t.Fatalf("want 3 effective rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].Name != "team-b" || rules[0].Scope != config.ScopeLocal || rules[0].Response.Status != 999 || !rules[0].Enabled {
		t.Errorf("rules[0]: %+v", rules[0])
	}
	if rules[1].Name != "personal" || !rules[1].Enabled {
		t.Errorf("rules[1]: %+v", rules[1])
	}
	if rules[2].Name != "team-a" || rules[2].Enabled {
		t.Errorf("rules[2] should be off (not enabled on com.x): %+v", rules[2])
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

	rules, err := LoadEffective(p, "com.x")
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
	if err := SaveAliases(p, map[string]string{
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

func TestResolveAliasWithNoFileWorks(t *testing.T) {
	// No aliases file at all — the resolver should silently treat every
	// input as a literal applicationId.
	p := pathsIn(t)
	got, err := ResolveAlias(p, "com.x")
	if err != nil {
		t.Fatalf("ResolveAlias without file: %v", err)
	}
	if got != "com.x" {
		t.Errorf("want com.x, got %q", got)
	}
}

func TestErrNoFileSentinel(t *testing.T) {
	p := pathsIn(t)
	scope := ScopeProject
	err := Remove(p, "x", &scope)
	if !errors.Is(err, ErrNoFile) {
		t.Errorf("want ErrNoFile, got %v", err)
	}
}
