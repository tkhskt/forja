package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDescriptionRoundTripsInYAML confirms the authoring-only description
// field survives a marshal/unmarshal cycle of the rule catalog.
func TestDescriptionRoundTripsInYAML(t *testing.T) {
	rf := &RulesFile{Rules: []Rule{{
		Name:        "x",
		Description: "simulate login outage",
		Match:       Match{Host: "e.com"},
		Response:    Response{Status: 418},
	}}}
	out, err := marshalYAML(rf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "description: simulate login outage") {
		t.Fatalf("yaml missing description:\n%s", out)
	}
	var got RulesFile
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Rules[0].Description != "simulate login outage" {
		t.Errorf("description = %q", got.Rules[0].Description)
	}
}

// TestDescriptionExcludedFromDeviceJSON is the load-bearing guarantee: the
// description is authoring metadata and must never reach the on-device wire
// payload (the interceptor never matches on it).
func TestDescriptionExcludedFromDeviceJSON(t *testing.T) {
	rf := &RulesFile{Rules: []Rule{{
		Name:        "x",
		Description: "secret intent note",
		Enabled:     true,
		Match:       Match{Host: "e.com", Path: "/p"},
		Response:    Response{Status: 418},
	}}}
	js, err := rf.ToDeviceJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(js)
	if strings.Contains(s, "description") || strings.Contains(s, "secret intent note") {
		t.Errorf("device JSON must not carry description:\n%s", s)
	}
	if !strings.Contains(s, "/p") {
		t.Errorf("expected the path to still be in device JSON:\n%s", s)
	}
}
