// Package config defines forja's on-disk file formats — the rule catalog
// (forja/rules.yml + rules.local.yml), the per-package enabled state
// (forja/status.json), and the personal alias map (forja/aliases.local.yml) —
// and converts the catalog to the JSON format consumed by the on-device runtime.
package config

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// RulesFile is the top-level structure of ./forja/rules.yml. The file is a
// pure catalog — no package targeting lives in the yml. Rules are applied to
// specific packages at runtime via status.json's per-package enabled lists.
//
// Order in `Rules` is significant: the on-device interceptor uses first-match
// semantics, so callers preserve declared order through to the device JSON.
type RulesFile struct {
	Rules []Rule `yaml:"rules"`
}

// Rule is one rewrite rule. Its two sub-structs split the catalog cleanly
// into "when does this fire?" (Match) and "what does it do?" (Response).
//
// `Enabled` is intentionally NOT persisted to the yaml file (yaml:"-"). The
// enabled state lives in forja/status.json so that toggling a rule on/off
// doesn't create noise in git diffs of the shared rules.yml. The field
// remains on the struct because it's used in-process when materializing the
// "effective" view (= rules merged with status overrides) for the device JSON
// payload.
type Rule struct {
	Name     string   `yaml:"name"`
	Enabled  bool     `yaml:"-"`
	Match    Match    `yaml:"match,omitempty"`
	Response Response `yaml:"response,omitempty"`
}

// Match collects the request-side fields that decide whether a rule fires.
// Empty fields mean "no constraint on this dimension". All non-empty fields
// are AND-ed together.
type Match struct {
	Host string `yaml:"host,omitempty"`
	Path string `yaml:"path,omitempty"`
}

// Response collects the fields that get applied to a matched response.
//
// `Body` and `BodyFile` are mutually exclusive ways to supply the response
// body. Body is inline (always a string scalar in the yml — to send a JSON
// object, write it as a JSON-encoded string: `body: '{"x":1}'`); BodyFile is
// a path to an external file. The path is resolved relative to the directory
// of the yml that declared the rule (or absolute). At push time the file is
// read and:
//   - `.json` extension → parsed as JSON object → emitted as `bodyObject`
//   - anything else      → raw bytes → emitted as `body` (string)
//
// `Body == nil` means "no body override — original response body passes
// through". An explicit empty body (e.g. `body: ""` in yml, or `--body ""`
// on the CLI) is represented by a non-nil pointer with String == "" and is
// distinct from nil — it forces the on-device response to have an empty body.
//
// `Headers` is an optional override map applied on top of the original
// response headers (with Content-Type also driving the response body's MIME).
type Response struct {
	Status   int               `yaml:"status,omitempty"`
	Body     *BodyValue        `yaml:"body,omitempty"`
	BodyFile string            `yaml:"bodyFile,omitempty"`
	Headers  map[string]string `yaml:"headers,omitempty"`
}

// IsZero reports whether m has no fields set. Used by yaml MarshalYAML to
// omit empty Match blocks from the output.
func (m Match) IsZero() bool {
	return m.Host == "" && m.Path == ""
}

// IsZero reports whether r has no fields set. Used by yaml MarshalYAML to
// omit empty Response blocks from the output.
func (r Response) IsZero() bool {
	return r.Status == 0 && r.Body == nil && r.BodyFile == "" && len(r.Headers) == 0
}

// BodyValue holds the response body for a rule. In the yml the body is
// always a string scalar; the Object field is populated only at runtime —
// either by the CLI's `--body` JSON auto-detect or by reading a `.json`
// bodyFile — so that the device JSON can emit `bodyObject` for structured
// content. Object never round-trips through the yml: MarshalYAML serializes
// it back to a JSON-encoded scalar string.
//
// An empty BodyValue (String == "" && Object == nil) is a valid value that
// means "force the on-device response body to be empty". The distinction
// between "no override" and "force empty" is encoded at the *pointer* level
// — Response.Body == nil for no override.
type BodyValue struct {
	String string
	Object map[string]any
}

// UnmarshalYAML accepts only scalar bodies. The mapping form
// (`body:\n  message: failure`) was dropped because a JSON-encoded scalar
// (`body: '{"message":"failure"}'`) covers the same use case with one less
// way to express the same intent.
func (b *BodyValue) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		b.String = value.Value
		return nil
	case yaml.AliasNode:
		return b.UnmarshalYAML(value.Alias)
	case yaml.MappingNode:
		return fmt.Errorf("body must be a string scalar; the mapping form is no longer supported — write it as a JSON string instead (e.g. `body: '{\"message\":\"failure\"}'`) or use `bodyFile:` for larger payloads")
	default:
		return fmt.Errorf("body must be a string scalar, got yaml kind=%d", value.Kind)
	}
}

// MarshalYAML always emits a scalar. When Object is set (CLI JSON
// auto-detect / bodyFile.json), it is re-encoded to a JSON string so the yml
// stays the single canonical representation.
func (b BodyValue) MarshalYAML() (any, error) {
	if b.Object != nil {
		js, err := json.Marshal(b.Object)
		if err != nil {
			return nil, fmt.Errorf("body: marshal object as json: %w", err)
		}
		return string(js), nil
	}
	return b.String, nil
}

// FindRule returns a pointer to the rule with the given name (case-sensitive)
// or nil if not found. The returned pointer points into the slice so callers
// can mutate fields like Enabled in place.
func (rf *RulesFile) FindRule(name string) *Rule {
	for i := range rf.Rules {
		if rf.Rules[i].Name == name {
			return &rf.Rules[i]
		}
	}
	return nil
}

// AddRule appends a rule. Returns an error if a rule with the same name
// already exists (names are the user-facing handle, so duplicates would be
// ambiguous in `rules remove`).
func (rf *RulesFile) AddRule(r Rule) error {
	if rf.FindRule(r.Name) != nil {
		return fmt.Errorf("rule %q already exists", r.Name)
	}
	rf.Rules = append(rf.Rules, r)
	return nil
}

// RemoveRule deletes a rule by name. Returns an error if no such rule exists.
func (rf *RulesFile) RemoveRule(name string) error {
	for i, r := range rf.Rules {
		if r.Name == name {
			rf.Rules = append(rf.Rules[:i], rf.Rules[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("rule %q not found", name)
}
