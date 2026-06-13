// aliases.go owns the on-disk shape of the alias map — short alias names to
// fully-qualified Android applicationIds. Like rules, aliases come in two
// scopes: project (.forja/aliases.yml, committed, shared by the team) and local
// (.forja/aliases.local.yml, gitignored, personal). The two are merged at
// resolve time with local entries overriding project. Anywhere a forja CLI flag
// accepts an `--app`, the value is first passed through the merged map; missing
// entries fall through to literal applicationIds so unaliased usage stays
// unchanged.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Alias file locations, relative to cwd. Siblings to the rules files and
// mirroring their project/local scope split.
const (
	DefaultAliasesPath      = DefaultDir + "/aliases.yml"       // project scope (you should commit it)
	DefaultLocalAliasesPath = DefaultDir + "/aliases.local.yml" // local scope (you should gitignore it)
)

// Aliases is the parsed alias map. Keys are short names ("dev", "staging");
// values are the full Android applicationIds they expand to.
//
// On-disk shape is wrapped in an `aliases:` key so the file is
// self-documenting and we can add per-entry metadata later without breaking
// existing files:
//
//	# .forja/aliases.yml (or aliases.local.yml)
//	aliases:
//	  dev: com.tkhskt.forja.sample
//	  staging: com.tkhskt.forja.sample.staging
type Aliases map[string]string

type aliasesFile struct {
	Aliases Aliases `yaml:"aliases"`
}

// Resolve returns the applicationId for the given input. If input matches an
// alias key the mapped value is returned; otherwise input is returned
// unchanged. This lets the resolver be inserted anywhere `--app` is read
// without forcing the caller to care whether the user typed an alias.
func (a Aliases) Resolve(input string) string {
	if app, ok := a[input]; ok {
		return app
	}
	return input
}

// SortedKeys returns alias names in lexicographic order. Used for `forja
// alias list` and the TUI picker's annotation lookup so output is stable.
func (a Aliases) SortedKeys() []string {
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AliasesFor returns the alias names that point at app. Multiple aliases for
// the same app is allowed (e.g. both `dev` and `main`).
func (a Aliases) AliasesFor(app string) []string {
	out := make([]string, 0)
	for k, v := range a {
		if v == app {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// LoadAliases reads the alias file from disk. A missing file yields an empty
// map (not nil) so callers can call Resolve unconditionally.
func LoadAliases(path string) (Aliases, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Aliases{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f aliasesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Aliases == nil {
		return Aliases{}, nil
	}
	return f.Aliases, nil
}

// SaveAliases writes the alias map to disk in the wrapped `aliases:` form.
// The parent directory must already exist — see the Save() comment in io.go
// for the directory-creation contract.
func SaveAliases(path string, a Aliases) error {
	if a == nil {
		a = Aliases{}
	}
	out, err := yaml.Marshal(aliasesFile{Aliases: a})
	if err != nil {
		return fmt.Errorf("marshal aliases: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
