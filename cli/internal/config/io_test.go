package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingReturnsNil(t *testing.T) {
	rf, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if rf != nil {
		t.Errorf("want nil for missing file, got %v", rf)
	}
}

func TestLoadParsesRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yml")
	// Unknown top-level fields (here `extraField:`) are silently dropped by
	// yaml.v3, so yml files with extras keep parsing.
	src := `
extraField: ignored-value
rules:
  - name: mock-failure
    match:
      host: example.com
      path: /foo
    response:
      status: 500
      body: '{"message":"failure"}'
  - name: slow-bar
    match:
      host: example.com
      path: /bar
    response:
      status: 200
      body: "raw string body"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rf, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(rf.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rf.Rules))
	}
	r0 := rf.Rules[0]
	if r0.Name != "mock-failure" || r0.Response.Status != 500 {
		t.Errorf("rule[0] unexpected: %+v", r0)
	}
	// In the yml everything is a string — structure-carrying JSON arrives
	// as String, not Object. The CLI's --body auto-detect is the only path
	// that ever sets Object.
	if r0.Response.Body == nil || r0.Response.Body.String != `{"message":"failure"}` {
		t.Errorf("rule[0] body not preserved as JSON string: %+v", r0.Response.Body)
	}
	r1 := rf.Rules[1]
	if r1.Response.Body == nil || r1.Response.Body.String != "raw string body" {
		t.Errorf("rule[1] body not scalar: %+v", r1.Response.Body)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "rules.yml") // exercise MkdirAll
	orig := &RulesFile{
		Rules: []Rule{
			{Name: "a", Enabled: true,
				Match:    Match{Host: "x.com"},
				Response: Response{Status: 200, Body: &BodyValue{Object: map[string]any{"ok": true}}}},
			{Name: "b", Enabled: false,
				Match:    Match{Path: "/baz"},
				Response: Response{Body: &BodyValue{String: "scalar"}}},
		},
	}
	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load back: %v", err)
	}
	if len(back.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(back.Rules))
	}
	// Object bodies are saved as JSON-encoded scalars, so on read-back the
	// content moves into String (Object is lossy by design — see types.go).
	if back.Rules[0].Response.Body == nil || back.Rules[0].Response.Body.String != `{"ok":true}` {
		t.Errorf("rule[0] body round-trip broken: %+v", back.Rules[0].Response.Body)
	}
	if back.Rules[1].Response.Body == nil || back.Rules[1].Response.Body.String != "scalar" {
		t.Errorf("rule[1] body round-trip broken: %+v", back.Rules[1].Response.Body)
	}
}

func TestStatusPerPkgRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "status.json")
	orig := Status{
		"com.a": {Enabled: []string{"foo", "bar"}},
		"com.b": {Enabled: []string{"foo"}},
		"com.c": {Enabled: []string{}}, // touched but currently off
	}
	if err := SaveStatus(path, orig); err != nil {
		t.Fatalf("SaveStatus: %v", err)
	}
	back, err := LoadStatus(path)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if !back.IsEnabled("com.a", "foo") || !back.IsEnabled("com.a", "bar") {
		t.Errorf("com.a entries lost: %+v", back)
	}
	if !back.IsEnabled("com.b", "foo") {
		t.Errorf("com.b/foo lost: %+v", back)
	}
	if back.IsEnabled("com.b", "bar") {
		t.Errorf("com.b/bar should not be enabled: %+v", back)
	}
	if back.IsEnabled("com.c", "anything") {
		t.Errorf("com.c is empty list — should report disabled: %+v", back)
	}
}

func TestStatusEnableDisableMutations(t *testing.T) {
	s := Status{}
	s.Enable("com.x", "foo")
	s.Enable("com.x", "bar")
	s.Enable("com.x", "foo") // duplicate add → no-op
	if got := len(s["com.x"].Enabled); got != 2 {
		t.Errorf("want 2 enabled, got %d (%v)", got, s["com.x"].Enabled)
	}
	s.Disable("com.x", "foo")
	if s.IsEnabled("com.x", "foo") {
		t.Errorf("foo should be disabled after Disable")
	}
	if !s.IsEnabled("com.x", "bar") {
		t.Errorf("bar should still be enabled")
	}
	s.Disable("com.x", "nope") // disable of absent → no-op
	if got := len(s["com.x"].Enabled); got != 1 {
		t.Errorf("want 1 enabled after disable, got %d", got)
	}
}

func TestStatusAppsEnablingFindsAll(t *testing.T) {
	s := Status{
		"com.a": {Enabled: []string{"shared", "a-only"}},
		"com.b": {Enabled: []string{"shared"}},
		"com.c": {Enabled: []string{"c-only"}},
	}
	apps := s.AppsEnabling("shared")
	want := []string{"com.a", "com.b"}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("AppsEnabling(shared): want %v, got %v", want, apps)
	}
	if got := s.AppsEnabling("a-only"); !reflect.DeepEqual(got, []string{"com.a"}) {
		t.Errorf("AppsEnabling(a-only): got %v", got)
	}
	if got := s.AppsEnabling("missing"); len(got) != 0 {
		t.Errorf("AppsEnabling(missing): want empty, got %v", got)
	}
}

func TestStatusDropRuleSweepsAllApps(t *testing.T) {
	s := Status{
		"com.a": {Enabled: []string{"shared", "a-only"}},
		"com.b": {Enabled: []string{"shared"}},
	}
	s.DropRule("shared")
	if s.IsEnabled("com.a", "shared") || s.IsEnabled("com.b", "shared") {
		t.Errorf("shared should be gone from all apps: %+v", s)
	}
	if !s.IsEnabled("com.a", "a-only") {
		t.Errorf("a-only lost as collateral: %+v", s)
	}
}

func TestStatusClearAppKeepsKey(t *testing.T) {
	s := Status{"com.a": {Enabled: []string{"foo"}}}
	s.ClearApp("com.a")
	if _, ok := s["com.a"]; !ok {
		t.Errorf("ClearApp should keep the key (with empty list)")
	}
	if len(s["com.a"].Enabled) != 0 {
		t.Errorf("ClearApp should empty the list: %+v", s["com.a"])
	}
}

// TestStatusJSONEmbedsCommentAtTop: SaveStatus must write a $comment metadata
// key, and that key must sort to the first line of the file so editors see
// the "managed file" warning immediately on open.
func TestStatusJSONEmbedsCommentAtTop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	orig := Status{
		"com.a": {Enabled: []string{"rule-1"}},
		"com.b": {Enabled: []string{}},
	}
	if err := SaveStatus(path, orig); err != nil {
		t.Fatalf("SaveStatus: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status.json: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"$comment"`) {
		t.Fatalf("expected $comment key in status.json; got:\n%s", body)
	}
	if !strings.Contains(body, "DO NOT EDIT") {
		t.Errorf("expected a do-not-edit hint in $comment value; got:\n%s", body)
	}
	// $comment must precede every app key (the marshaler sorts by ASCII
	// and `$` 0x24 is less than every letter, so this should hold).
	commentIdx := strings.Index(body, `"$comment"`)
	for _, app := range []string{`"com.a"`, `"com.b"`} {
		if idx := strings.Index(body, app); idx > 0 && idx < commentIdx {
			t.Errorf("$comment should appear before %s; got: comment=%d %s=%d", app, commentIdx, app, idx)
		}
	}
}

// TestStatusJSONLoadIgnoresMetaKeys: hand-authored status.json files (or
// older forja outputs that introduce additional `$`-prefixed metadata keys)
// must load successfully — the metadata is silently dropped, the real
// app entries survive.
func TestStatusJSONLoadIgnoresMetaKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	manual := []byte(`{
  "$comment": "hand-authored, expect this to be stripped",
  "$schema": "https://example.com/forja-status.schema.json",
  "com.real": { "enabled": ["foo"] },
  "com.empty": { "enabled": [] }
}`)
	if err := os.WriteFile(path, manual, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadStatus(path)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if _, exists := st["$comment"]; exists {
		t.Errorf("$comment must be stripped, but found in loaded Status: %+v", st)
	}
	if _, exists := st["$schema"]; exists {
		t.Errorf("$schema must be stripped, but found in loaded Status: %+v", st)
	}
	if !st.IsEnabled("com.real", "foo") {
		t.Errorf("com.real/foo should be enabled: %+v", st)
	}
	if _, exists := st["com.empty"]; !exists {
		t.Errorf("com.empty entry should survive (even with empty list): %+v", st)
	}

	// Round-trip: saving again must re-emit $comment (forja-owned), and
	// loading once more must keep IsEnabled stable.
	if err := SaveStatus(path, st); err != nil {
		t.Fatalf("SaveStatus round-trip: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"$comment"`) {
		t.Errorf("re-save lost the $comment metadata key")
	}
	back, err := LoadStatus(path)
	if err != nil {
		t.Fatalf("LoadStatus round-trip: %v", err)
	}
	if !back.IsEnabled("com.real", "foo") {
		t.Errorf("com.real/foo should still be enabled after round-trip: %+v", back)
	}
}

func TestToDeviceJSONEnabledOnly(t *testing.T) {
	rf := &RulesFile{
		Rules: []Rule{
			{Name: "on", Enabled: true,
				Match:    Match{Host: "x.com"},
				Response: Response{Status: 500, Body: &BodyValue{Object: map[string]any{"k": "v"}}}},
			{Name: "off", Enabled: false,
				Match:    Match{Host: "x.com"},
				Response: Response{Status: 200}},
			{Name: "scalar-body", Enabled: true,
				Match:    Match{Path: "/p"},
				Response: Response{Body: &BodyValue{String: "plain"}}},
		},
	}
	js, err := rf.ToDeviceJSON()
	if err != nil {
		t.Fatalf("ToDeviceJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(js, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, js)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 enabled rules, got %d", len(got))
	}
	if got[0]["name"] != "on" {
		t.Errorf("first rule should be 'on', got %v", got[0]["name"])
	}
	if got[0]["enabled"] != true {
		t.Errorf("enabled flag missing/false on enabled rule: %v", got[0])
	}
	// status comes back as float64 from json.Unmarshal into map[string]any
	if got[0]["status"].(float64) != 500 {
		t.Errorf("status mismatch: %v", got[0]["status"])
	}
	bo, ok := got[0]["bodyObject"].(map[string]any)
	if !ok {
		t.Fatalf("bodyObject missing or wrong type: %v", got[0])
	}
	if bo["k"] != "v" {
		t.Errorf("bodyObject content: %v", bo)
	}
	// Should NOT have a plain "body" key when bodyObject was set
	if _, has := got[0]["body"]; has {
		t.Errorf("body and bodyObject both present (only one should be)")
	}

	// Scalar body case
	if got[1]["name"] != "scalar-body" {
		t.Errorf("second rule should be 'scalar-body', got %v", got[1]["name"])
	}
	if got[1]["body"] != "plain" {
		t.Errorf("body not preserved as string: %v", got[1]["body"])
	}
	if _, has := got[1]["bodyObject"]; has {
		t.Errorf("scalar body should not produce bodyObject")
	}
}

// Partial-field rules: host-only, path-only, status-only must round-trip
// through ToDeviceJSON without leaking empty strings into the device JSON.
// (Empty fields would otherwise cause the runtime to fail every match since
//  o.optStringOrNull("host") returns "" rather than null, and url.host != ""
//  evaluates true.)
func TestToDeviceJSONOmitsEmptyMatchFields(t *testing.T) {
	rf := &RulesFile{Rules: []Rule{
		{Name: "host-only", Enabled: true,
			Match:    Match{Host: "example.com"},
			Response: Response{Status: 418}},
		{Name: "path-only", Enabled: true,
			Match:    Match{Path: "/api"},
			Response: Response{Status: 418}},
		{Name: "status-only", Enabled: true,
			Response: Response{Status: 503}},
	}}
	js, err := rf.ToDeviceJSON()
	if err != nil {
		t.Fatalf("ToDeviceJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(js, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, js)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rules, got %d", len(got))
	}
	// host-only must not carry a "path" key
	if _, has := got[0]["path"]; has {
		t.Errorf("host-only rule leaked a path key: %v", got[0])
	}
	// path-only must not carry a "host" key
	if _, has := got[1]["host"]; has {
		t.Errorf("path-only rule leaked a host key: %v", got[1])
	}
	// status-only must not carry host or path keys
	for _, k := range []string{"host", "path"} {
		if _, has := got[2][k]; has {
			t.Errorf("status-only rule leaked a %s key: %v", k, got[2])
		}
	}
}

func TestResolveBodyJSONFile(t *testing.T) {
	dir := t.TempDir()
	bodyPath := filepath.Join(dir, "resp.json")
	if err := os.WriteFile(bodyPath, []byte(`{"message":"failure","code":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	er := &EffectiveRule{
		Rule:    Rule{Name: "x", Response: Response{BodyFile: "resp.json"}},
		Scope:   ScopeLocal,
		BaseDir: dir,
	}
	b, err := er.ResolveBody()
	if err != nil {
		t.Fatalf("ResolveBody: %v", err)
	}
	if b.Object == nil {
		t.Fatalf("want Object body for .json file, got %+v", b)
	}
	if b.Object["message"] != "failure" {
		t.Errorf("message field: %v", b.Object["message"])
	}
}

func TestResolveBodyPlainTextFile(t *testing.T) {
	dir := t.TempDir()
	bodyPath := filepath.Join(dir, "resp.txt")
	if err := os.WriteFile(bodyPath, []byte("hello plaintext"), 0o644); err != nil {
		t.Fatal(err)
	}
	er := &EffectiveRule{
		Rule:    Rule{Name: "x", Response: Response{BodyFile: "resp.txt"}},
		Scope:   ScopeLocal,
		BaseDir: dir,
	}
	b, err := er.ResolveBody()
	if err != nil {
		t.Fatalf("ResolveBody: %v", err)
	}
	if b.String != "hello plaintext" {
		t.Errorf("plain text body: %q", b.String)
	}
}

func TestResolveBodyAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "resp.json")
	if err := os.WriteFile(abs, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	er := &EffectiveRule{
		Rule:    Rule{Name: "x", Response: Response{BodyFile: abs}},
		Scope:   ScopeLocal,
		BaseDir: "/tmp/other-dir-doesnt-matter",
	}
	b, err := er.ResolveBody()
	if err != nil {
		t.Fatalf("ResolveBody: %v", err)
	}
	if b.Object["ok"] != true {
		t.Errorf("absolute path resolution failed: %+v", b)
	}
}

func TestResolveBodyInlineBodyTakesPrecedenceWhenFileEmpty(t *testing.T) {
	// When BodyFile is empty, return inline Body unchanged.
	er := &EffectiveRule{
		Rule: Rule{Name: "x", Response: Response{Body: &BodyValue{Object: map[string]any{"k": "v"}}}},
	}
	b, err := er.ResolveBody()
	if err != nil {
		t.Fatalf("ResolveBody: %v", err)
	}
	if b.Object["k"] != "v" {
		t.Errorf("inline body lost: %+v", b)
	}
}

func TestResolveBodyBothSetIsError(t *testing.T) {
	er := &EffectiveRule{
		Rule: Rule{
			Name: "x",
			Response: Response{
				Body:     &BodyValue{String: "inline"},
				BodyFile: "some.json",
			},
		},
		BaseDir: "/tmp",
	}
	_, err := er.ResolveBody()
	if err == nil {
		t.Error("expected error when both body and bodyFile are set")
	}
}

func TestResolveBodyMissingFile(t *testing.T) {
	er := &EffectiveRule{
		Rule:    Rule{Name: "x", Response: Response{BodyFile: "nonexistent.json"}},
		BaseDir: t.TempDir(),
	}
	_, err := er.ResolveBody()
	if err == nil {
		t.Error("expected error for missing bodyFile")
	}
}

func TestEffectiveToDeviceJSONResolvesBodyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(`{"shared":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rules := []EffectiveRule{
		{
			Rule:    Rule{Name: "a", Enabled: true, Response: Response{Status: 500, BodyFile: "team.json"}},
			Scope:   ScopeLocal,
			BaseDir: dir,
		},
	}
	js, err := EffectiveToDeviceJSON(rules)
	if err != nil {
		t.Fatalf("EffectiveToDeviceJSON: %v", err)
	}
	var out []map[string]any
	_ = json.Unmarshal(js, &out)
	if len(out) != 1 {
		t.Fatalf("want 1 rule, got %d", len(out))
	}
	bo, ok := out[0]["bodyObject"].(map[string]any)
	if !ok {
		t.Fatalf("bodyObject missing/wrong type: %v", out[0])
	}
	if bo["shared"] != true {
		t.Errorf("file content not propagated: %+v", bo)
	}
}

func TestToDeviceJSONEmpty(t *testing.T) {
	rf := &RulesFile{Rules: []Rule{{Name: "off", Enabled: false}}}
	js, err := rf.ToDeviceJSON()
	if err != nil {
		t.Fatalf("ToDeviceJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(js, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(got, []map[string]any{}) {
		t.Errorf("want empty array, got %v", got)
	}
}
