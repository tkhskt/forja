package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tkhskt/forja/internal/config"
)

// TestParseBodyExplicitEmpty: an empty --body value is the explicit
// "force empty body" case. The caller (newRulesAddCmd / newRulesUpdateCmd)
// is responsible for gating on cmd.Flags().Changed("body") so this only
// runs when the user actually passed --body.
func TestParseBodyExplicitEmpty(t *testing.T) {
	b, err := parseBody("")
	if err != nil {
		t.Fatalf("parseBody(\"\"): %v", err)
	}
	if b == nil {
		t.Fatal("parseBody(\"\") should return non-nil BodyValue for the explicit-empty case")
	}
	if b.String != "" || b.Object != nil {
		t.Errorf("parseBody(\"\") should be &BodyValue{}, got %+v", b)
	}
}

func TestParseBodyJSONObjectAutoDetect(t *testing.T) {
	b, err := parseBody(`{"x":1}`)
	if err != nil {
		t.Fatalf("parseBody json: %v", err)
	}
	if b.Object == nil {
		t.Fatalf("JSON object string should produce Object body: %+v", b)
	}
	if v, ok := b.Object["x"].(float64); !ok || v != 1 {
		t.Errorf("Object content lost: %+v", b.Object)
	}
}

func TestParseBodyPlainString(t *testing.T) {
	b, err := parseBody("hello")
	if err != nil {
		t.Fatalf("parseBody plain: %v", err)
	}
	if b.Object != nil {
		t.Errorf("plain string should not yield Object: %+v", b)
	}
	if b.String != "hello" {
		t.Errorf("plain string lost: %q", b.String)
	}
}

// TestParseBodyMalformedJSON: a value that opens like a JSON object but
// doesn't parse is an error, not a silent fallback to a raw string body.
func TestParseBodyMalformedJSON(t *testing.T) {
	if _, err := parseBody(`{bad`); err == nil {
		t.Error("parseBody(`{bad`) should error, not fall back to a raw string")
	}
}

func TestParseHeadersHappyPath(t *testing.T) {
	got, err := parseHeaders([]string{
		"Content-Type=text/html; charset=utf-8",
		"X-Forja=1",
	})
	if err != nil {
		t.Fatalf("parseHeaders: %v", err)
	}
	if got["Content-Type"] != "text/html; charset=utf-8" {
		t.Errorf("Content-Type with embedded '=' / ';' should split on first '=' only, got %q", got["Content-Type"])
	}
	if got["X-Forja"] != "1" {
		t.Errorf("X-Forja: got %q", got["X-Forja"])
	}
}

func TestParseHeadersEmptyValueAllowed(t *testing.T) {
	got, err := parseHeaders([]string{"X-Empty="})
	if err != nil {
		t.Fatalf("parseHeaders empty value: %v", err)
	}
	if v, ok := got["X-Empty"]; !ok || v != "" {
		t.Errorf("empty value should be preserved, got %v (ok=%v)", v, ok)
	}
}

func TestParseHeadersClearSentinel(t *testing.T) {
	got, err := parseHeaders([]string{""})
	if err != nil {
		t.Fatalf("parseHeaders clear: %v", err)
	}
	if got == nil {
		t.Fatal("clear sentinel should return non-nil empty map (distinguishes from \"not provided\")")
	}
	if len(got) != 0 {
		t.Errorf("clear sentinel should produce empty map, got %+v", got)
	}
}

func TestParseHeadersClearSentinelRejectsMix(t *testing.T) {
	_, err := parseHeaders([]string{"", "X-Foo=bar"})
	if err == nil {
		t.Error("empty entry mixed with KEY=VALUE should be rejected (almost certainly user confusion)")
	}
}

func TestParseHeadersRejectsMissingEquals(t *testing.T) {
	_, err := parseHeaders([]string{"X-NoEquals"})
	if err == nil {
		t.Error("entry without '=' should be rejected")
	}
}

func TestParseHeadersRejectsEmptyKey(t *testing.T) {
	_, err := parseHeaders([]string{"=value"})
	if err == nil {
		t.Error("empty key should be rejected")
	}
}

func TestParseHeadersRejectsInvalidKeyChars(t *testing.T) {
	cases := []string{
		"X Name=v",  // space
		"X:Name=v",  // colon
		"X\tName=v", // tab
		"X\nName=v", // newline
	}
	for _, in := range cases {
		if _, err := parseHeaders([]string{in}); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}

// TestParseHeadersRejectsCRLFInValue: catching CR/LF in the value at the CLI
// boundary protects against accidental response-splitting and short-circuits
// the more confusing runtime error OkHttp would otherwise throw on the device.
func TestParseHeadersRejectsCRLFInValue(t *testing.T) {
	cases := []string{
		"X-Foo=line1\nline2", // LF
		"X-Foo=line1\rline2", // CR
		"X-Foo=null\x00byte", // NUL
	}
	for _, in := range cases {
		if _, err := parseHeaders([]string{in}); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}

// TestParseHeadersAcceptsBenignControlCharsInValue: tab and high-bit / UTF-8
// content must NOT be rejected — OkHttp's checkValue accepts them and forja
// shouldn't be stricter than the runtime that has to ingest the value.
func TestParseHeadersAcceptsBenignControlCharsInValue(t *testing.T) {
	cases := []string{
		"X-Foo=tab\there",    // HTAB is allowed in field-content per RFC 7230
		"X-Foo=日本語",          // UTF-8 (technically out of spec but commonly accepted)
		"X-Foo=spaces  here", // multiple spaces
	}
	for _, in := range cases {
		if _, err := parseHeaders([]string{in}); err != nil {
			t.Errorf("expected accept for %q, got error: %v", in, err)
		}
	}
}

// ---- rules list -----------------------------------------------------

// TestPrintRulesListEmpty: empty catalog prints a helpful onboarding hint
// rather than a blank line, so users can't get stuck thinking the command
// silently no-op'd.
func TestPrintRulesListEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := printRulesList(&buf, nil, ""); err != nil {
		t.Fatalf("printRulesList: %v", err)
	}
	if !strings.Contains(buf.String(), "no rules") {
		t.Errorf("empty list should hint at add command; got %q", buf.String())
	}
}

// TestPrintRulesListGroupsByScope: local then project, each under its own
// header, in the order they would be tried by the on-device interceptor.
func TestPrintRulesListGroupsByScope(t *testing.T) {
	eff := []config.EffectiveRule{
		{Rule: config.Rule{Name: "local-only", Match: config.Match{Host: "example.com"}, Response: config.Response{Status: 500}}, Scope: config.ScopeLocal},
		{Rule: config.Rule{Name: "project-only", Match: config.Match{Host: "example.com", Path: "/p"}, Response: config.Response{Status: 418}}, Scope: config.ScopeProject},
	}
	var buf bytes.Buffer
	if err := printRulesList(&buf, eff, ""); err != nil {
		t.Fatalf("printRulesList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "local:\n") {
		t.Errorf("missing local section header: %s", out)
	}
	if !strings.Contains(out, "project:\n") {
		t.Errorf("missing project section header: %s", out)
	}
	// Local must appear before project in the output (= match-scan order).
	if strings.Index(out, "local:") > strings.Index(out, "project:") {
		t.Errorf("local section must precede project; got:\n%s", out)
	}
	if !strings.Contains(out, "local-only") || !strings.Contains(out, "project-only") {
		t.Errorf("rule names missing: %s", out)
	}
}

// TestPrintRulesListShowEnabledColumn: passing a non-empty app argument
// switches the per-line prefix from a bullet `- ` to a `[on ]` / `[off]`
// marker so users can see which rules are active for the chosen app.
func TestPrintRulesListShowEnabledColumn(t *testing.T) {
	eff := []config.EffectiveRule{
		{Rule: config.Rule{Name: "a", Enabled: true, Response: config.Response{Status: 200}}, Scope: config.ScopeLocal},
		{Rule: config.Rule{Name: "b", Enabled: false, Response: config.Response{Status: 500}}, Scope: config.ScopeLocal},
	}
	var buf bytes.Buffer
	if err := printRulesList(&buf, eff, "com.example.app"); err != nil {
		t.Fatalf("printRulesList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[on]  a") {
		t.Errorf("expected [on] prefix on rule 'a': %s", out)
	}
	if !strings.Contains(out, "[off] b") {
		t.Errorf("expected [off] prefix on rule 'b': %s", out)
	}
	if !strings.Contains(out, "target: com.example.app") {
		t.Errorf("footer should name the target app: %s", out)
	}
}

// TestFormatRuleLineFieldRendering: each non-zero field appears, zero fields
// stay hidden, and body presentation shows the actual content (truncated)
// rather than an opaque length so users can spot the right rule visually.
func TestFormatRuleLineFieldRendering(t *testing.T) {
	cases := []struct {
		name    string
		rule    config.EffectiveRule
		want    []string
		notWant []string
	}{
		{
			name: "host-only rule shows host and nothing else",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Match: config.Match{Host: "example.com"}},
			},
			want:    []string{"x", "host=example.com"},
			notWant: []string{"path=", "status=", "body="},
		},
		{
			name: "json object body renders as the JSON itself (in-memory form)",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Response: config.Response{
					Body: &config.BodyValue{Object: map[string]any{"k": "v"}},
				}},
			},
			want: []string{`body='{"k":"v"}'`},
		},
		{
			name: "explicit empty body renders as body=''",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Response: config.Response{
					Body: &config.BodyValue{String: ""},
				}},
			},
			want: []string{"body=''"},
		},
		{
			name: "short string body renders inline in single quotes",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Response: config.Response{
					Body: &config.BodyValue{String: `{"message":"failure"}`},
				}},
			},
			// 21-char JSON-shaped string, fits under the 30-rune truncation cap.
			want:    []string{`body='{"message":"failure"}'`},
			notWant: []string{"chars)", "..."},
		},
		{
			name: "long body is truncated with rune count suffix",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Response: config.Response{
					Body: &config.BodyValue{String: strings.Repeat("a", 200)},
				}},
			},
			want: []string{"...", "(200 chars)"},
		},
		{
			name: "control chars in body are escaped, line stays single-line",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Response: config.Response{
					Body: &config.BodyValue{String: "line1\nline2"},
				}},
			},
			want:    []string{`body='line1\nline2'`},
			notWant: []string{"\n"}, // literal newline must not appear in output
		},
		{
			name: "headers count appears when set",
			rule: config.EffectiveRule{
				Rule: config.Rule{Name: "x", Response: config.Response{
					Headers: map[string]string{"Content-Type": "text/html", "X-A": "1"},
				}},
			},
			want: []string{"headers=2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatRuleLine(tc.rule, false)
			for _, s := range tc.want {
				if !strings.Contains(got, s) {
					t.Errorf("want %q in output, got: %s", s, got)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(got, s) {
					t.Errorf("want %q absent, got: %s", s, got)
				}
			}
		})
	}
}
