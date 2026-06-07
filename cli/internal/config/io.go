package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Canonical paths under ./forja/, relative to cwd. The directory is the
// per-project root and forja never modifies these paths automatically — the
// dir layout is part of the user's repo.
const (
	DefaultPath       = "forja/rules.yml"       // project-scope rule definitions (you should commit it)
	DefaultLocalPath  = "forja/rules.local.yml" // local-scope rule definitions (you should gitignore it)
	DefaultStatusPath = "forja/status.json"     // per-(app, rule) enabled state (you should gitignore it)
)

// Load reads a RulesFile from disk. If the file is missing it returns nil
// without an error, since "no rules yet" is a valid state for `rules add`
// to create the file from scratch.
//
// Load enforces a same-file uniqueness invariant on rule names: a yml
// file with two entries sharing a name is rejected with an error pointing
// at the offending name + indices. This catches hand-edit mistakes that
// `rules add`'s in-process duplicate guard cannot see; without it, callers
// would silently push two-entry wire JSON in which the second copy is
// invisible to the on-device first-match interceptor, and CLI operations
// like `update`/`remove` would only ever touch the first one.
//
// Cross-scope uniqueness (= same name in rules.yml and rules.local.yml) is
// a stricter contract enforced one layer up by rules.loadBothScopes, since
// that layer is the one that has visibility into both files at once.
func Load(path string) (*RulesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rf RulesFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if dup, i, j, ok := firstDuplicateRuleName(rf.Rules); ok {
		return nil, fmt.Errorf(
			"%s: duplicate rule name %q at entries %d and %d — rule names must be unique within a single yml file",
			path, dup, i, j,
		)
	}
	return &rf, nil
}

// firstDuplicateRuleName scans rules in declaration order and returns the
// first name that appears twice along with the indices of both occurrences.
// Reporting the first hit (rather than collecting all duplicates) keeps the
// error message short and points the user at a concrete spot to fix; once
// they resolve it, a re-load surfaces the next one (if any).
func firstDuplicateRuleName(rules []Rule) (name string, first, second int, found bool) {
	seen := make(map[string]int, len(rules))
	for i, r := range rules {
		if prev, ok := seen[r.Name]; ok {
			return r.Name, prev, i, true
		}
		seen[r.Name] = i
	}
	return "", 0, 0, false
}

// Save writes the RulesFile to disk. The parent directory must already
// exist — `forja init` is responsible for creating forja/, and the cmd
// layer's requireForjaDir preflight gates every write path. Save itself
// does NOT mkdir so accidental writes from outside an initialized project
// fail loudly instead of silently materializing a stray forja/ directory.
// The output is deterministic (2-space indent, sorted-by-document-order).
func Save(path string, rf *RulesFile) error {
	enc, err := marshalYAML(rf)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, enc, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// marshalYAML serializes the rules file with a uniform 2-space indent.
// yaml.v3's package-level Marshal defaults to 4 spaces, which makes the
// top-level list (`rules:`) indent visually inconsistent with all the
// nested 2-space indents inside each rule. Going through the Encoder API
// lets us pin every level to 2 so the output reads cleanly.
func marshalYAML(rf *RulesFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(rf); err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshal yaml: close: %w", err)
	}
	return buf.Bytes(), nil
}

// AppStatus is the per-app slice of the workspace status. Enabled is the
// sparse list of rule names currently active for the app. A rule is "on"
// for an app iff its name appears in this list (= absent means off).
type AppStatus struct {
	Enabled []string `json:"enabled"`
}

// Status is the workspace-wide enabled state, keyed by Android applicationId.
// Lives in forja/status.json and is considered transient runtime state
// (intended to be gitignored — forja does not touch your .gitignore for you).
// The on-disk shape is a flat top-level map of app → {enabled},
// e.g. `{"com.example.app": {"enabled": ["rule-a", "rule-b"]}, "com.example.other": {"enabled": []}}`.
//
// An app key that exists with an empty `enabled` list means "this app has
// been touched by forja but currently has no rules active" (= the state right
// after `forja off`). An app key that's absent entirely means forja has never
// interacted with that app.
//
// Reads and writes go through MarshalJSON / UnmarshalJSON so that the file
// can carry a top-level "$comment" metadata key warning users that the file
// is forja-managed. The `$` prefix is a JSON Schema-style convention; we
// borrow the name here for a data instance because it sorts to position 0
// in json.MarshalIndent output (ASCII `$` < `A` < `_` < `a`), so the warning
// always lands on line 1 when an editor opens the file.
type Status map[string]AppStatus

// statusJSONComment is emitted at the top of status.json on every save.
// Hand edits are technically tolerated by UnmarshalJSON (we strip any
// `$`-prefixed metadata key) but will be silently overwritten the next time
// forja writes the file. The string is intentionally short — the README
// holds the why and the recovery instructions.
const statusJSONComment = "THIS FILE IS GENERATED BY forja. DO NOT EDIT BY HAND."

// MarshalJSON emits the per-app map alongside a top-level $comment key. The
// encoding/json package sorts string-keyed map output, so $comment lands on
// the first line of the file.
func (s Status) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(s)+1)
	cb, err := json.Marshal(statusJSONComment)
	if err != nil {
		return nil, err
	}
	out["$comment"] = cb
	for k, v := range s {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal status[%s]: %w", k, err)
		}
		out[k] = b
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads the per-app map while ignoring any `$`-prefixed
// metadata key (the file-level $comment we write, plus any future $schema
// etc.). Keys starting with `$` are reserved for forja metadata and never
// represent a real Android applicationId.
func (s *Status) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if *s == nil {
		*s = Status{}
	}
	for k, v := range raw {
		if strings.HasPrefix(k, "$") {
			continue
		}
		var as AppStatus
		if err := json.Unmarshal(v, &as); err != nil {
			return fmt.Errorf("decode status[%s]: %w", k, err)
		}
		(*s)[k] = as
	}
	return nil
}

// IsEnabled reports whether name is in the enabled list for app. Default
// (absent app or absent name) is false.
func (s Status) IsEnabled(app, name string) bool {
	as, ok := s[app]
	if !ok {
		return false
	}
	for _, n := range as.Enabled {
		if n == name {
			return true
		}
	}
	return false
}

// Enable adds name to app's enabled list. No-op if already present.
func (s Status) Enable(app, name string) {
	as := s[app]
	for _, n := range as.Enabled {
		if n == name {
			return
		}
	}
	as.Enabled = append(as.Enabled, name)
	s[app] = as
}

// Disable removes name from app's enabled list. No-op if absent.
func (s Status) Disable(app, name string) {
	as, ok := s[app]
	if !ok {
		return
	}
	for i, n := range as.Enabled {
		if n == name {
			as.Enabled = append(as.Enabled[:i], as.Enabled[i+1:]...)
			s[app] = as
			return
		}
	}
}

// ClearApp wipes app's enabled list (= all rules off for app) while keeping
// the app key. Mirrors what `forja off --app X` does to local state.
func (s Status) ClearApp(app string) {
	s[app] = AppStatus{Enabled: []string{}}
}

// AppsEnabling returns the sorted set of apps that currently have name in
// their enabled list. Used by `rules update / remove` to discover which apps
// should receive the auto-propagated push.
func (s Status) AppsEnabling(name string) []string {
	out := make([]string, 0)
	for app, as := range s {
		for _, n := range as.Enabled {
			if n == name {
				out = append(out, app)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// DropRule removes name from every app's enabled list. Called when a rule
// is deleted from the catalog so stale status entries don't accumulate.
func (s Status) DropRule(name string) {
	for app, as := range s {
		for i, n := range as.Enabled {
			if n == name {
				as.Enabled = append(as.Enabled[:i], as.Enabled[i+1:]...)
				s[app] = as
				break
			}
		}
	}
}

// EnabledFor returns the enabled-rule list for app in a defensive copy. Empty
// (nil) for unknown apps.
func (s Status) EnabledFor(app string) []string {
	as, ok := s[app]
	if !ok || len(as.Enabled) == 0 {
		return nil
	}
	out := make([]string, len(as.Enabled))
	copy(out, as.Enabled)
	return out
}

// LoadStatus reads forja/status.json. Missing file returns an empty Status
// (not nil) so callers can mutate without nil checks.
func LoadStatus(path string) (Status, error) {
	s := Status{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// SaveStatus writes the status to disk. JSON map key order is sorted by
// encoding/json so the output is stable. The parent directory must already
// exist — see the Save() comment for the directory-creation contract.
func SaveStatus(path string, s Status) error {
	if s == nil {
		s = Status{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// EffectiveRule is one rule plus context the rule layer needs after merging:
// the scope it came from (for TUI section header) and the directory of the
// source yml file (for resolving relative bodyFile paths). Rule names are
// required to be unique across both scopes (the rules layer validates this
// when loading both files together), so no per-entry shadow flag is needed.
type EffectiveRule struct {
	Rule
	Scope   string // "project" or "local"
	BaseDir string // directory of the yml file the rule came from (for bodyFile resolution)
}

// Scope constants for EffectiveRule.Scope.
const (
	ScopeProject = "project"
	ScopeLocal    = "local"
)

// ResolveBody returns the body content to ship to the device. Either reads
// the BodyFile from disk (if set) or returns the inline Body (if set). At
// most one of Body/BodyFile may be set; both being set is an error.
//
// BodyFile content interpretation:
//   - file extension `.json` → parse as a JSON object, return as BodyValue.Object
//   - any other extension     → raw bytes as BodyValue.String
//
// Relative BodyFile paths are resolved against EffectiveRule.BaseDir.
func (er *EffectiveRule) ResolveBody() (*BodyValue, error) {
	if er.Response.BodyFile == "" {
		return er.Response.Body, nil
	}
	if er.Response.Body != nil {
		return nil, fmt.Errorf("rule %q: cannot set both body and bodyFile", er.Name)
	}
	path := er.Response.BodyFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(er.BaseDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rule %q: read bodyFile %s: %w", er.Name, path, err)
	}
	if strings.EqualFold(filepath.Ext(er.Response.BodyFile), ".json") {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("rule %q: parse %s as JSON object: %w", er.Name, path, err)
		}
		return &BodyValue{Object: m}, nil
	}
	return &BodyValue{String: string(data)}, nil
}

// Effective merges project + local rules and applies the per-app status to
// produce the rule list for a specific app. Rule names are unique across
// both scopes (the rules layer rejects cross-scope name duplicates on load
// and on add), so this function is a straight concat:
//
//   - local rules first (in declaration order)
//   - project rules second (in declaration order)
//
// The local-first ordering matches the on-device interceptor's first-match
// semantics — if two rules with different names happen to match the same
// request, the local one fires first. Same-request match collision is the
// only collision Effective has to think about; name-level collisions are
// caught upstream.
//
// projectDir and localDir are the directories of the yml files each scope
// came from. They're propagated to each EffectiveRule.BaseDir so relative
// bodyFile paths can be resolved at push time.
//
// `app` is the target Android applicationId; each returned rule's Enabled
// field is set based on the per-app enabled list in status (absent = disabled).
func Effective(project *RulesFile, projectDir string, local *RulesFile, localDir string, status Status, app string) []EffectiveRule {
	out := make([]EffectiveRule, 0)
	if local != nil {
		for _, r := range local.Rules {
			rr := r
			rr.Enabled = status.IsEnabled(app, r.Name)
			out = append(out, EffectiveRule{Rule: rr, Scope: ScopeLocal, BaseDir: localDir})
		}
	}
	if project != nil {
		for _, r := range project.Rules {
			rr := r
			rr.Enabled = status.IsEnabled(app, r.Name)
			out = append(out, EffectiveRule{Rule: rr, Scope: ScopeProject, BaseDir: projectDir})
		}
	}
	return out
}

// EffectiveToDeviceJSON converts the merged effective ruleset to the JSON
// array format expected by the on-device runtime. Only enabled rules are
// included; order is preserved (first match wins on-device).
//
// For each rule, body content is resolved via EffectiveRule.ResolveBody
// (reads bodyFile if set, otherwise uses inline body). File-read errors
// propagate so the user sees a clear "rule X bodyFile not found" rather
// than a silent missing-body push.
func EffectiveToDeviceJSON(rules []EffectiveRule) ([]byte, error) {
	out := make([]map[string]any, 0, len(rules))
	for _, er := range rules {
		if !er.Enabled {
			continue
		}
		body, err := er.ResolveBody()
		if err != nil {
			return nil, err
		}
		m := ruleToDeviceMap(er.Rule)
		// ruleToDeviceMap used the inline Body field; if ResolveBody read a
		// file, swap in the resolved value. A non-nil but-empty body still
		// emits `"body": ""` — that's an explicit empty-body override.
		delete(m, "body")
		delete(m, "bodyObject")
		if body != nil {
			if body.Object != nil {
				m["bodyObject"] = body.Object
			} else {
				m["body"] = body.String
			}
		}
		out = append(out, m)
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return append(buf, '\n'), nil
}

// ToDeviceJSON serializes the enabled-only subset of rules as the JSON array
// expected by the runtime's FileRulesProvider. Order is preserved (first match
// wins on-device).
//
// Considers only Rule.Enabled — the per-app enabled state in status.json
// is NOT consulted. Use EffectiveToDeviceJSON for production push paths that
// need per-(app, rule) resolution.
func (rf *RulesFile) ToDeviceJSON() ([]byte, error) {
	out := make([]map[string]any, 0, len(rf.Rules))
	for _, r := range rf.Rules {
		if !r.Enabled {
			continue
		}
		out = append(out, ruleToDeviceMap(r))
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return append(buf, '\n'), nil
}

// ruleToDeviceMap converts a single rule to the device JSON shape. The
// yml's nested `match` + `response` groups are flattened back to the flat
// wire format the runtime expects ({name, enabled, host, path, status,
// headers, body}). Kotlin's parseList stays unchanged; the nesting is purely
// an authoring convenience for hand-edited yml.
//
// Body is emitted whenever the pointer is non-nil — that includes the
// "explicit empty" case (`&BodyValue{}` → `"body": ""`), which the runtime
// interprets as "replace the response body with an empty one".
func ruleToDeviceMap(r Rule) map[string]any {
	m := map[string]any{
		"name":    r.Name,
		"enabled": true,
	}
	if r.Match.Host != "" {
		m["host"] = r.Match.Host
	}
	if r.Match.Path != "" {
		m["path"] = r.Match.Path
	}
	if r.Response.Status != 0 {
		m["status"] = r.Response.Status
	}
	if len(r.Response.Headers) > 0 {
		m["headers"] = r.Response.Headers
	}
	if r.Response.Body != nil {
		if r.Response.Body.Object != nil {
			m["bodyObject"] = r.Response.Body.Object
		} else {
			m["body"] = r.Response.Body.String
		}
	}
	return m
}
