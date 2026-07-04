package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPRuleAddListDescription drives the MCP handlers directly (no protocol
// round-trip) against a temp project: add a rule with a description, then list
// it back and confirm the description + wildcard path survive into the yml and
// the structured list output.
func TestMCPRuleAddListDescription(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".forja"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, _, err := addHandler(ctx, nil, AddInput{
		ProjectPath: tmp,
		Name:        "mock-login",
		Description: "simulate login server outage",
		Host:        "example.com",
		Path:        "/users/*/posts",
		Status:      418,
		Body:        `{"ok":true}`,
	}); err != nil {
		t.Fatalf("addHandler: %v", err)
	}

	_, out, err := listHandler(ctx, nil, ListInput{ProjectPath: tmp})
	if err != nil {
		t.Fatalf("listHandler: %v", err)
	}
	if len(out.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(out.Rules))
	}
	r := out.Rules[0]
	if r.Description != "simulate login server outage" {
		t.Errorf("description = %q", r.Description)
	}
	if r.Path != "/users/*/posts" {
		t.Errorf("path = %q", r.Path)
	}
	if r.Status != 418 || !r.HasBody {
		t.Errorf("status/body = %d/%v", r.Status, r.HasBody)
	}

	yml, err := os.ReadFile(filepath.Join(tmp, ".forja", "rules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yml), "description: simulate login server outage") {
		t.Errorf("rules.yml missing description:\n%s", yml)
	}
}

// TestMCPServerProtocol exercises the full MCP protocol path (initialize →
// tools/list → tools/call) over an in-memory transport pair, so the schema
// inference, tool dispatch, and result wiring are all covered — not just the
// handler functions.
func TestMCPServerProtocol(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".forja"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	clientT, serverT := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "forja", Version: "test"}, nil)
	registerTools(server)
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect/initialize: %v", err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range tools.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"forja_rules_list", "forja_rule_add", "forja_apply", "forja_off"} {
		if !got[want] {
			t.Errorf("tool %q not advertised", want)
		}
	}

	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "forja_rule_add",
		Arguments: map[string]any{
			"project_path": tmp,
			"name":         "proto",
			"description":  "added over the wire",
			"host":         "e.com",
			"path":         "/users/*/posts",
			"status":       418,
		},
	}); err != nil {
		t.Fatalf("call forja_rule_add: %v", err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "forja_rules_list",
		Arguments: map[string]any{"project_path": tmp},
	})
	if err != nil {
		t.Fatalf("call forja_rules_list: %v", err)
	}
	if res.IsError {
		t.Fatalf("rules_list returned tool error: %+v", res.Content)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if !strings.Contains(text.String(), "added over the wire") || !strings.Contains(text.String(), "/users/*/posts") {
		t.Errorf("list result missing rule data:\n%s", text.String())
	}
}

// --- shared test helpers ----------------------------------------------

// tempProject makes a temp dir with an empty .forja/ so the requireForjaDir
// gate passes, and returns its path for use as a project_path argument.
func tempProject(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".forja"), 0o755); err != nil {
		t.Fatal(err)
	}
	return tmp
}

func mustAdd(t *testing.T, tmp string, in AddInput) {
	t.Helper()
	in.ProjectPath = tmp
	if _, _, err := addHandler(context.Background(), nil, in); err != nil {
		t.Fatalf("addHandler(%s): %v", in.Name, err)
	}
}

func listRules(t *testing.T, tmp string) []RuleView {
	t.Helper()
	_, out, err := listHandler(context.Background(), nil, ListInput{ProjectPath: tmp})
	if err != nil {
		t.Fatalf("listHandler: %v", err)
	}
	return out.Rules
}

func strptr(s string) *string { return &s }

// TestMCPUpdatePartial verifies the patch semantics promised in the tool
// description: only the fields passed change; everything else is preserved.
func TestMCPUpdatePartial(t *testing.T) {
	tmp := tempProject(t)
	mustAdd(t, tmp, AddInput{
		Name: "patch-me", Description: "before",
		Host: "example.com", Path: "/v1/thing", Status: 500,
	})
	if _, _, err := updateHandler(context.Background(), nil, UpdateInput{
		ProjectPath: tmp, Name: "patch-me", Description: strptr("after"),
	}); err != nil {
		t.Fatalf("updateHandler: %v", err)
	}

	rules := listRules(t, tmp)
	if len(rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules))
	}
	r := rules[0]
	if r.Description != "after" {
		t.Errorf("description = %q, want after", r.Description)
	}
	if r.Host != "example.com" || r.Path != "/v1/thing" || r.Status != 500 {
		t.Errorf("unpatched fields not preserved: host=%q path=%q status=%d", r.Host, r.Path, r.Status)
	}
}

// TestMCPRemove confirms removeHandler deletes the rule from the catalog.
func TestMCPRemove(t *testing.T) {
	tmp := tempProject(t)
	mustAdd(t, tmp, AddInput{Name: "goner", Host: "127.0.0.1", Path: "/", Status: 404})
	if _, _, err := removeHandler(context.Background(), nil, RemoveInput{
		ProjectPath: tmp, Name: "goner",
	}); err != nil {
		t.Fatalf("removeHandler: %v", err)
	}
	if rules := listRules(t, tmp); len(rules) != 0 {
		t.Errorf("want 0 rules after remove, got %d", len(rules))
	}
}

// TestMCPAddLocalScope confirms the Local flag routes the rule to
// .forja/rules.local.yml and the list reports it as local-scoped.
func TestMCPAddLocalScope(t *testing.T) {
	tmp := tempProject(t)
	mustAdd(t, tmp, AddInput{
		Name: "personal", Host: "127.0.0.1", Path: "/", Status: 418, Local: true,
	})
	if _, err := os.Stat(filepath.Join(tmp, ".forja", "rules.local.yml")); err != nil {
		t.Errorf("expected rules.local.yml to be written: %v", err)
	}
	rules := listRules(t, tmp)
	if len(rules) != 1 || rules[0].Scope != "local" {
		t.Errorf("want 1 local-scoped rule, got %+v", rules)
	}
}

// TestMCPHandlersStdoutSilent guards the invariant the whole MCP design leans
// on: stdout is the protocol channel, so tool handlers must never write to it.
// If someone later slips an fmt.Print into a handler path, this fails.
func TestMCPHandlersStdoutSilent(t *testing.T) {
	tmp := tempProject(t)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	ctx := context.Background()
	_, _, _ = addHandler(ctx, nil, AddInput{ProjectPath: tmp, Name: "s", Host: "127.0.0.1", Path: "/", Status: 418, Body: `{"a":1}`})
	_, _, _ = listHandler(ctx, nil, ListInput{ProjectPath: tmp})
	_, _, _ = updateHandler(ctx, nil, UpdateInput{ProjectPath: tmp, Name: "s", Description: strptr("d")})
	_, _, _ = removeHandler(ctx, nil, RemoveInput{ProjectPath: tmp, Name: "s"})

	_ = w.Close()
	os.Stdout = old
	data, _ := io.ReadAll(r)
	if len(data) != 0 {
		t.Errorf("MCP handlers must not write to stdout (it is the protocol channel); got:\n%s", data)
	}
}

// TestMCPMissingForjaDir confirms a handler surfaces a clear error (rather than
// panicking or silently succeeding) when run outside a forja project.
func TestMCPMissingForjaDir(t *testing.T) {
	tmp := t.TempDir() // deliberately no .forja/
	_, _, err := listHandler(context.Background(), nil, ListInput{ProjectPath: tmp})
	if err == nil {
		t.Fatal("expected an error when .forja/ is absent")
	}
	if !strings.Contains(err.Error(), ".forja") {
		t.Errorf("error should mention .forja, got: %v", err)
	}
}

// TestMCPToolErrorSurface confirms a handler error travels back to the client
// as a tool-level error result (IsError), not a transport-level failure that
// would kill the session.
func TestMCPToolErrorSurface(t *testing.T) {
	tmp := t.TempDir() // no .forja/
	ctx := context.Background()

	clientT, serverT := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "forja", Version: "test"}, nil)
	registerTools(server)
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "forja_rules_list",
		Arguments: map[string]any{"project_path": tmp},
	})
	if err != nil {
		t.Fatalf("CallTool returned a transport error; want a tool-level error result: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError result for missing .forja/, got: %+v", res.Content)
	}
}
