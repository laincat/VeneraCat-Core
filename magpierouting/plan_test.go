package magpierouting

import (
	"strings"
	"testing"
	"time"
)

// The decision rules users care about, each pinned to the behaviour a wrong
// implementation would break.

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func ptr(v float64) *float64 { return &v }

func models(entries ...Candidate) map[string]Candidate {
	out := map[string]Candidate{}
	for _, entry := range entries {
		out[entry.Model] = entry
	}
	return out
}

func TestNeedsRemoveCandidatesRatherThanRankThem(t *testing.T) {
	accounts := []Account{{ID: "a", Provider: "p1"}, {ID: "b", Provider: "p2"}}
	catalog := models(
		Candidate{Provider: "p1", Model: "text-only"},
		Candidate{Provider: "p2", Model: "sees", Capability: Capability{Vision: true}},
	)
	plan := Build(Request{Needs: Needs{Vision: true}}, accounts, catalog, Policy{}, now)

	if len(plan.Candidates) != 1 || plan.Candidates[0].Model != "sees" {
		t.Fatalf("candidates = %+v", plan.Candidates)
	}
	// The rejection is part of the answer: a user asking why their strong
	// model was not used needs it.
	if len(plan.Rejections) != 1 || plan.Rejections[0].Reason != "cannot meet the requirement" {
		t.Fatalf("rejections = %+v", plan.Rejections)
	}
}

func TestUnknownQuotaIsNotZeroQuota(t *testing.T) {
	// "Cannot tell" must not remove an account. Only a reported window that is
	// fully used does.
	unknown := Account{ID: "unknown", Provider: "p", Allowances: []Allowance{{ResetAt: now.Add(time.Hour)}}}
	spent := Account{ID: "spent", Provider: "p", Allowances: []Allowance{{Used: ptr(10), Limit: ptr(10)}}}
	catalog := models(Candidate{Provider: "p", Model: "m"})

	plan := Build(Request{}, []Account{unknown, spent}, catalog, Policy{}, now)

	if len(plan.Candidates) != 1 || plan.Candidates[0].Account != "unknown" {
		t.Fatalf("candidates = %+v", plan.Candidates)
	}
	if len(plan.Rejections) != 1 || plan.Rejections[0].Reason != "quota used up" {
		t.Fatalf("rejections = %+v", plan.Rejections)
	}
}

func TestAnEndedSubscriptionIsNotUsed(t *testing.T) {
	ended := Account{ID: "ended", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(-time.Hour)}}
	renewing := Account{ID: "renewing", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(-time.Hour), AutoRenew: true}}
	catalog := models(Candidate{Provider: "p", Model: "m"})

	plan := Build(Request{}, []Account{ended, renewing}, catalog, Policy{}, now)

	if len(plan.Candidates) != 1 || plan.Candidates[0].Account != "renewing" {
		t.Fatalf("candidates = %+v", plan.Candidates)
	}
	if len(plan.Rejections) != 1 || plan.Rejections[0].Reason != "the subscription has ended" {
		t.Fatalf("rejections = %+v", plan.Rejections)
	}
}

func TestExpiringSoonGoesFirstOnlyWhenAsked(t *testing.T) {
	soon := Account{ID: "soon", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(48 * time.Hour)}}
	later := Account{ID: "later", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(300 * 24 * time.Hour)}}
	catalog := models(Candidate{Provider: "p", Model: "m"})
	accounts := []Account{later, soon}

	plain := Build(Request{}, accounts, catalog, Policy{}, now)
	if plain.Candidates[0].Account != "later" {
		t.Fatalf("without the policy the order must be the given one, got %s", plain.Candidates[0].Account)
	}

	policy := Policy{PreferExpiringSoon: true, ExpiringWithin: 7 * 24 * time.Hour}
	expiring := Build(Request{}, accounts, catalog, policy, now)
	if expiring.Candidates[0].Account != "soon" {
		t.Fatalf("expiring-soon policy did not reorder: %+v", expiring.Candidates)
	}
}

func TestAutoRenewIsNotSpentDownFirst(t *testing.T) {
	// A plan that continues by itself has no allowance to waste, so its end date
	// must not make it look like one that lapses.
	renewing := Account{ID: "renewing", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(24 * time.Hour), AutoRenew: true}}
	ending := Account{ID: "ending", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(24 * time.Hour)}}
	catalog := models(Candidate{Provider: "p", Model: "m"})
	policy := Policy{PreferExpiringSoon: true, ExpiringWithin: 7 * 24 * time.Hour}

	plan := Build(Request{}, []Account{renewing, ending}, catalog, policy, now)
	if plan.Candidates[0].Account != "ending" {
		t.Fatalf("auto-renewing account went first: %+v", plan.Candidates)
	}
}

func TestDifficultyUsesTheTierAndSaysWhyTheRestWereLeftOut(t *testing.T) {
	accounts := []Account{{ID: "a", Provider: "cheap"}, {ID: "b", Provider: "strong"}}
	catalog := models(
		Candidate{Provider: "cheap", Model: "small"},
		Candidate{Provider: "strong", Model: "big"},
	)
	policy := Policy{Tiers: map[Difficulty]Tier{
		Complex: {Models: []string{"big"}, Providers: []string{"strong"}},
	}}

	plan := Build(Request{Difficulty: Simple}, accounts, catalog, policy, now)
	if len(plan.Candidates) != 2 {
		t.Fatalf("a tier that names nothing for Simple must not filter: %+v", plan.Candidates)
	}

	plan = Build(Request{Difficulty: Complex}, accounts, catalog, policy, now)
	if len(plan.Candidates) != 1 || plan.Candidates[0].Model != "big" {
		t.Fatalf("complex tier = %+v", plan.Candidates)
	}
	if len(plan.Rejections) != 1 || plan.Rejections[0].Reason != "not in this task's tier" {
		t.Fatalf("rejections = %+v", plan.Rejections)
	}
}

func TestAnExplicitPickIsNeverReplaced(t *testing.T) {
	// Silently answering with a different model would be worse than failing.
	accounts := []Account{{ID: "a", Provider: "p"}}
	catalog := models(
		Candidate{Provider: "p", Model: "asked"},
		Candidate{Provider: "p", Model: "other"},
	)
	plan := Build(Request{Model: "asked", Difficulty: Complex}, accounts, catalog,
		Policy{Tiers: map[Difficulty]Tier{Complex: {Models: []string{"other"}}}}, now)

	if len(plan.Candidates) != 1 || plan.Candidates[0].Model != "asked" {
		t.Fatalf("explicit pick was filtered or replaced: %+v", plan.Candidates)
	}

	missing := Build(Request{Model: "absent"}, accounts, catalog, Policy{}, now)
	if len(missing.Candidates) != 0 {
		t.Fatalf("a pick nobody serves must produce no candidate: %+v", missing)
	}
	// The reason has to name the pick, not blame a tier the user never set.
	for _, rejection := range missing.Rejections {
		if rejection.Reason != "not the model that was asked for" {
			t.Fatalf("rejection reason = %q", rejection.Reason)
		}
	}
}

func TestRestingAndBlockedAccounts(t *testing.T) {
	blocked := Account{ID: "blocked", Provider: "p", Health: Blocked}
	resting := Account{ID: "resting", Provider: "p", Health: Resting, RetryAt: now.Add(10 * time.Minute)}
	healthy := Account{ID: "healthy", Provider: "p"}
	catalog := models(Candidate{Provider: "p", Model: "m"})

	plan := Build(Request{}, []Account{blocked, resting}, catalog, Policy{}, now)
	if len(plan.Candidates) != 1 || plan.Candidates[0].Account != "resting" {
		t.Fatalf("a resting account should still answer when nothing healthy is left: %+v", plan.Candidates)
	}

	plan = Build(Request{}, []Account{blocked, resting, healthy}, catalog, Policy{}, now)
	if len(plan.Candidates) != 2 || plan.Candidates[0].Account != "healthy" {
		t.Fatalf("healthy must come first: %+v", plan.Candidates)
	}

	plan = Build(Request{}, []Account{blocked}, catalog, Policy{}, now)
	if len(plan.Candidates) != 0 || plan.Rejections[0].Reason != "the sign-in needs attention" {
		t.Fatalf("blocked account = %+v", plan)
	}
}

func TestSessionAffinityBeatsPriceAndExpiry(t *testing.T) {
	session := Account{ID: "session", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(300 * 24 * time.Hour)}}
	other := Account{ID: "other", Provider: "p", Subscription: Subscription{ExpiresAt: now.Add(time.Hour)}}
	catalog := models(Candidate{Provider: "p", Model: "m"})
	policy := Policy{PreferExpiringSoon: true, ExpiringWithin: 7 * 24 * time.Hour,
		KeepSession: true, SessionAccount: "session"}

	plan := Build(Request{}, []Account{other, session}, catalog, policy, now)
	if plan.Candidates[0].Account != "session" {
		t.Fatalf("affinity lost to expiry: %+v", plan.Candidates)
	}
}

func TestLowestCostIsOptIn(t *testing.T) {
	cheap := Account{ID: "cheap", Provider: "p"}
	catalog := models(
		Candidate{Provider: "p", Model: "a", Cost: 0.2},
		Candidate{Provider: "p", Model: "b", Cost: 2},
	)
	plain := Build(Request{}, []Account{cheap}, catalog, Policy{}, now)
	if !strings.HasSuffix(plain.Candidates[0].Model, "a") {
		t.Fatalf("the map order is not the point, but the tiebreak must be stable: %+v", plain.Candidates)
	}

	costly := Build(Request{}, []Account{cheap}, catalog, Policy{PreferLowestCost: true}, now)
	if costly.Candidates[0].Model != "a" {
		t.Fatalf("cost policy did not pick the cheaper model: %+v", costly.Candidates)
	}
}

func TestPlanIsReproducible(t *testing.T) {
	accounts := []Account{{ID: "b", Provider: "p"}, {ID: "a", Provider: "p"}}
	catalog := models(
		Candidate{Provider: "p", Model: "y"},
		Candidate{Provider: "p", Model: "x"},
	)
	first := Build(Request{}, accounts, catalog, Policy{}, now)
	for i := 0; i < 20; i++ {
		again := Build(Request{}, accounts, catalog, Policy{}, now)
		if len(again.Candidates) != len(first.Candidates) {
			t.Fatalf("length changed between runs")
		}
		for j := range first.Candidates {
			if first.Candidates[j].Model != again.Candidates[j].Model ||
				first.Candidates[j].Account != again.Candidates[j].Account {
				t.Fatalf("order changed between runs: %+v vs %+v", first.Candidates, again.Candidates)
			}
		}
	}
	// The tiebreak is spelling, so "a" comes before "b".
	if first.Candidates[0].Account != "a" {
		t.Fatalf("tiebreak = %+v", first.Candidates)
	}
}

func TestExplainNamesTheChoiceAndTheReasons(t *testing.T) {
	accounts := []Account{{ID: "a", Provider: "p"}}
	catalog := models(Candidate{Provider: "p", Model: "m", Capability: Capability{Vision: true}})
	plan := Build(Request{Needs: Needs{Vision: true, Tools: true}}, accounts, catalog, Policy{}, now)
	line := plan.Explain()
	if !strings.Contains(line, "no candidate") || !strings.Contains(line, "cannot meet the requirement") {
		t.Fatalf("explanation = %q", line)
	}
}

func TestAllowanceModelsAreRespected(t *testing.T) {
	// A window that names models applies only to those models; a model with no
	// window of its own falls back to the unnamed one.
	account := Account{ID: "a", Provider: "p", Allowances: []Allowance{
		{Used: ptr(5), Limit: ptr(5), Models: []string{"only-this"}},
		{Used: ptr(0), Limit: ptr(100)},
	}}
	catalog := models(
		Candidate{Provider: "p", Model: "only-this"},
		Candidate{Provider: "p", Model: "anything-else"},
	)
	plan := Build(Request{}, []Account{account}, catalog, Policy{}, now)
	if len(plan.Candidates) != 1 || plan.Candidates[0].Model != "anything-else" {
		t.Fatalf("per-model allowance was not applied: %+v", plan.Candidates)
	}
}

func TestDialectRequirement(t *testing.T) {
	accounts := []Account{{ID: "a", Provider: "p"}}
	catalog := models(
		Candidate{Provider: "p", Model: "chat", Capability: Capability{Dialects: []string{"chat"}}},
		Candidate{Provider: "p", Model: "messages", Capability: Capability{Dialects: []string{"messages"}}},
		// A model that says nothing about dialects is not excluded: silence is
		// not a refusal.
		Candidate{Provider: "p", Model: "silent"},
	)
	plan := Build(Request{Needs: Needs{Dialect: "messages"}}, accounts, catalog, Policy{}, now)
	got := map[string]bool{}
	for _, candidate := range plan.Candidates {
		got[candidate.Model] = true
	}
	if !got["messages"] || !got["silent"] || got["chat"] {
		t.Fatalf("dialect filter = %+v", plan.Candidates)
	}
}
