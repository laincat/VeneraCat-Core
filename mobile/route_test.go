package mobile

import (
	"encoding/json"
	"strings"
	"testing"
)

// PlanRoute is the one place a client's account state becomes a decision. These
// tests pin the JSON contract and the rules a wrong implementation would break.

func routeJSON(t *testing.T, input string) (map[string]any, error) {
	t.Helper()
	m := &Magpie{}
	out, err := m.PlanRoute(input)
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("route output is not JSON: %v (%s)", err, out)
	}
	return parsed, nil
}

func TestPlanRouteRefusesToInventATime(t *testing.T) {
	// A plan that reads the machine clock cannot be reproduced, and an
	// explanation that changes per run is not an explanation.
	m := &Magpie{}
	if _, err := m.PlanRoute(`{"accounts":[]}`); err == nil {
		t.Fatal("a plan with no now was accepted")
	}
	if _, err := m.PlanRoute(`{"now":1,"accounts":[{"provider":"p"}]}`); err == nil {
		t.Fatal("an account without an id was accepted")
	}
	if _, err := m.PlanRoute(`not json`); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestPlanRouteOrdersAndExplains(t *testing.T) {
	input := `{
	  "now": 1793520000000,
	  "accounts": [
	    {"id":"later","provider":"p","subscription":{"expiresAt":1800892800000}},
	    {"id":"soon","provider":"p","subscription":{"expiresAt":1794200000000}}
	  ],
	  "models": [{"provider":"p","model":"m"}],
	  "request": {"difficulty":"normal"},
	  "policy": {"preferExpiringSoon":true,"expiringWithinSecs":1209600}
	}`
	parsed, err := routeJSON(t, input)
	if err != nil {
		t.Fatal(err)
	}
	candidates, _ := parsed["candidates"].([]any)
	if len(candidates) != 2 {
		t.Fatalf("candidates = %v", parsed["candidates"])
	}
	first, _ := candidates[0].(map[string]any)
	if first["account"] != "soon" {
		t.Fatalf("expiring-soon account did not go first: %v", first)
	}
	if explain, _ := parsed["explain"].(string); !strings.Contains(explain, "p/m") {
		t.Fatalf("explain = %q", explain)
	}
}

func TestPlanRouteKeepsUnreportedQuotaUsable(t *testing.T) {
	input := `{
	  "now": 1793520000000,
	  "accounts": [
	    {"id":"unknown","provider":"p","allowances":[{"resetAt":1793530000000}]},
	    {"id":"spent","provider":"p","allowances":[{"used":10,"limit":10}]}
	  ],
	  "models": [{"provider":"p","model":"m"}],
	  "request": {}
	}`
	parsed, err := routeJSON(t, input)
	if err != nil {
		t.Fatal(err)
	}
	candidates, _ := parsed["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %v", parsed["candidates"])
	}
	only, _ := candidates[0].(map[string]any)
	if only["account"] != "unknown" {
		t.Fatalf("the account with unreported quota was not the one chosen: %v", only)
	}
	rejections, _ := parsed["rejections"].([]any)
	if len(rejections) != 1 {
		t.Fatalf("rejections = %v", parsed["rejections"])
	}
	rejection, _ := rejections[0].(map[string]any)
	if rejection["reason"] != "quota used up" {
		t.Fatalf("reason = %v", rejection["reason"])
	}
}

func TestPlanRouteCarriesCapabilitiesAndTiers(t *testing.T) {
	base := `{
	  "now": 1793520000000,
	  "accounts": [{"id":"a","provider":"p"}],
	  "models": [
	    {"provider":"p","model":"text"},
	    {"provider":"p","model":"vision","vision":true,"context":200000}
	  ],
	  "request": {"vision":true,"minContext":100000,"difficulty":"complex"},
	  "policy": {"tiers":{"complex":{"models":["vision"]}}}
	}`
	parsed, err := routeJSON(t, base)
	if err != nil {
		t.Fatal(err)
	}
	candidates, _ := parsed["candidates"].([]any)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %v", parsed["candidates"])
	}
	only, _ := candidates[0].(map[string]any)
	if only["model"] != "vision" {
		t.Fatalf("model = %v", only["model"])
	}
}

func TestPlanRouteWithoutCandidatesStillExplains(t *testing.T) {
	parsed, err := routeJSON(t, `{"now":1793520000000,"accounts":[{"id":"a","provider":"p","health":"blocked"}],
	  "models":[{"provider":"p","model":"m"}],"request":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	candidates, _ := parsed["candidates"].([]any)
	if len(candidates) != 0 {
		t.Fatalf("candidates = %v", parsed["candidates"])
	}
	explain, _ := parsed["explain"].(string)
	if !strings.Contains(explain, "no candidate") {
		t.Fatalf("explain = %q", explain)
	}
	rejections, _ := parsed["rejections"].([]any)
	rejection, _ := rejections[0].(map[string]any)
	if rejection["reason"] != "the sign-in needs attention" {
		t.Fatalf("reason = %v", rejection["reason"])
	}
}
