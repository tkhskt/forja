// Package attach maintains forja's lightweight notion of "have we already
// pushed the JVMTI agent into this process?" without daemonizing.
//
// Approach: keep a per-app cache file in ~/.cache/forja/<app>.json that
// records the PID and timestamp of the last successful attach. Before each
// push, compare against `pidof <app>` — if the PID changed, the app process
// was restarted and the agent went with it, so we re-attach. If the PID
// matches but the timestamp is older than TTL, re-attach defensively
// (covers cases like the agent crashing but the process surviving).
package attach

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State is what we persist per-app. AttachedAt is the timestamp of the last
// actual JVMTI attach. LastAction reflects what forja most recently did to
// the device's rule state — distinct from attach because `forja off` updates
// rules without re-attaching, and we want the TUI to show that.
type State struct {
	App          string `json:"app"`
	Pid          int    `json:"pid"`
	AttachedAt   int64  `json:"attached_at"`              // Unix seconds
	LastAction   string `json:"last_action,omitempty"`    // one of the Action* constants
	LastActionAt int64  `json:"last_action_at,omitempty"` // Unix seconds
}

// Action constants used in State.LastAction. Exported so the engine and TUI
// can compare against them without stringly-typed bugs.
const (
	ActionPush = "push"
	ActionOff  = "off"
)

// DefaultTTL is the maximum age of a cached attach before we re-attach
// defensively. Tuned to be long enough that consecutive `forja rules`
// invocations don't pay the attach cost, but short enough that a hung agent
// gets refreshed within a coffee break.
const DefaultTTL = 12 * time.Hour

// Cache encapsulates the per-app state directory. New instances are cheap;
// a Cache is keyed only on the base directory it manages.
type Cache struct {
	dir string
}

// NewCache returns a Cache rooted at the given directory. The directory is
// created lazily on first Save.
func NewCache(dir string) *Cache { return &Cache{dir: dir} }

// DefaultDir returns ~/.cache/forja, honoring XDG_CACHE_HOME if set. Used by
// production callers; tests should construct a Cache with a t.TempDir().
func DefaultDir() (string, error) {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "forja"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "forja"), nil
}

// path returns the state-file path for a given app. applicationIds contain
// dots, which are fine in filenames on all supported platforms; we just
// append ".json" to namespace alongside any other files we might keep here
// later.
func (c *Cache) path(app string) string {
	return filepath.Join(c.dir, app+".json")
}

// Load returns the cached state for app, or nil if no record exists. A
// corrupt or partially-written file returns (nil, err) so callers can
// distinguish "no state" from "I/O problem".
func (c *Cache) Load(app string) (*State, error) {
	data, err := os.ReadFile(c.path(app))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read attach cache: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse attach cache: %w", err)
	}
	return &s, nil
}

// Save writes the state. It creates the cache directory if missing. The write
// is rename-on-close: write to a tmp file then rename, to avoid leaving a
// truncated file if the process is interrupted mid-write.
//
// Convenience wrapper: takes pid + now and zeros LastAction. For a full update
// (including LastAction), use SaveState.
func (c *Cache) Save(app string, pid int, now time.Time) error {
	if app == "" {
		return errors.New("empty app")
	}
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	return c.SaveState(&State{App: app, Pid: pid, AttachedAt: now.Unix()})
}

// SaveState writes the full State to disk. Used by callers that need to set
// LastAction (forja push / forja off) in addition to the attach fields.
func (c *Cache) SaveState(s *State) error {
	if s == nil || s.App == "" {
		return errors.New("empty state")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := c.path(s.App) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, c.path(s.App)); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// RecordAction updates only the LastAction / LastActionAt fields, preserving
// the rest of the state. Used by Push/Off after a successful operation. If no
// prior state exists, this is a no-op (best-effort: the operation succeeded,
// we just can't record metadata about it).
func (c *Cache) RecordAction(app, action string, now time.Time) error {
	s, err := c.Load(app)
	if err != nil || s == nil {
		return err // err is nil when state is missing
	}
	s.LastAction = action
	s.LastActionAt = now.Unix()
	return c.SaveState(s)
}

// Clear removes the cached state for app. Missing files are not an error.
func (c *Cache) Clear(app string) error {
	err := os.Remove(c.path(app))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear: %w", err)
	}
	return nil
}

// Decision is what ShouldReattach returns, with a human-readable reason for
// the chosen action (used in -v output / logging so users can see why a slow
// attach was triggered).
type Decision struct {
	Reattach bool
	Reason   string
}

// ShouldReattach decides whether to invoke the JVMTI attach again given the
// cached state and the currently-observed PID. currentPid==0 means the app
// is not running, which the caller must treat specially (= error, don't push).
//
// The decision tree:
//   - no cached state                → first contact, attach
//   - currentPid != cached.Pid       → process restarted, attach
//   - cached older than ttl          → defensive refresh, attach
//   - otherwise                      → still good, skip attach
func ShouldReattach(cached *State, currentPid int, now time.Time, ttl time.Duration) Decision {
	if currentPid == 0 {
		return Decision{Reattach: false, Reason: "process not running"}
	}
	if cached == nil {
		return Decision{Reattach: true, Reason: "no prior attach recorded"}
	}
	if cached.Pid != currentPid {
		return Decision{
			Reattach: true,
			Reason:   fmt.Sprintf("pid changed: cached=%d current=%d", cached.Pid, currentPid),
		}
	}
	age := now.Sub(time.Unix(cached.AttachedAt, 0))
	if age > ttl {
		return Decision{
			Reattach: true,
			Reason:   fmt.Sprintf("cached attach older than ttl (%s > %s)", trunc(age), ttl),
		}
	}
	return Decision{Reattach: false, Reason: fmt.Sprintf("attach is %s old, fresh", trunc(age))}
}

func trunc(d time.Duration) string {
	if d > time.Hour {
		return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
	}
	return d.Truncate(time.Second).String()
}
