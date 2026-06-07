// Package engine wires the ADB, attach-state cache, and bundle layout into
// the high-level operations command handlers actually want: "make sure the
// agent is attached" and "push these rules right now".
//
// The engine is the only layer that knows about both the device and the
// filesystem at once, so it's also the home for bundle path resolution and
// orchestration sequencing (attach before push, etc.).
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tkhskt/forja/internal/adb"
	"github.com/tkhskt/forja/internal/attach"
	"github.com/tkhskt/forja/internal/config"
)

// RemoteRulesRel is the path under /data/data/<app>/ where the runtime expects
// to find rules.json. The agent reads this on attach and then deletes it.
const RemoteRulesRel = "files/rules.json"

// ErrAppNotRunning is returned by EnsureAttached (and surfaces from Push /
// PushEffective / Off) when the target app's process is absent from the
// device. Callers that iterate multiple apps (e.g. rules.Update's
// auto-propagation) use errors.Is(err, ErrAppNotRunning) to warn-and-continue
// rather than abort the whole operation.
var ErrAppNotRunning = errors.New("app not running on device")

// AgentSoName and AgentDexName are the filenames the bundle ships.
const (
	AgentSoName  = "libforja-agent.so" // pushed name on device (ABI suffix dropped)
	AgentDexName = "agent-bundle.dex"
)

// Engine holds dependencies. Construct with New for production wiring or by
// hand for tests that want to swap a fake ADB.
type Engine struct {
	ADB    *adb.ADB
	Cache  *attach.Cache
	Bundle string // local directory containing libforja-agent-<abi>.so + agent-bundle.dex
	// Now is injectable to make ShouldReattach tests deterministic. Production
	// callers can leave it nil and time.Now is used.
	Now func() time.Time
}

// New constructs an Engine with the default ADB and cache directory.
// bundleDir defaults to the first matching path returned by ResolveBundleDir.
func New(bundleDir string) (*Engine, error) {
	if bundleDir == "" {
		resolved, err := ResolveBundleDir()
		if err != nil {
			return nil, err
		}
		bundleDir = resolved
	}
	cacheDir, err := attach.DefaultDir()
	if err != nil {
		return nil, err
	}
	return &Engine{
		ADB:    adb.New(),
		Cache:  attach.NewCache(cacheDir),
		Bundle: bundleDir,
	}, nil
}

// ResolveBundleDir walks the list of standard locations and returns the
// first one that actually exists. Search order:
//
//  1. $FORJA_BUNDLE_DIR
//  2. $XDG_DATA_HOME/forja/agent
//  3. $HOME/.local/share/forja/agent
//  4. /usr/local/share/forja/agent
//  5. ./jvmti-agent/build/outputs/agent  (repo-local development fallback)
//
// If none exist, returns an error listing the locations tried so the user
// can decide whether to install, set FORJA_BUNDLE_DIR, or build from source.
func ResolveBundleDir() (string, error) {
	candidates := []string{}
	if v := os.Getenv("FORJA_BUNDLE_DIR"); v != "" {
		candidates = append(candidates, v)
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		candidates = append(candidates, filepath.Join(v, "forja", "agent"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "forja", "agent"))
	}
	candidates = append(candidates,
		"/usr/local/share/forja/agent",
		filepath.Join("jvmti-agent", "build", "outputs", "agent"),
	)
	for _, c := range candidates {
		if stat, err := os.Stat(c); err == nil && stat.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf(
		"forja agent bundle not found in any of:\n  %s\n\nInstall forja "+
			"(see https://github.com/tkhskt/forja#install), set FORJA_BUNDLE_DIR, "+
			"or pass --bundle DIR.",
		strings.Join(candidates, "\n  "),
	)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// EnsureAttached verifies the JVMTI agent is live in the named process,
// re-attaching if PID changed or the cache is stale. It is intended to be
// idempotent: calling it twice in quick succession does not double-attach.
//
// Returns an error if the app isn't running — callers should surface that
// to the user with the hint "launch the app first".
func (e *Engine) EnsureAttached(ctx context.Context, app string) error {
	pid, err := e.ADB.Pidof(ctx, app)
	if err != nil {
		return fmt.Errorf("pidof %s: %w", app, err)
	}
	if pid == 0 {
		return fmt.Errorf("%s: %w — launch the app first", app, ErrAppNotRunning)
	}

	cached, _ := e.Cache.Load(app) // load errors are non-fatal; treat as no cache
	decision := attach.ShouldReattach(cached, pid, e.now(), attach.DefaultTTL)
	if !decision.Reattach {
		return nil
	}

	if err := e.attach(ctx, app); err != nil {
		return err
	}
	if err := e.Cache.Save(app, pid, e.now()); err != nil {
		// Saving cache failure is non-fatal: worst case we re-attach next time.
		// Surface as a warning, not an error.
		fmt.Fprintf(os.Stderr, "[warn] could not save attach cache: %v\n", err)
	}
	return nil
}

// attach copies the agent .so/.dex to the app's data dir and invokes the
// JVMTI attach. Each call is independent — no caching here.
func (e *Engine) attach(ctx context.Context, app string) error {
	abi, err := e.ADB.PrimaryABI(ctx)
	if err != nil {
		return err
	}
	soLocal := filepath.Join(e.Bundle, fmt.Sprintf("libforja-agent-%s.so", abi))
	dexLocal := filepath.Join(e.Bundle, AgentDexName)
	soData, err := os.ReadFile(soLocal)
	if err != nil {
		return fmt.Errorf("read agent .so: %w (run `gradle :jvmti-agent:bundleAgentDex` first?)", err)
	}
	dexData, err := os.ReadFile(dexLocal)
	if err != nil {
		return fmt.Errorf("read agent .dex: %w", err)
	}
	remoteSoRel := "files/" + AgentSoName
	remoteDexRel := "files/" + AgentDexName
	if err := e.ADB.RunAsWrite(ctx, app, remoteSoRel, soData); err != nil {
		return fmt.Errorf("push .so: %w", err)
	}
	if err := e.ADB.RunAsWrite(ctx, app, remoteDexRel, dexData); err != nil {
		return fmt.Errorf("push .dex: %w", err)
	}
	remoteSoAbs := "/data/data/" + app + "/" + remoteSoRel
	remoteDexAbs := "/data/data/" + app + "/" + remoteDexRel
	if err := e.ADB.AttachAgent(ctx, app, remoteSoAbs, remoteDexAbs); err != nil {
		return err
	}
	return nil
}

// Push performs the full deploy of a RulesFile to the named app: ensures
// the agent is live, converts to device JSON, writes /data/data/<app>/files/
// rules.json. The agent reads that file at the next opportunity and deletes
// it on disk, so this function is effectively "fire and forget" from the CLI
// side.
func (e *Engine) Push(ctx context.Context, app string, rf *config.RulesFile) error {
	if app == "" {
		return errors.New("Push requires a non-empty app")
	}
	if err := e.EnsureAttached(ctx, app); err != nil {
		return err
	}
	js, err := rf.ToDeviceJSON()
	if err != nil {
		return err
	}
	if err := e.ADB.RunAsWrite(ctx, app, RemoteRulesRel, js); err != nil {
		return err
	}
	// Record that "push" was the last action so QueryAttachStatus can tell
	// the TUI that rules are effective (as opposed to being cleared by off).
	_ = e.Cache.RecordAction(app, attach.ActionPush, e.now())
	return nil
}

// PushEffective is the project/user/status-aware variant of Push. It takes the
// already-merged effective rule list from rules.LoadEffective rather than a
// raw RulesFile, so the engine doesn't need to know about scopes.
func (e *Engine) PushEffective(ctx context.Context, app string, eff []config.EffectiveRule) error {
	if app == "" {
		return errors.New("PushEffective requires a non-empty app")
	}
	if err := e.EnsureAttached(ctx, app); err != nil {
		return err
	}
	js, err := config.EffectiveToDeviceJSON(eff)
	if err != nil {
		return err
	}
	if err := e.ADB.RunAsWrite(ctx, app, RemoteRulesRel, js); err != nil {
		return err
	}
	_ = e.Cache.RecordAction(app, attach.ActionPush, e.now())
	return nil
}

// AttachStatusKind enumerates how forja sees the current attach situation
// for a given app. Used by the TUI to render the device status line.
type AttachStatusKind int

const (
	StatusAppNotRunning   AttachStatusKind = iota // app process is absent
	StatusNoAttachRecord                          // app running but forja never attached here
	StatusAgentStale                              // cached PID differs from current → agent dead
	StatusAgentLive                               // cache matches current PID + last action = push
	StatusAgentLiveButOff                         // cache matches current PID but last action was `forja off`
	StatusUnknown                                 // couldn't reach adb (device offline etc.)
)

// AttachStatus is what the TUI reads. forja is intentionally pessimistic:
// even StatusAgentLive only means "the CLI's last attach matches the running
// process" — the agent itself could in theory have crashed without restarting
// the process. The TUI phrases this as "should be effective".
type AttachStatus struct {
	Kind       AttachStatusKind
	CurrentPid int
	CachedPid  int
	Err        error
}

// Message returns a one-line summary suitable for the TUI header.
func (s AttachStatus) Message() string {
	switch s.Kind {
	case StatusAppNotRunning:
		return "app not running on device — start the app and press q to attach"
	case StatusNoAttachRecord:
		return fmt.Sprintf("app running (pid %d) but forja never attached — press q to attach",
			s.CurrentPid)
	case StatusAgentStale:
		return fmt.Sprintf("app restarted since last attach (was pid %d, now %d) — press q to re-attach",
			s.CachedPid, s.CurrentPid)
	case StatusAgentLive:
		return fmt.Sprintf("agent live (pid %d) — rules below are effective", s.CurrentPid)
	case StatusAgentLiveButOff:
		return fmt.Sprintf("agent live (pid %d) but rules were cleared via `forja off` — press q to re-push",
			s.CurrentPid)
	case StatusUnknown:
		if s.Err != nil {
			return fmt.Sprintf("could not read device state: %v", s.Err)
		}
		return "could not read device state"
	}
	return ""
}

// Live reports whether the rules below the status line are *currently
// effective* on the device. This is the source of truth for whether the TUI
// should dim them — `forja off` makes Live() false even though the agent
// process itself is alive.
func (s AttachStatus) Live() bool { return s.Kind == StatusAgentLive }

// QueryAttachStatus does the PID baseline comparison without performing any
// attach. Read-only and fast (just `adb shell pidof`), so it's safe to call
// on every TUI open.
func (e *Engine) QueryAttachStatus(ctx context.Context, app string) AttachStatus {
	pid, err := e.ADB.Pidof(ctx, app)
	if err != nil {
		return AttachStatus{Kind: StatusUnknown, Err: err}
	}
	if pid == 0 {
		return AttachStatus{Kind: StatusAppNotRunning}
	}
	cached, _ := e.Cache.Load(app)
	if cached == nil {
		return AttachStatus{Kind: StatusNoAttachRecord, CurrentPid: pid}
	}
	if cached.Pid != pid {
		return AttachStatus{Kind: StatusAgentStale, CurrentPid: pid, CachedPid: cached.Pid}
	}
	// PID matches → agent should be live in this process. Whether rules are
	// effective depends on the last action: `forja off` cleared them even
	// though the agent itself is still attached.
	if cached.LastAction == attach.ActionOff {
		return AttachStatus{Kind: StatusAgentLiveButOff, CurrentPid: pid, CachedPid: cached.Pid}
	}
	return AttachStatus{Kind: StatusAgentLive, CurrentPid: pid, CachedPid: cached.Pid}
}

// Off writes an empty rule array to the device, effectively pausing all
// rewrites without removing local rules.yml.
func (e *Engine) Off(ctx context.Context, app string) error {
	if app == "" {
		return errors.New("Off requires a non-empty app")
	}
	if err := e.EnsureAttached(ctx, app); err != nil {
		return err
	}
	if err := e.ADB.RunAsWrite(ctx, app, RemoteRulesRel, []byte("[]\n")); err != nil {
		return err
	}
	_ = e.Cache.RecordAction(app, attach.ActionOff, e.now())
	return nil
}
