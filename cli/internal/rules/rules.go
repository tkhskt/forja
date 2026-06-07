// Package rules is the engine layer that command handlers call into.
// It treats forja/rules.yml (project scope) and forja/rules.local.yml
// (local scope) as the rule definitions and forja/status.json as the
// per-app enabled state. Operations target one of:
//
//   - rule definitions (Add / Update / Remove): writes a yml file
//   - per-app enabled state (Enable / Disable / ClearApp): writes status.json
//   - either, by name lookup, when callers don't specify scope
//
// Splitting these surfaces keeps the cmd layer thin and prevents the TUI
// from accidentally rewriting shared yml when it just wants to flip a toggle.
package rules

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tkhskt/forja/internal/config"
)

// ErrNoFile is returned when an operation needs an existing rules file
// (e.g. remove) but none exists for the scope it was looking in.
var ErrNoFile = errors.New("rules file not found")

// Scope identifies which of the two rules-definition files we're operating on.
type Scope int

const (
	// ScopeProject corresponds to forja/rules.yml — shared, committed.
	ScopeProject Scope = iota
	// ScopeLocal corresponds to forja/rules.local.yml — per-developer, intended
	// to be gitignored (forja never edits .gitignore for you).
	ScopeLocal
)

// String returns the human-facing label ("project" / "local") so command
// handlers can use it in messages without a switch.
func (s Scope) String() string {
	switch s {
	case ScopeProject:
		return config.ScopeProject
	case ScopeLocal:
		return config.ScopeLocal
	}
	return "(?)"
}

// Paths bundles the on-disk locations we read/write. Tests can construct a
// Paths over t.TempDir() to avoid touching the real cwd.
type Paths struct {
	Project string // forja/rules.yml
	Local   string // forja/rules.local.yml
	Status  string // forja/status.json
	Aliases string // forja/aliases.local.yml
}

// DefaultPaths returns the paths relative to cwd (forja/rules.yml etc.).
func DefaultPaths() Paths {
	return Paths{
		Project: config.DefaultPath,
		Local:   config.DefaultLocalPath,
		Status:  config.DefaultStatusPath,
		Aliases: config.DefaultAliasesPath,
	}
}

// ResolveAlias takes a CLI-provided app value (could be a short alias or a
// literal applicationId) and returns the literal applicationId. Missing
// aliases pass through unchanged, so callers can always use the result as
// "the app" without special-casing.
func ResolveAlias(paths Paths, input string) (string, error) {
	if input == "" {
		return "", nil
	}
	a, err := config.LoadAliases(paths.Aliases)
	if err != nil {
		return "", err
	}
	return a.Resolve(input), nil
}

// LoadAliases is the convenience wrapper that callers use when they need the
// whole alias map (e.g. for `forja alias list` or the TUI picker annotation).
func LoadAliases(paths Paths) (config.Aliases, error) {
	return config.LoadAliases(paths.Aliases)
}

// SaveAliases writes the alias map back to disk.
func SaveAliases(paths Paths, a config.Aliases) error {
	return config.SaveAliases(paths.Aliases, a)
}

// pathFor returns the rule-file path corresponding to the scope.
func (p Paths) pathFor(s Scope) string {
	if s == ScopeLocal {
		return p.Local
	}
	return p.Project
}

// AddOptions is what a CLI add invocation provides. Empty fields are dropped
// from the saved yaml; Status==0 is treated as unset.
//
// Body and BodyFile are mutually exclusive — passing both is rejected by
// Add. The CLI layer enforces this at flag parsing too.
type AddOptions struct {
	Name     string
	Host     string
	Path     string
	Status   int
	Body     *config.BodyValue
	BodyFile string
}

// Add appends a rule to the file at the given scope. If the file is missing
// it's created. The added rule does NOT modify status.json — newly added
// rules are off for every app by default, and become live only when
// explicitly enabled via Enable() or the TUI.
func Add(paths Paths, scope Scope, opts AddOptions) error {
	if opts.Name == "" {
		return errors.New("rule name required")
	}
	if opts.Body != nil && !opts.Body.IsEmpty() && opts.BodyFile != "" {
		return errors.New("body and bodyFile are mutually exclusive")
	}
	path := paths.pathFor(scope)
	rf, err := config.Load(path)
	if err != nil {
		return err
	}
	if rf == nil {
		rf = &config.RulesFile{}
	}

	// Disallow same-name duplicates within the SAME scope. Cross-scope
	// (project ↔ user) shadowing is intentionally allowed (override pattern).
	if rf.FindRule(opts.Name) != nil {
		return fmt.Errorf("rule %q already exists in %s scope", opts.Name, scope)
	}

	r := config.Rule{
		Name: opts.Name,
		Match: config.Match{
			Host: opts.Host,
			Path: opts.Path,
		},
		Response: config.Response{
			Status:   opts.Status,
			Body:     opts.Body,
			BodyFile: opts.BodyFile,
		},
	}
	if err := rf.AddRule(r); err != nil {
		return err
	}
	return config.Save(path, rf)
}

// findRule looks the named rule up across both scopes. Returns the scope it
// was found in. User scope wins on collision (= override semantics).
func findRule(paths Paths, name string) (Scope, error) {
	if u, err := config.Load(paths.Local); err == nil && u != nil && u.FindRule(name) != nil {
		return ScopeLocal, nil
	}
	if p, err := config.Load(paths.Project); err == nil && p != nil && p.FindRule(name) != nil {
		return ScopeProject, nil
	}
	return ScopeProject, fmt.Errorf("rule %q not found in either scope", name)
}

// Remove deletes a rule by name from yml AND drops every status.json entry
// for that rule (so no orphan enable state lingers). If scope is specified,
// only that yml is inspected; otherwise the rule is searched across both
// (local-wins).
func Remove(paths Paths, name string, scope *Scope) error {
	var s Scope
	if scope != nil {
		s = *scope
	} else {
		found, err := findRule(paths, name)
		if err != nil {
			return err
		}
		s = found
	}
	path := paths.pathFor(s)
	rf, err := config.Load(path)
	if err != nil {
		return err
	}
	if rf == nil {
		return ErrNoFile
	}
	if err := rf.RemoveRule(name); err != nil {
		return err
	}
	if err := config.Save(path, rf); err != nil {
		return err
	}
	st, _ := config.LoadStatus(paths.Status)
	if st != nil {
		st.DropRule(name)
		_ = config.SaveStatus(paths.Status, st)
	}
	return nil
}

// UpdateOptions is the patch-semantics input to Update. Only non-nil fields
// are applied; everything else is left as-is on the existing rule.
//
// Body and BodyFile are mutually exclusive at the patch level too — setting
// one explicitly clears the other (= switching from inline body to file
// reference, or vice versa, requires only passing the new value).
type UpdateOptions struct {
	Host     *string
	Path     *string
	Status   *int
	Body     *config.BodyValue
	BodyFile *string
}

// Update patches an existing rule. If scope is nil, the rule is searched
// across both scopes (local-wins). If scope is given, only that scope is used.
// Errors if the rule doesn't exist in the targeted scope.
//
// Status.json is NOT touched — Update only edits the rule definition. Callers
// that want to propagate the new definition to live apps should consult
// status.AppsEnabling(name) and push to each.
func Update(paths Paths, name string, scope *Scope, opts UpdateOptions) error {
	var s Scope
	if scope != nil {
		s = *scope
	} else {
		found, err := findRule(paths, name)
		if err != nil {
			return err
		}
		s = found
	}
	path := paths.pathFor(s)
	rf, err := config.Load(path)
	if err != nil {
		return err
	}
	if rf == nil {
		return ErrNoFile
	}
	r := rf.FindRule(name)
	if r == nil {
		return fmt.Errorf("rule %q not found in %s scope", name, s)
	}
	if opts.Body != nil && opts.BodyFile != nil {
		return errors.New("body and bodyFile are mutually exclusive on update")
	}
	if opts.Host != nil {
		r.Match.Host = *opts.Host
	}
	if opts.Path != nil {
		r.Match.Path = *opts.Path
	}
	if opts.Status != nil {
		r.Response.Status = *opts.Status
	}
	if opts.Body != nil {
		r.Response.Body = opts.Body
		r.Response.BodyFile = "" // clear the file ref so they don't fight
	}
	if opts.BodyFile != nil {
		r.Response.BodyFile = *opts.BodyFile
		r.Response.Body = nil // clear the inline body
	}
	return config.Save(path, rf)
}

// Enable adds names to app's enabled list in status.json. Names that don't
// exist in any yml scope are rejected so typos surface immediately.
func Enable(paths Paths, app string, names []string) error {
	if app == "" {
		return errors.New("Enable requires a non-empty app")
	}
	if len(names) == 0 {
		return nil
	}
	known, err := loadKnownNames(paths)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, ok := known[n]; !ok {
			return fmt.Errorf("rule %q not found in either scope", n)
		}
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return err
	}
	for _, n := range names {
		st.Enable(app, n)
	}
	return config.SaveStatus(paths.Status, st)
}

// Disable removes names from app's enabled list. Unknown rule names are NOT
// rejected (you should be able to forcibly clean up stale entries).
func Disable(paths Paths, app string, names []string) error {
	if app == "" {
		return errors.New("Disable requires a non-empty app")
	}
	if len(names) == 0 {
		return nil
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return err
	}
	for _, n := range names {
		st.Disable(app, n)
	}
	return config.SaveStatus(paths.Status, st)
}

// ClearApp empties app's enabled list (= every rule off for this app) while
// keeping the app key. Mirrors what `forja off --app X` records locally.
func ClearApp(paths Paths, app string) error {
	if app == "" {
		return errors.New("ClearApp requires a non-empty app")
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return err
	}
	st.ClearApp(app)
	return config.SaveStatus(paths.Status, st)
}

// SetEnabledForApp overwrites app's enabled list with exactly the given
// names. Used by the TUI on save (when the user toggled rules within an
// app's view). Unknown rule names are NOT rejected — the caller may
// legitimately be storing a snapshot.
func SetEnabledForApp(paths Paths, app string, enabled []string) error {
	if app == "" {
		return errors.New("SetEnabledForApp requires a non-empty app")
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return err
	}
	st.ClearApp(app)
	for _, n := range enabled {
		st.Enable(app, n)
	}
	return config.SaveStatus(paths.Status, st)
}

// LoadEffective returns the merged effective rule list for app, ready to
// push. EffectiveRule.Enabled reflects app's status.json enabled list
// (absent = false).
func LoadEffective(paths Paths, app string) ([]config.EffectiveRule, error) {
	proj, err := config.Load(paths.Project)
	if err != nil {
		return nil, err
	}
	user, err := config.Load(paths.Local)
	if err != nil {
		return nil, err
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return nil, err
	}
	projectDir := filepath.Dir(paths.Project)
	localDir := filepath.Dir(paths.Local)
	return config.Effective(proj, projectDir, user, localDir, st, app), nil
}

// LoadStatus returns the current status (loading from disk). Convenience for
// callers that want to walk AppsEnabling without re-implementing the io step.
func LoadStatus(paths Paths) (config.Status, error) {
	return config.LoadStatus(paths.Status)
}

// loadKnownNames returns the union of all rule names across project + user yml.
// Used by Enable to typo-check before mutating status.json.
func loadKnownNames(paths Paths) (map[string]struct{}, error) {
	known := map[string]struct{}{}
	for _, p := range []string{paths.Project, paths.Local} {
		rf, err := config.Load(p)
		if err != nil {
			return nil, err
		}
		if rf == nil {
			continue
		}
		for _, r := range rf.Rules {
			known[r.Name] = struct{}{}
		}
	}
	return known, nil
}
