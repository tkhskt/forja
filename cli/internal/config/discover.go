package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Rule files are discovered by name anywhere under the .forja/ directory: a
// directory contributes rules via a file named rules.yml (project scope) and/or
// rules.local.yml (local scope). This lets rules be split into self-contained
// bundle directories (e.g. .forja/rules/payments/rules.yml + responses/) that
// can be shared by copying the directory, while the root .forja/rules.yml keeps
// working unchanged.
const (
	RuleFileName      = "rules.yml"
	RuleLocalFileName = "rules.local.yml"
	DefaultDir        = ".forja" // the .forja/ root, relative to cwd
)

// RuleSource is one discovered rules file: its parsed content plus the context
// needed to resolve (BaseDir for bodyFile) and address (Rel as the bundle
// qualifier) the rules it declares.
type RuleSource struct {
	Path  string     // path to the yml file
	Dir   string     // directory containing Path (= BaseDir for bodyFile resolution)
	Rel   string     // Dir relative to the forja root, slash-separated ("" for the root) — the bundle qualifier
	Scope string     // ScopeProject / ScopeLocal
	File  *RulesFile // parsed content (never nil for a discovered source)
}

// DiscoverRuleFiles walks forjaDir recursively and returns every rules.yml
// (project) and rules.local.yml (local) it finds, sorted deterministically:
// all local sources first, then all project, each group ordered by their
// directory's path relative to forjaDir. That ordering is what the on-device
// first-match semantics see (local wins, then bundles in path order).
//
// A missing forjaDir yields an empty slice with no error ("no rules yet" is a
// valid state). Per-file same-name duplicates are rejected by Load.
func DiscoverRuleFiles(forjaDir string) ([]RuleSource, error) {
	var sources []RuleSource
	walkErr := filepath.WalkDir(forjaDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		var scope string
		switch d.Name() {
		case RuleFileName:
			scope = ScopeProject
		case RuleLocalFileName:
			scope = ScopeLocal
		default:
			return nil
		}
		rf, lerr := Load(p)
		if lerr != nil {
			return lerr
		}
		if rf == nil {
			rf = &RulesFile{}
		}
		dir := filepath.Dir(p)
		rel, rerr := filepath.Rel(forjaDir, dir)
		if rerr != nil {
			rel = dir
		}
		if rel == "." {
			rel = ""
		}
		sources = append(sources, RuleSource{
			Path:  p,
			Dir:   dir,
			Rel:   filepath.ToSlash(rel),
			Scope: scope,
			File:  rf,
		})
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, walkErr
	}
	scopeRank := func(s string) int {
		if s == ScopeLocal {
			return 0
		}
		return 1
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if ri, rj := scopeRank(sources[i].Scope), scopeRank(sources[j].Scope); ri != rj {
			return ri < rj
		}
		return sources[i].Rel < sources[j].Rel
	})
	return sources, nil
}

// EffectiveFromSources flattens discovered sources into the merged effective
// rule list for app, in discovery order (local-first, then by bundle path).
// Each rule carries its source directory as BaseDir so relative bodyFile paths
// resolve per-bundle, and its Enabled flag from the per-app status list.
func EffectiveFromSources(sources []RuleSource, status Status, app string) []EffectiveRule {
	out := make([]EffectiveRule, 0)
	for _, src := range sources {
		for _, r := range src.File.Rules {
			handle := r.Name
			if src.Rel != "" {
				handle = src.Rel + "/" + r.Name
			}
			rr := r
			rr.Enabled = status.IsEnabled(app, handle)
			out = append(out, EffectiveRule{Rule: rr, Scope: src.Scope, BaseDir: src.Dir, Handle: handle})
		}
	}
	return out
}
