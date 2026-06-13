package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkhskt/forja/internal/config"
)

// isolateCache redirects the OS user-cache dir to a fresh tempdir so tests
// never touch the real ~/Library/Caches or ~/.cache. os.UserCacheDir derives
// from HOME on every supported platform (HOME/Library/Caches on macOS,
// XDG_CACHE_HOME or HOME/.cache on Linux), so pointing HOME at a tempdir and
// clearing XDG_CACHE_HOME isolates the cache uniformly. Returns the base the
// CLI will compute (== os.UserCacheDir() under the redirected HOME).
func isolateCache(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", t.TempDir())
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	return base
}

// TestDefaultPathsStatusLivesInCache: status.json resolves under the OS user
// cache (<cache>/forja/status/<key>.json), NOT under the authored .forja/
// directory. The authored paths stay relative to cwd as before.
func TestDefaultPathsStatusLivesInCache(t *testing.T) {
	base := isolateCache(t)
	t.Chdir(t.TempDir())

	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	wantPrefix := filepath.Join(base, "forja", "status") + string(filepath.Separator)
	if !strings.HasPrefix(p.Status, wantPrefix) || !strings.HasSuffix(p.Status, ".json") {
		t.Errorf("status path %q should be under %q and end in .json", p.Status, wantPrefix)
	}
	if strings.Contains(p.Status, config.DefaultDir+string(filepath.Separator)) {
		t.Errorf("status path %q must not live under %s/", p.Status, config.DefaultDir)
	}
	// Authored paths are unchanged.
	if p.Project != config.DefaultPath || p.Aliases != config.DefaultAliasesPath {
		t.Errorf("authored paths drifted: %+v", p)
	}
}

// TestProjectKeyStableAndDistinct: the same project root yields the same key
// across calls; sibling dirs with the same basename get distinct keys (the
// sha256 suffix disambiguates); the key is filesystem-safe.
func TestProjectKeyStableAndDistinct(t *testing.T) {
	a := projectKey("/home/dev/work/checkout-one/myapp")
	b := projectKey("/home/dev/work/checkout-two/myapp")
	if a != projectKey("/home/dev/work/checkout-one/myapp") {
		t.Error("projectKey is not stable for the same path")
	}
	if a == b {
		t.Errorf("same-basename siblings collided: %q == %q", a, b)
	}
	for _, k := range []string{a, b} {
		if !strings.HasPrefix(k, "myapp-") {
			t.Errorf("key %q should carry the debuggable basename prefix", k)
		}
		for _, r := range k {
			ok := r == '.' || r == '_' || r == '-' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				t.Errorf("key %q contains unsafe rune %q", k, r)
			}
		}
	}
}

// TestProjectKeyExoticBasename: a basename with no safe characters still
// produces a usable key (sanitized segment empties out → bare hash).
func TestProjectKeyExoticBasename(t *testing.T) {
	k := projectKey("/srv/日本語")
	if k == "" || strings.Contains(k, "/") {
		t.Errorf("exotic basename produced bad key %q", k)
	}
}

// TestMigrateLegacyStatusMovesAndIsIdempotent: a pre-cache .forja/status.json
// is migrated into the cache on the first DefaultPaths call, the legacy file is
// removed, and a second call is a no-op (doesn't resurrect or clobber).
func TestMigrateLegacyStatusMovesAndIsIdempotent(t *testing.T) {
	isolateCache(t)
	project := t.TempDir()
	t.Chdir(project)

	// Seed a legacy status file under .forja/ (the pre-cache location).
	if err := os.MkdirAll(config.DefaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := config.LegacyStatusPath
	if err := os.WriteFile(legacy, []byte(`{"com.example.app":{"enabled":["r1"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	// Legacy file is gone; cache file carries the state.
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy status.json should be removed after migration; stat err=%v", err)
	}
	st, err := config.LoadStatus(p.Status)
	if err != nil {
		t.Fatalf("load migrated status: %v", err)
	}
	if !st.IsEnabled("com.example.app", "r1") {
		t.Errorf("migrated status lost its enabled rule: %+v", st)
	}

	// Idempotent: a second resolve doesn't error and keeps the cache intact.
	if _, err := DefaultPaths(); err != nil {
		t.Fatalf("second DefaultPaths: %v", err)
	}
	st2, _ := config.LoadStatus(p.Status)
	if !st2.IsEnabled("com.example.app", "r1") {
		t.Errorf("second resolve clobbered the cache: %+v", st2)
	}
}

// TestMigrateSkippedWhenCacheExists: if the cache already has state, a stale
// legacy file is left untouched (cache wins; no surprise overwrite).
func TestMigrateSkippedWhenCacheExists(t *testing.T) {
	isolateCache(t)
	project := t.TempDir()
	t.Chdir(project)

	// Pre-populate the cache via a first resolve + save.
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatus(p.Status, config.Status{"app": {Enabled: []string{"cached"}}}); err != nil {
		t.Fatal(err)
	}
	// Now drop a legacy file that should be ignored.
	if err := os.MkdirAll(config.DefaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.LegacyStatusPath, []byte(`{"app":{"enabled":["legacy"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := DefaultPaths(); err != nil {
		t.Fatal(err)
	}
	st, _ := config.LoadStatus(p.Status)
	if st.IsEnabled("app", "legacy") || !st.IsEnabled("app", "cached") {
		t.Errorf("cache should win over a stale legacy file; got %+v", st)
	}
}
