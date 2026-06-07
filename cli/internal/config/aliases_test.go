package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadAliasesMissingReturnsEmpty(t *testing.T) {
	a, err := LoadAliases(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if a == nil {
		t.Errorf("want non-nil empty map, got nil")
	}
	if len(a) != 0 {
		t.Errorf("want empty, got %v", a)
	}
}

func TestSaveLoadAliasesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "aliases.local.yml")
	orig := Aliases{
		"dev":     "com.tkhskt.forja.sample",
		"staging": "com.tkhskt.forja.sample.staging",
	}
	if err := SaveAliases(path, orig); err != nil {
		t.Fatalf("SaveAliases: %v", err)
	}
	back, err := LoadAliases(path)
	if err != nil {
		t.Fatalf("LoadAliases: %v", err)
	}
	if !reflect.DeepEqual(back, orig) {
		t.Errorf("round-trip mismatch: want %+v, got %+v", orig, back)
	}
}

func TestAliasesResolveUnknownPassThrough(t *testing.T) {
	a := Aliases{"dev": "com.tkhskt.forja.sample"}
	if got := a.Resolve("dev"); got != "com.tkhskt.forja.sample" {
		t.Errorf("alias not resolved: %q", got)
	}
	// Unknown input must pass through untouched so literal applicationIds keep working.
	if got := a.Resolve("com.something.else"); got != "com.something.else" {
		t.Errorf("unknown input should pass through: got %q", got)
	}
	// Empty input → empty (caller may use this as "no app" signal).
	if got := a.Resolve(""); got != "" {
		t.Errorf("empty should pass through: got %q", got)
	}
}

func TestAliasesSortedKeysAreStable(t *testing.T) {
	a := Aliases{"z": "x", "a": "y", "m": "z"}
	want := []string{"a", "m", "z"}
	got := a.SortedKeys()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SortedKeys: want %v, got %v", want, got)
	}
}

func TestAliasesForFindsMultiple(t *testing.T) {
	a := Aliases{
		"dev":  "com.tkhskt.forja.sample",
		"main": "com.tkhskt.forja.sample", // intentional duplicate target
		"qa":   "com.tkhskt.forja.sample.staging",
	}
	got := a.AliasesFor("com.tkhskt.forja.sample")
	want := []string{"dev", "main"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AliasesFor multi-target: want %v, got %v", want, got)
	}
	if got := a.AliasesFor("nonexistent"); len(got) != 0 {
		t.Errorf("AliasesFor unknown should be empty: %v", got)
	}
}

func TestLoadAliasesHandlesAliasesNilInFile(t *testing.T) {
	// File exists but the aliases: key is missing or empty. Should return
	// an empty map, not nil.
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.local.yml")
	if err := os.WriteFile(path, []byte("# empty file, no aliases\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAliases(path)
	if err != nil {
		t.Fatalf("LoadAliases: %v", err)
	}
	if a == nil {
		t.Errorf("want non-nil empty map")
	}
}
