package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBodyValueUnmarshalScalar(t *testing.T) {
	var b BodyValue
	if err := yaml.Unmarshal([]byte("hello world"), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.String != "hello world" {
		t.Errorf("want String=hello world, got %q", b.String)
	}
	if b.Object != nil {
		t.Errorf("want Object=nil, got %v", b.Object)
	}
}

func TestBodyValueRejectsMappingForm(t *testing.T) {
	var b BodyValue
	src := "message: failure\ncode: 42\n"
	err := yaml.Unmarshal([]byte(src), &b)
	if err == nil {
		t.Fatalf("expected error, got success: %+v", b)
	}
	// Error message should hint at the supported alternatives.
	if msg := err.Error(); !strings.Contains(msg, "JSON string") &&
		!strings.Contains(msg, "bodyFile") {
		t.Errorf("error message should point users to the supported forms, got %q", msg)
	}
}

func TestBodyValueMarshalRoundTrip(t *testing.T) {
	t.Run("scalar round-trips as-is", func(t *testing.T) {
		in := BodyValue{String: "plain text"}
		out, err := yaml.Marshal(&in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back BodyValue
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal back: %v", err)
		}
		if back.String != in.String {
			t.Errorf("String mismatch: %q vs %q", back.String, in.String)
		}
	})

	t.Run("empty string survives round-trip as explicit empty", func(t *testing.T) {
		// Empty body is meaningful — it's "force the response body to be
		// empty", which is distinct from "no body override" (= nil pointer).
		// The Rule struct uses *BodyValue precisely so callers can encode
		// the no-override case as nil; the BodyValue itself just needs to
		// preserve the empty string verbatim.
		in := BodyValue{String: ""}
		out, err := yaml.Marshal(&in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back BodyValue
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal back: %v", err)
		}
		if back.String != "" {
			t.Errorf("empty string body should round-trip as empty, got %q", back.String)
		}
	})

	t.Run("Object marshals to JSON string scalar", func(t *testing.T) {
		// Object is set internally by the CLI auto-detect or bodyFile.json
		// readers. It is NOT a user-facing yml shape — round-trip is
		// lossy by design (Object → JSON string → String on read-back).
		in := BodyValue{Object: map[string]any{"k": "v"}}
		out, err := yaml.Marshal(&in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back BodyValue
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal back: %v", err)
		}
		if back.String != `{"k":"v"}` {
			t.Errorf("Object should marshal to JSON string scalar, got String=%q", back.String)
		}
		if back.Object != nil {
			t.Errorf("Object should be nil after round-trip (lossy by design), got %v", back.Object)
		}
	})
}

func TestFindRule(t *testing.T) {
	rf := &RulesFile{
		Rules: []Rule{
			{Name: "a"}, {Name: "b"}, {Name: "c"},
		},
	}
	if r := rf.FindRule("b"); r == nil || r.Name != "b" {
		t.Errorf("FindRule(b) = %v, want b", r)
	}
	if r := rf.FindRule("missing"); r != nil {
		t.Errorf("FindRule(missing) = %v, want nil", r)
	}
	// FindRule should return a pointer that mutates the slice.
	r := rf.FindRule("a")
	r.Enabled = true
	if !rf.Rules[0].Enabled {
		t.Errorf("FindRule did not return pointer into slice")
	}
}

func TestAddRule(t *testing.T) {
	rf := &RulesFile{}
	if err := rf.AddRule(Rule{Name: "x"}); err != nil {
		t.Fatalf("add x: %v", err)
	}
	if err := rf.AddRule(Rule{Name: "y"}); err != nil {
		t.Fatalf("add y: %v", err)
	}
	if err := rf.AddRule(Rule{Name: "x"}); err == nil {
		t.Errorf("expected duplicate add to fail")
	}
	if len(rf.Rules) != 2 {
		t.Errorf("want 2 rules, got %d", len(rf.Rules))
	}
}

func TestRemoveRule(t *testing.T) {
	rf := &RulesFile{Rules: []Rule{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	if err := rf.RemoveRule("b"); err != nil {
		t.Fatalf("remove b: %v", err)
	}
	if len(rf.Rules) != 2 {
		t.Errorf("want 2 rules after remove, got %d", len(rf.Rules))
	}
	if rf.Rules[0].Name != "a" || rf.Rules[1].Name != "c" {
		t.Errorf("unexpected order after remove: %+v", rf.Rules)
	}
	if err := rf.RemoveRule("missing"); err == nil {
		t.Errorf("expected remove missing to fail")
	}
}
