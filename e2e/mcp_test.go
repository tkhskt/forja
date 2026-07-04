//go:build e2e

// MCP end-to-end coverage — the one path unit tests can't reach: the
// device-facing MCP tools (forja_apply / forja_sync / forja_off) actually
// attaching, pushing to a real device, and taking effect in the app.
//
// The MCP handlers deliberately bypass the CLI's print-heavy pushToApps and
// re-implement the push via pushEffective (see cli/cmd/mcp.go). That
// duplication is exactly where MCP behavior could silently diverge from the
// CLI, so we drive the real `forja mcp` server over its stdio transport and
// assert the same device outcomes the core CLI tests assert.
package e2e_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// forjaMCPSession launches `forja mcp` as a subprocess (cwd = repoRoot, so
// .forja/ resolves exactly like the CLI tests) and returns a connected client
// session. The server is torn down via t.Cleanup.
func forjaMCPSession(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()
	cmd := exec.Command(forjaBinary, "mcp")
	cmd.Dir = repoRoot
	client := mcp.NewClient(&mcp.Implementation{Name: "forja-e2e", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect forja mcp: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, ctx
}

// mcpCall invokes a tool and returns its concatenated text content, failing
// the test on a transport error or a tool-level error result.
func mcpCall(t *testing.T, cs *mcp.ClientSession, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("tool %s returned error: %+v", name, res.Content)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestMCPApplySyncOff mirrors TestCoreBasicRewrite + TestCoreOff, but drives
// the whole flow through the MCP server instead of the CLI: add a rule, apply
// it (attach + push), re-sync it, confirm the device sees the rewrite, then
// turn it off and confirm the app is back to baseline. This exercises
// applyHandler / syncHandler / offHandler and their shared pushEffective path
// against a real device.
func TestMCPApplySyncOff(t *testing.T) {
	resetForjaState(t, AppDev)
	forceStop(t, AppDev)
	startMainActivity(t, AppDev)
	clearLogcat(t)

	cs, ctx := forjaMCPSession(t)

	mcpCall(t, cs, ctx, "forja_rule_add", map[string]any{
		"name":   "mcp-teapot",
		"host":   "127.0.0.1",
		"path":   "/",
		"status": 418,
		"body":   `{"rewritten":true}`,
	})

	applyOut := mcpCall(t, cs, ctx, "forja_apply", map[string]any{
		"app":    AppDev,
		"enable": []string{"mcp-teapot"},
	})
	if !strings.Contains(applyOut, "pushed to device") {
		t.Errorf("forja_apply result unexpected: %q", applyOut)
	}

	// The auto-push during apply attaches the agent; wait for the stable
	// attach + self-destruct log lines just like the CLI core test does.
	waitForLogcat(t, "forja JVMTI agent attached", 30*time.Second, "ForjaAgent")
	waitForLogcat(t, "self-destruct mode enabled", 5*time.Second, "ForjaAgent")

	// status.json should reflect the enable via the MCP path.
	if st := readStatusJSON(t); !st.IsEnabled(AppDev, "mcp-teapot") {
		t.Errorf("status.json after forja_apply: mcp-teapot should be enabled on %s", AppDev)
	}

	// Device sees the rewrite: 418 + rewritten body.
	runInlineMaestro(t, `
appId: com.tkhskt.forja.sample
---
- tapOn:
    id: "fetch_singleton"
- extendedWaitUntil:
    visible:
      text: ".*HTTP 418.*"
    timeout: 30000
- assertVisible:
    text: ".*rewritten.*"
`)
	waitForLogcat(t, "hit 'mcp-teapot'", 10*time.Second, "Forja")

	// forja_sync re-pushes the effective set to the (still running) app.
	syncOut := mcpCall(t, cs, ctx, "forja_sync", map[string]any{"app": AppDev})
	if !strings.Contains(syncOut, "synced") {
		t.Errorf("forja_sync result unexpected: %q", syncOut)
	}

	// forja_off clears the device and empties the app's enabled list.
	offOut := mcpCall(t, cs, ctx, "forja_off", map[string]any{"app": AppDev})
	if !strings.Contains(offOut, "cleared rules") {
		t.Errorf("forja_off result unexpected: %q", offOut)
	}
	if st := readStatusJSON(t); st.IsEnabled(AppDev, "mcp-teapot") {
		t.Errorf("status.json after forja_off: mcp-teapot should be disabled on %s", AppDev)
	}

	// Back to baseline: tapping now returns 200 (no rewrite).
	clearLogcat(t)
	maestroFlow(t, "tap_singleton_assert_200.yaml")
}
