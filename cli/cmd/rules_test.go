package cmd

import "testing"

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
		"X Name=v",      // space
		"X:Name=v",      // colon
		"X\tName=v",     // tab
		"X\nName=v",     // newline
	}
	for _, in := range cases {
		if _, err := parseHeaders([]string{in}); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}
