// Package rules is the engine layer that command handlers call into.
// It treats .forja/rules.yml (project scope) and .forja/rules.local.yml
// (local scope) as the rule definitions and .forja/status.json as the
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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tkhskt/forja/internal/attach"
	"github.com/tkhskt/forja/internal/config"
)

// ErrNoFile is returned when an operation needs an existing rules file
// (e.g. remove) but none exists for the scope it was looking in.
var ErrNoFile = errors.New("rules file not found")

// Scope identifies which of the two rules-definition files we're operating on.
type Scope int

const (
	// ScopeProject corresponds to .forja/rules.yml — shared, committed.
	ScopeProject Scope = iota
	// ScopeLocal corresponds to .forja/rules.local.yml — per-developer, intended
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
	Project      string // .forja/rules.yml (the root project file + add() default target)
	Local        string // .forja/rules.local.yml (the root local file)
	Status       string // user-cache status.json (per-project, machine-managed) — NOT under .forja/
	Aliases      string // .forja/aliases.yml (project scope)
	AliasesLocal string // .forja/aliases.local.yml (local scope)
	Dir          string // .forja/ root — recursively discovered for rules.yml / rules.local.yml
}

// DefaultPaths returns the production Paths: authored files relative to cwd
// (.forja/rules.yml etc.) plus the per-project status file in the user cache.
// Computing the cache path can fail (no home dir, unreadable cwd), so this
// returns an error rather than papering over a broken environment. The first
// call also migrates a pre-cache .forja/status.json into the cache, so existing
// projects keep their enabled state without any manual step.
func DefaultPaths() (Paths, error) {
	status, err := defaultStatusPath()
	if err != nil {
		return Paths{}, err
	}
	migrateLegacyStatus(status)
	return Paths{
		Project:      config.DefaultPath,
		Local:        config.DefaultLocalPath,
		Status:       status,
		Aliases:      config.DefaultAliasesPath,
		AliasesLocal: config.DefaultLocalAliasesPath,
		Dir:          config.DefaultDir,
	}, nil
}

// defaultStatusPath returns the per-project status file under the user cache:
// <cache>/forja/status/<key>.json. status.json holds which rules are enabled
// for which app — machine-managed transient state — so it lives in the cache
// instead of polluting the authored .forja/ directory. The key namespaces the
// file by project root so two checkouts don't clobber each other's state.
func defaultStatusPath() (string, error) {
	cacheDir, err := attach.DefaultDir() // platform user cache (~/Library/Caches/forja, ~/.cache/forja, …)
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	return filepath.Join(cacheDir, "status", projectKey(abs)+".json"), nil
}

// projectKey derives a stable, filesystem-safe, per-project cache key from the
// project root's absolute path: a sanitized basename for human debuggability
// plus a short sha256 prefix of the full path for collision resistance (two
// sibling dirs with the same basename get distinct keys).
func projectKey(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	short := hex.EncodeToString(sum[:])[:12]
	base := sanitizeKeySegment(filepath.Base(abs))
	if base == "" {
		return short
	}
	return base + "-" + short
}

// sanitizeKeySegment keeps only characters that are safe and readable in a
// filename, collapsing everything else to '-'. The sha256 suffix guarantees
// uniqueness even when sanitization is lossy.
func sanitizeKeySegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// migrateLegacyStatus performs the one-time move of a pre-cache
// .forja/status.json into the cache. It's best-effort and idempotent: if the
// cache file already exists, or there's no legacy file, it does nothing. Errors
// are swallowed because status is transient (re-derivable by re-enabling rules)
// and a migration hiccup must never block an otherwise-valid command. The
// legacy file is removed only after the cache copy is written successfully.
func migrateLegacyStatus(cachePath string) {
	if _, err := os.Stat(cachePath); err == nil {
		return // cache already populated — nothing to migrate
	}
	st, err := config.LoadStatus(config.LegacyStatusPath)
	if err != nil || len(st) == 0 {
		return // no legacy state to carry over
	}
	if err := config.SaveStatus(cachePath, st); err != nil {
		return // leave the legacy file in place; try again next run
	}
	_ = os.Remove(config.LegacyStatusPath)
}

// aliasPathFor returns the alias file path corresponding to the scope, mirroring
// pathFor for rule files.
func (p Paths) aliasPathFor(s Scope) string {
	if s == ScopeLocal {
		return p.AliasesLocal
	}
	return p.Aliases
}

// ResolveAlias takes a CLI-provided app value (could be a short alias or a
// literal applicationId) and returns the literal applicationId. Missing
// aliases pass through unchanged, so callers can always use the result as
// "the app" without special-casing. Resolution uses the merged (project +
// local) map so an alias defined in either scope works.
func ResolveAlias(paths Paths, input string) (string, error) {
	if input == "" {
		return "", nil
	}
	a, err := LoadAliases(paths)
	if err != nil {
		return "", err
	}
	return a.Resolve(input), nil
}

// LoadAliases returns the merged alias map (project overlaid by local, so a
// personal alias wins over a same-named project alias). This is what callers
// use when they need "the effective aliases" — resolution, `forja alias list`,
// and the TUI picker annotation.
func LoadAliases(paths Paths) (config.Aliases, error) {
	project, err := config.LoadAliases(paths.Aliases)
	if err != nil {
		return nil, err
	}
	local, err := config.LoadAliases(paths.AliasesLocal)
	if err != nil {
		return nil, err
	}
	merged := config.Aliases{}
	for k, v := range project {
		merged[k] = v
	}
	for k, v := range local {
		merged[k] = v // local overrides project
	}
	return merged, nil
}

// LoadAliasesScope returns the alias map for a single scope's file (no merge).
// Used by `alias set` / `rm` / `list`, which operate on one file at a time.
func LoadAliasesScope(paths Paths, scope Scope) (config.Aliases, error) {
	return config.LoadAliases(paths.aliasPathFor(scope))
}

// SaveAliasesScope writes the alias map to a single scope's file.
func SaveAliasesScope(paths Paths, scope Scope, a config.Aliases) error {
	return config.SaveAliases(paths.aliasPathFor(scope), a)
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
// Add. The CLI layer enforces this at flag parsing too. Body == nil means
// "no body override"; an explicit empty body is &config.BodyValue{}.
//
// Headers, when non-empty, overrides headers on the matched response. The
// Content-Type entry also drives the response body's MIME type on the
// device side — so e.g. `Content-Type=text/html` + a string body lets the
// rule return HTML instead of the default JSON.
type AddOptions struct {
	Name        string
	Description string
	Host        string
	Path        string
	Status      int
	Body        *config.BodyValue
	BodyFile    string
	Headers     map[string]string
	// Dir, when non-empty, writes the rule into .forja/<Dir>/rules.yml (a
	// shareable bundle directory, created if absent) instead of the root
	// .forja/rules.yml. Must stay inside .forja/.
	Dir string
}

// ValidateRuleName rejects names that would clash with forja's CLI surface
// or otherwise behave surprisingly downstream. The accepted set is wide on
// purpose — UTF-8 letters, digits, whitespace, dashes, dots, etc. all work.
// The bans cover:
//
//   - empty / whitespace-only names (no identifier to reference later)
//   - comma — `forja apply --enable a,b` splits on comma, so a name containing
//     one cannot be enabled/disabled via the flag
//   - control characters (\n, \r, \t, NUL …) — would break yaml round-trip,
//     logcat output, or TUI rendering
func ValidateRuleName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("rule name required (cannot be empty or only whitespace)")
	}
	if strings.ContainsRune(name, ',') {
		return errors.New("rule name cannot contain ',' (the comma is used as a separator by --enable/--disable)")
	}
	if strings.ContainsRune(name, '/') {
		return errors.New("rule name cannot contain '/' (it's the bundle/name handle separator)")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("rule name cannot contain control characters (found U+%04X)", r)
		}
	}
	return nil
}

// Add appends a rule to the file at the given scope. If the file is missing
// it's created. The added rule does NOT modify status.json — newly added
// rules are off for every app by default, and become live only when
// explicitly enabled via Enable() or the TUI.
func Add(paths Paths, scope Scope, opts AddOptions) error {
	if err := ValidateRuleName(opts.Name); err != nil {
		return err
	}
	if opts.Body != nil && opts.BodyFile != "" {
		return errors.New("body and bodyFile are mutually exclusive")
	}
	// The new rule's fully-qualified handle must be unique. The same bare name
	// in a *different* bundle is fine (that's the point of bundles); only a
	// same-bundle/same-name collision is rejected. index() also surfaces any
	// pre-existing handle collision before we touch disk.
	refs, err := index(paths)
	if err != nil {
		return err
	}
	newHandle := handleFor(relForDir(opts.Dir), opts.Name)
	for _, r := range refs {
		if r.handle == newHandle {
			return fmt.Errorf("rule %q already exists at %s", opts.Name, newHandle)
		}
	}

	path, err := addTargetPath(paths, scope, opts.Dir)
	if err != nil {
		return err
	}
	rf, err := config.Load(path)
	if err != nil {
		return err
	}
	if rf == nil {
		rf = &config.RulesFile{}
	}

	r := config.Rule{
		Name:        opts.Name,
		Description: opts.Description,
		Match: config.Match{
			Host: opts.Host,
			Path: opts.Path,
		},
		Response: config.Response{
			Status:   opts.Status,
			Body:     opts.Body,
			BodyFile: opts.BodyFile,
			Headers:  opts.Headers,
		},
	}
	if err := rf.AddRule(r); err != nil {
		return err
	}
	return config.Save(path, rf)
}

// forjaDir returns the .forja/ root used for recursive discovery. Falls back to
// the directory of the root Project file so tests (which set only Project over
// a TempDir) keep working without setting Dir explicitly.
func (p Paths) forjaDir() string {
	if p.Dir != "" {
		return p.Dir
	}
	return filepath.Dir(p.Project)
}

// discover walks .forja/ for every rules.yml (project) / rules.local.yml (local)
// and returns them in deterministic, first-match order. The same bare name may
// repeat across different bundles, but each rule's fully-qualified handle
// (bundle path + name) must be unique. A handle collision — e.g. the same name
// in a single bundle's rules.yml and rules.local.yml — is reported with both
// declaring files.
func discover(paths Paths) ([]config.RuleSource, error) {
	sources, err := config.DiscoverRuleFiles(paths.forjaDir())
	if err != nil {
		return nil, err
	}
	owner := map[string]string{} // handle -> declaring file path
	for _, src := range sources {
		for _, r := range src.File.Rules {
			h := handleFor(src.Rel, r.Name)
			if prev, ok := owner[h]; ok {
				return nil, fmt.Errorf("two rules resolve to the same handle %q (in %s and %s) — rename one", h, prev, src.Path)
			}
			owner[h] = src.Path
		}
	}
	return sources, nil
}

// addTargetPath resolves which yml file `rules add` writes to: the root file
// for the scope by default, or .forja/<dir>/{rules.yml|rules.local.yml} when a
// bundle directory is requested. The bundle dir is created if absent and must
// stay inside .forja/.
func addTargetPath(paths Paths, scope Scope, dir string) (string, error) {
	name := config.RuleFileName
	if scope == ScopeLocal {
		name = config.RuleLocalFileName
	}
	if dir == "" {
		return paths.pathFor(scope), nil
	}
	clean := filepath.Clean(dir)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("--dir must be a path inside .forja/ (got %q)", dir)
	}
	targetDir := filepath.Join(paths.forjaDir(), clean)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("create bundle dir %s: %w", targetDir, err)
	}
	return filepath.Join(targetDir, name), nil
}

// handleFor builds a rule's fully-qualified handle: the bundle path (the rule
// file's directory relative to .forja/, slash-separated) plus the name. Rules in
// the root rules.yml / rules.local.yml have an empty bundle, so their handle is
// just the name — which keeps pre-bundle status.json entries valid without any
// migration.
func handleFor(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// relForDir mirrors the Rel that discovery assigns to a rule file written into
// the given --dir bundle ("" for the root).
func relForDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(dir))
}

// ruleRef is one discovered rule's addressing info.
type ruleRef struct {
	handle string
	name   string
	path   string // declaring file
	scope  string
}

// index returns every discovered rule with its handle. discover() already
// guarantees handle uniqueness, so handles form a unique key here.
func index(paths Paths) ([]ruleRef, error) {
	sources, err := discover(paths)
	if err != nil {
		return nil, err
	}
	var refs []ruleRef
	for _, src := range sources {
		for _, r := range src.File.Rules {
			refs = append(refs, ruleRef{
				handle: handleFor(src.Rel, r.Name),
				name:   r.Name,
				path:   src.Path,
				scope:  src.Scope,
			})
		}
	}
	return refs, nil
}

// resolveToken maps a user-supplied token to exactly one rule. A full-handle
// match wins; otherwise a bare name is accepted when it is unique across all
// files. An ambiguous bare name errors with the qualified candidates so the
// user can re-run with one. A non-nil scope restricts the search.
func resolveToken(refs []ruleRef, token string, scope *Scope) (ruleRef, error) {
	pool := refs
	if scope != nil {
		pool = nil
		for _, r := range refs {
			if r.scope == scope.String() {
				pool = append(pool, r)
			}
		}
	}
	for _, r := range pool {
		if r.handle == token {
			return r, nil
		}
	}
	var byName []ruleRef
	for _, r := range pool {
		if r.name == token {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return ruleRef{}, fmt.Errorf("rule %q not found", token)
	default:
		handles := make([]string, 0, len(byName))
		for _, m := range byName {
			handles = append(handles, m.handle)
		}
		sort.Strings(handles)
		return ruleRef{}, fmt.Errorf("rule name %q is ambiguous — qualify it as one of: %s", token, strings.Join(handles, ", "))
	}
}

// Remove deletes a rule by name from yml AND drops every status.json entry
// for that rule (so no orphan enable state lingers). If scope is specified,
// only that yml is inspected; otherwise the rule is searched across both
// (local-wins).
func Remove(paths Paths, name string, scope *Scope) error {
	refs, err := index(paths)
	if err != nil {
		return err
	}
	ref, err := resolveToken(refs, name, scope)
	if err != nil {
		return err
	}
	rf, err := config.Load(ref.path)
	if err != nil {
		return err
	}
	if rf == nil {
		return ErrNoFile
	}
	if err := rf.RemoveRule(ref.name); err != nil {
		return err
	}
	if err := config.Save(ref.path, rf); err != nil {
		return err
	}
	st, _ := config.LoadStatus(paths.Status)
	if st != nil {
		st.DropRule(ref.handle)
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
//
// Headers uses a pointer-to-map so the patch can distinguish three intents:
//   - nil           → leave headers as-is on the existing rule
//   - &(empty map)  → clear all headers
//   - &(non-empty)  → replace the entire headers map (no per-key merging)
type UpdateOptions struct {
	Description *string
	Host        *string
	Path        *string
	Status      *int
	Body        *config.BodyValue
	BodyFile    *string
	Headers     *map[string]string
}

// Update patches an existing rule. If scope is nil, the rule is searched
// across both scopes (local-wins). If scope is given, only that scope is used.
// Errors if the rule doesn't exist in the targeted scope.
//
// Status.json is NOT touched — Update only edits the rule definition. Callers
// that want to propagate the new definition to live apps should consult
// status.AppsEnabling(name) and push to each.
func Update(paths Paths, name string, scope *Scope, opts UpdateOptions) error {
	refs, err := index(paths)
	if err != nil {
		return err
	}
	ref, err := resolveToken(refs, name, scope)
	if err != nil {
		return err
	}
	rf, err := config.Load(ref.path)
	if err != nil {
		return err
	}
	if rf == nil {
		return ErrNoFile
	}
	r := rf.FindRule(ref.name)
	if r == nil {
		return fmt.Errorf("rule %q not found", name)
	}
	if opts.Body != nil && opts.BodyFile != nil {
		return errors.New("body and bodyFile are mutually exclusive on update")
	}
	if opts.Description != nil {
		r.Description = *opts.Description
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
	if opts.Headers != nil {
		if len(*opts.Headers) == 0 {
			r.Response.Headers = nil
		} else {
			r.Response.Headers = *opts.Headers
		}
	}
	return config.Save(ref.path, rf)
}

// Enable adds rules to app's enabled list in status.json, keyed by their
// fully-qualified handle. Each token is resolved (full handle, or unique bare
// name); an unknown name is rejected and an ambiguous bare name errors with
// the qualified candidates so typos and collisions surface immediately.
func Enable(paths Paths, app string, names []string) error {
	if app == "" {
		return errors.New("Enable requires a non-empty app")
	}
	if len(names) == 0 {
		return nil
	}
	refs, err := index(paths)
	if err != nil {
		return err
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return err
	}
	for _, n := range names {
		ref, rerr := resolveToken(refs, n, nil)
		if rerr != nil {
			return rerr
		}
		st.Enable(app, ref.handle)
	}
	return config.SaveStatus(paths.Status, st)
}

// Disable removes rules from app's enabled list. Tokens are resolved to their
// handle when possible; an unresolvable token is removed verbatim so stale
// entries can always be cleaned up (unknown names are NOT rejected).
func Disable(paths Paths, app string, names []string) error {
	if app == "" {
		return errors.New("Disable requires a non-empty app")
	}
	if len(names) == 0 {
		return nil
	}
	refs, _ := index(paths) // best-effort: still clean up even if discovery fails
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return err
	}
	for _, n := range names {
		key := n
		if ref, rerr := resolveToken(refs, n, nil); rerr == nil {
			key = ref.handle
		}
		st.Disable(app, key)
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
// push. EffectiveRule.Enabled reflects app's status.json enabled list (keyed
// by handle; absent = false). discover() rejects handle collisions so the
// returned list always has unique handles.
func LoadEffective(paths Paths, app string) ([]config.EffectiveRule, error) {
	sources, err := discover(paths)
	if err != nil {
		return nil, err
	}
	st, err := config.LoadStatus(paths.Status)
	if err != nil {
		return nil, err
	}
	return config.EffectiveFromSources(sources, st, app), nil
}

// LoadStatus returns the current status (loading from disk). Convenience for
// callers that want to walk AppsEnabling without re-implementing the io step.
func LoadStatus(paths Paths) (config.Status, error) {
	return config.LoadStatus(paths.Status)
}
