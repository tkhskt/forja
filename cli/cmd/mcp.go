package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/tkhskt/forja/internal/engine"
	"github.com/tkhskt/forja/internal/rules"
)

// newMcpCmd exposes forja over the Model Context Protocol so an MCP client
// (Claude Code/Desktop, etc.) can author rules and drive the device through
// natural language. It speaks the stdio transport, so the client launches it
// as `forja mcp` and talks newline-delimited JSON over stdin/stdout.
//
// IMPORTANT: stdout is the protocol channel. Tool handlers must never write to
// stdout — they return structured results instead. The interactive cobra
// commands print progress with fmt.Printf; the MCP handlers deliberately call
// the internal rules.*/engine.* functions (which are stdout-silent) directly
// and assemble a result string, rather than reusing the print-heavy command
// helpers like pushToApps.
func newMcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run forja as an MCP server (stdio) for AI clients",
		Long: `Start a Model Context Protocol server over stdio so an AI client can
create/edit rules and apply them to a device by calling tools.

Register it with your client, e.g. for Claude Code:

  claude mcp add forja -- forja mcp

The server operates on the .forja/ directory of the current working directory
unless a tool is given an explicit project_path argument.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := mcp.NewServer(&mcp.Implementation{Name: "forja", Version: versionString}, nil)
			registerTools(s)
			// A client disconnecting ends the session: stdin closes (EOF / the
			// SDK's "server is closing" terminal error) or the context is
			// cancelled. For a stdio server that's a normal shutdown, not a
			// forja error, so don't let Execute print it / exit non-zero. The
			// SDK's terminal error lives in an internal package, so match it by
			// cause and by message rather than a sentinel.
			err := s.Run(cmd.Context(), &mcp.StdioTransport{})
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			msg := err.Error()
			if strings.Contains(msg, "is closing") || strings.Contains(msg, "connection closed") {
				return nil
			}
			return err
		},
	}
}

// mcpMu serializes every tool invocation. forja operations are quick (yml
// edits, status.json writes, adb pushes) and conversation-driven, so running
// them one at a time costs nothing and removes whole classes of hazard: the
// per-call os.Chdir for project_path can't race, and concurrent status.json
// writes / adb sessions can't interleave.
var mcpMu sync.Mutex

// withProject runs fn under the lock, optionally with the working directory
// switched to projectPath for the duration (restored afterward). All of
// forja's path resolution (requireForjaDir, rules.DefaultPaths) is cwd-relative,
// so this is how a tool targets a specific project without threading a base
// dir through the whole internal API.
func withProject(projectPath string, fn func() error) error {
	mcpMu.Lock()
	defer mcpMu.Unlock()
	if projectPath != "" {
		orig, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(projectPath); err != nil {
			return fmt.Errorf("project_path %q: %w", projectPath, err)
		}
		defer func() { _ = os.Chdir(orig) }()
	}
	return fn()
}

func textResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

func registerTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_rules_list",
		Description: "List the rule catalog. With `app` set, also report whether each rule is enabled on that app.",
	}, listHandler)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_rule_add",
		Description: "Add a rewrite rule to the catalog. Writes the root .forja/rules.yml by default; pass `dir` to group it into a shareable bundle (.forja/<dir>/rules.yml), or `local` for the personal file. Does not push to any device.",
	}, addHandler)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_rule_update",
		Description: "Patch fields of an existing rule. Only the fields you pass change.",
	}, updateHandler)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_rule_remove",
		Description: "Delete a rule from the catalog.",
	}, removeHandler)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_apply",
		Description: "Enable/disable rules on a running app and push the effective set to the device.",
	}, applyHandler)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_off",
		Description: "Turn off every rewrite on an app so it sees real responses again (catalog untouched).",
	}, offHandler)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forja_sync",
		Description: "Re-push the current effective rule set to every enabled app (or one) after editing rules.",
	}, syncHandler)
}

// --- rules_list -------------------------------------------------------

type ListInput struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	App         string `json:"app,omitempty" jsonschema:"applicationId or alias; when set, each rule's enabled state for this app is reported"`
	Device      string `json:"device,omitempty" jsonschema:"device serial (from adb devices); needed with app only when several devices are connected"`
}

type RuleView struct {
	Handle      string `json:"handle"`
	Description string `json:"description,omitempty"`
	Host        string `json:"host,omitempty"`
	Path        string `json:"path,omitempty"`
	Status      int    `json:"status,omitempty"`
	HasBody     bool   `json:"has_body"`
	Scope       string `json:"scope"`
	Enabled     bool   `json:"enabled"`
}

type ListOutput struct {
	Rules []RuleView `json:"rules"`
}

func listHandler(_ context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, ListOutput, error) {
	var out ListOutput
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		app := in.App
		serial := ""
		if app != "" {
			if app, err = resolveApp(app); err != nil {
				return err
			}
			// Enabled state is per-device, so resolve one when reporting it.
			if serial, err = resolveDevice(in.Device); err != nil {
				return err
			}
		}
		eff, err := rules.LoadEffective(paths, serial, app)
		if err != nil {
			return err
		}
		for _, r := range eff {
			out.Rules = append(out.Rules, RuleView{
				Handle:      r.Handle,
				Description: r.Description,
				Host:        r.Match.Host,
				Path:        r.Match.Path,
				Status:      r.Response.Status,
				HasBody:     r.Response.Body != nil || r.Response.BodyFile != "",
				Scope:       r.Scope,
				Enabled:     r.Enabled,
			})
		}
		return nil
	})
	if err != nil {
		return nil, ListOutput{}, err
	}
	return nil, out, nil
}

// --- rule_add ---------------------------------------------------------

type AddInput struct {
	ProjectPath string            `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	Name        string            `json:"name" jsonschema:"unique rule name (its handle)"`
	Description string            `json:"description,omitempty" jsonschema:"the rule's intent/why, e.g. 'simulate login server outage'. Prefer intent over restating status/path."`
	Host        string            `json:"host,omitempty" jsonschema:"match: exact HTTP host"`
	Path        string            `json:"path,omitempty" jsonschema:"match: encoded-path substring, or a glob where each * matches one path segment (e.g. /users/*/posts)"`
	Status      int               `json:"status,omitempty" jsonschema:"rewrite: HTTP status code"`
	Body        string            `json:"body,omitempty" jsonschema:"rewrite: inline response body. A JSON-object string is sent structured; anything else is sent raw."`
	Headers     map[string]string `json:"headers,omitempty" jsonschema:"rewrite: response header overrides"`
	Local       bool              `json:"local,omitempty" jsonschema:"write to the local scope (.forja/rules.local.yml) instead of the shared project catalog"`
	Dir         string            `json:"dir,omitempty" jsonschema:"bundle directory under .forja/ (e.g. 'payments' or 'auth/checkout') to group this rule into a self-contained, shareable bundle at .forja/<dir>/rules.yml instead of the root catalog. Set this when the user asks to make the rule as a bundle, group it with related rules, or produce a reusable/shareable set — pick a short kebab-case name from the rule's domain if the user didn't name one. The rule's handle becomes <dir>/<name>. Composes with local."`
}

func addHandler(_ context.Context, _ *mcp.CallToolRequest, in AddInput) (*mcp.CallToolResult, any, error) {
	var msg string
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		opts := rules.AddOptions{
			Name:        in.Name,
			Description: in.Description,
			Host:        in.Host,
			Path:        in.Path,
			Status:      in.Status,
			Headers:     in.Headers,
			Dir:         in.Dir,
		}
		if in.Body != "" {
			body, err := parseBody(in.Body)
			if err != nil {
				return err
			}
			opts.Body = body
		}
		scope := rules.ScopeProject
		if in.Local {
			scope = rules.ScopeLocal
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		if err := rules.Add(paths, scope, opts); err != nil {
			return err
		}
		if in.Dir != "" {
			msg = fmt.Sprintf("added rule %q to %s bundle (%s scope)", in.Name, in.Dir, scope.String())
		} else {
			msg = fmt.Sprintf("added rule %q to %s scope", in.Name, scope.String())
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(msg), nil, nil
}

// --- rule_update ------------------------------------------------------

type UpdateInput struct {
	ProjectPath string             `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	Name        string             `json:"name" jsonschema:"name or handle of the rule to update"`
	Description *string            `json:"description,omitempty" jsonschema:"new intent note"`
	Host        *string            `json:"host,omitempty"`
	Path        *string            `json:"path,omitempty" jsonschema:"new match path; supports * glob (one segment)"`
	Status      *int               `json:"status,omitempty"`
	Body        *string            `json:"body,omitempty"`
	Headers     *map[string]string `json:"headers,omitempty" jsonschema:"replaces the entire header map; pass an empty object to clear"`
	Local       bool               `json:"local,omitempty" jsonschema:"restrict the search to the local scope"`
}

func updateHandler(_ context.Context, _ *mcp.CallToolRequest, in UpdateInput) (*mcp.CallToolResult, any, error) {
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		opts := rules.UpdateOptions{
			Description: in.Description,
			Host:        in.Host,
			Path:        in.Path,
			Status:      in.Status,
			Headers:     in.Headers,
		}
		if in.Body != nil {
			body, err := parseBody(*in.Body)
			if err != nil {
				return err
			}
			opts.Body = body
		}
		var scopePtr *rules.Scope
		if in.Local {
			s := rules.ScopeLocal
			scopePtr = &s
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		return rules.Update(paths, in.Name, scopePtr, opts)
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("updated rule %q", in.Name)), nil, nil
}

// --- rule_remove ------------------------------------------------------

type RemoveInput struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	Name        string `json:"name" jsonschema:"name or handle of the rule to remove"`
	Local       bool   `json:"local,omitempty" jsonschema:"restrict the search to the local scope"`
}

func removeHandler(_ context.Context, _ *mcp.CallToolRequest, in RemoveInput) (*mcp.CallToolResult, any, error) {
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		var scopePtr *rules.Scope
		if in.Local {
			s := rules.ScopeLocal
			scopePtr = &s
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		return rules.Remove(paths, in.Name, scopePtr)
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("removed rule %q", in.Name)), nil, nil
}

// --- apply ------------------------------------------------------------

type ApplyInput struct {
	ProjectPath string   `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	App         string   `json:"app" jsonschema:"target applicationId or alias (the app must be running on the device)"`
	Device      string   `json:"device,omitempty" jsonschema:"device serial (from adb devices); required only when several devices are connected"`
	Enable      []string `json:"enable,omitempty" jsonschema:"rule names/handles to enable on the app"`
	Disable     []string `json:"disable,omitempty" jsonschema:"rule names/handles to disable on the app"`
}

func applyHandler(_ context.Context, _ *mcp.CallToolRequest, in ApplyInput) (*mcp.CallToolResult, any, error) {
	var msg string
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		if in.App == "" {
			return errors.New("app is required")
		}
		if len(in.Enable) == 0 && len(in.Disable) == 0 {
			return errors.New("at least one of enable / disable is required")
		}
		app, err := resolveApp(in.App)
		if err != nil {
			return err
		}
		serial, err := resolveDevice(in.Device)
		if err != nil {
			return err
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		if len(in.Enable) > 0 {
			if err := rules.Enable(paths, serial, app, in.Enable); err != nil {
				return err
			}
		}
		if len(in.Disable) > 0 {
			if err := rules.Disable(paths, serial, app, in.Disable); err != nil {
				return err
			}
		}
		n, err := pushEffective(serial, app)
		if err != nil {
			return err
		}
		msg = fmt.Sprintf("applied to %s on %s: %d rule(s) enabled, pushed to device", app, serial, n)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(msg), nil, nil
}

// --- off --------------------------------------------------------------

type OffInput struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	App         string `json:"app" jsonschema:"target applicationId or alias"`
	Device      string `json:"device,omitempty" jsonschema:"device serial (from adb devices); required only when several devices are connected"`
}

func offHandler(_ context.Context, _ *mcp.CallToolRequest, in OffInput) (*mcp.CallToolResult, any, error) {
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		if in.App == "" {
			return errors.New("app is required")
		}
		app, err := resolveApp(in.App)
		if err != nil {
			return err
		}
		serial, err := resolveDevice(in.Device)
		if err != nil {
			return err
		}
		eng, err := engine.NewWithDevice(globals.BundleDir, serial)
		if err != nil {
			return err
		}
		if err := eng.Off(context.Background(), app); err != nil {
			return err
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		if err := rules.ClearApp(paths, serial, app); err != nil {
			return fmt.Errorf("update status.json: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("cleared rules on %s", in.App)), nil, nil
}

// --- sync -------------------------------------------------------------

type SyncInput struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"path to the project root containing .forja/ (defaults to the server's working directory)"`
	App         string `json:"app,omitempty" jsonschema:"limit the sync to this app (or alias); omit to sync every app with state on the device"`
	Device      string `json:"device,omitempty" jsonschema:"device serial (from adb devices); required only when several devices are connected"`
}

func syncHandler(_ context.Context, _ *mcp.CallToolRequest, in SyncInput) (*mcp.CallToolResult, any, error) {
	var msg string
	err := withProject(in.ProjectPath, func() error {
		if err := requireForjaDir(); err != nil {
			return err
		}
		serial, err := resolveDevice(in.Device)
		if err != nil {
			return err
		}
		paths, err := rulesPaths()
		if err != nil {
			return err
		}
		st, err := rules.LoadStatus(paths)
		if err != nil {
			return err
		}
		deviceSt := st[serial] // apps with state on the resolved device (may be nil)
		var targets []string
		if in.App != "" {
			app, err := resolveApp(in.App)
			if err != nil {
				return err
			}
			if _, ok := deviceSt[app]; !ok {
				return fmt.Errorf("no state for %s on %s — run forja_apply first", app, serial)
			}
			targets = []string{app}
		} else {
			for k := range deviceSt {
				targets = append(targets, k)
			}
			if len(targets) == 0 {
				return fmt.Errorf("no apps have state on %s — nothing to sync (use forja_apply first)", serial)
			}
			sort.Strings(targets)
		}

		var pushed, skipped []string
		for _, app := range targets {
			if _, err := pushEffective(serial, app); err != nil {
				if errors.Is(err, engine.ErrAppNotRunning) {
					skipped = append(skipped, app)
					continue
				}
				return err
			}
			pushed = append(pushed, app)
		}
		msg = fmt.Sprintf("synced %d app(s): %s", len(pushed), strings.Join(pushed, ", "))
		if len(skipped) > 0 {
			msg += fmt.Sprintf("; skipped %d not running: %s", len(skipped), strings.Join(skipped, ", "))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(msg), nil, nil
}

// pushEffective loads the effective rule set for app and pushes it to the
// device, returning how many rules ended up enabled. Stdout-silent (unlike the
// command-layer pushToApps), so it's safe to call from MCP handlers.
func pushEffective(serial, app string) (int, error) {
	eng, err := engine.NewWithDevice(globals.BundleDir, serial)
	if err != nil {
		return 0, err
	}
	paths, err := rulesPaths()
	if err != nil {
		return 0, err
	}
	eff, err := rules.LoadEffective(paths, serial, app)
	if err != nil {
		return 0, err
	}
	if err := eng.PushEffective(context.Background(), app, eff); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range eff {
		if r.Enabled {
			n++
		}
	}
	return n, nil
}
