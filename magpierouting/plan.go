// Package magpierouting chooses which provider account and model answers a
// request.
//
// Magpie's own gateway routes inside the desktop app. iOS embeds the plugin
// runtime instead, so the choice has to happen here: the same request may be
// servable by several providers, several accounts of one provider, and several
// models with different capabilities and costs.
//
// The rules that matter to users are stated as data and applied in a fixed
// order, so a decision can be explained afterwards: hard requirements remove
// candidates, then policy orders what is left. Nothing here talks to a network
// or reads a clock; the caller passes both.
package magpierouting

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Difficulty is how demanding the task is, as the user or a classifier judged it.
// It is deliberately not a number taken from a model name.
type Difficulty int

const (
	// Simple is a short answer, or a cheap rewrite.
	Simple Difficulty = iota
	// Normal is the default when nothing says otherwise.
	Normal
	// Complex needs the strongest model the user is willing to spend.
	Complex
)

// Needs are the hard requirements of one request. A candidate that fails any of
// them is removed rather than ranked lower: a model without vision cannot be
// asked to read an image at all.
type Needs struct {
	Vision     bool
	Tools      bool
	Reasoning  bool
	MinContext int64
	Dialect    string
}

// Capability is what one model can do, as its provider reported it.
type Capability struct {
	Vision    bool
	Tools     bool
	Reasoning bool
	Context   int64
	Dialects  []string
}

// Supports reports whether this capability covers the needs.
func (c Capability) Supports(n Needs) bool {
	if n.Vision && !c.Vision {
		return false
	}
	if n.Tools && !c.Tools {
		return false
	}
	if n.Reasoning && !c.Reasoning {
		return false
	}
	if n.MinContext > 0 && c.Context < n.MinContext {
		return false
	}
	if n.Dialect != "" && len(c.Dialects) > 0 {
		found := false
		for _, dialect := range c.Dialects {
			if dialect == n.Dialect {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Health is what the last request to an account came to.
type Health int

const (
	// Healthy has answered, or has not been tried yet.
	Healthy Health = iota
	// Resting is cooling down after a refusal, and is used only when nothing
	// healthy is left.
	Resting
	// Blocked is a refusal the client has to resolve: a rejected credential, a
	// captcha, a suspended account. It is never chosen automatically.
	Blocked
)

// Allowance is what is left on one account, per model when the vendor reports
// per-model windows.
//
// Used and Limit are pointers because "the vendor did not say" and "nothing is
// used" read differently: a quota card that shows 0% for an unreported account
// is a lie, and routing that treats it as empty spends the wrong account.
type Allowance struct {
	Used  *float64
	Limit *float64
	// ResetAt is when the vendor says the window turns over.
	ResetAt time.Time
	// Models, when not empty, limits this allowance to those model ids.
	Models []string
}

// Remaining is the fraction left, and whether that is known at all.
func (a Allowance) Remaining() (float64, bool) {
	if a.Used == nil || a.Limit == nil || *a.Limit <= 0 {
		return 0, false
	}
	left := 1 - *a.Used / *a.Limit
	if left < 0 {
		left = 0
	}
	return left, true
}

// Exhausted reports a window the vendor says is used up. An unknown window is
// not exhausted.
func (a Allowance) Exhausted() bool {
	left, ok := a.Remaining()
	return ok && left <= 0
}

// Subscription is a plan the account is billed through, when the provider says.
type Subscription struct {
	// ExpiresAt is when the plan ends, zero when the vendor did not say.
	ExpiresAt time.Time
	// AutoRenew reports a plan that continues by itself, so its end date does
	// not make it a candidate to spend down first.
	AutoRenew bool
}

// ExpiresWithin reports a dated, non-renewing plan ending within d of now.
func (s Subscription) ExpiresWithin(now time.Time, d time.Duration) bool {
	if s.AutoRenew || s.ExpiresAt.IsZero() {
		return false
	}
	return !s.ExpiresAt.After(now.Add(d)) && s.ExpiresAt.After(now)
}

// Expired reports a plan that has already ended.
func (s Subscription) Expired(now time.Time) bool {
	return !s.AutoRenew && !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(now)
}

// Account is one signed-in provider account.
type Account struct {
	// ID identifies the sign-in. Two accounts of one provider are two IDs.
	ID       string
	Provider string
	Health   Health
	// RetryAt is when a resting account may be tried again.
	RetryAt time.Time
	// Subscription is the plan behind the account, when it has one.
	Subscription Subscription
	// Allowances are the quota windows the vendor reported.
	Allowances []Allowance
}

// Usable reports whether the account may answer at now.
func (a Account) Usable(now time.Time) bool {
	if a.Health == Blocked {
		return false
	}
	// A resting account stays usable: the caller may still have nothing else,
	// and a cooling-down account that answers beats failing the request. Ordering
	// puts it behind every healthy one.
	if a.Subscription.Expired(now) {
		return false
	}
	for _, allowance := range a.Allowances {
		// One exhausted window does not rule an account out: the others may
		// cover this model. Exhaustion is judged per model below.
		_ = allowance
	}
	return true
}

// AllowanceFor returns the allowance covering a model: the first one naming it,
// else the first that names no models.
func (a Account) AllowanceFor(model string) (Allowance, bool) {
	for _, allowance := range a.Allowances {
		for _, named := range allowance.Models {
			if named == model {
				return allowance, true
			}
		}
	}
	for _, allowance := range a.Allowances {
		if len(allowance.Models) == 0 {
			return allowance, true
		}
	}
	return Allowance{}, false
}

// Candidate is one account and model that could answer.
type Candidate struct {
	Provider string
	Account  string
	Model    string
	Capability
	// Cost is the vendor's price signal, 0 when unknown or free.
	Cost float64
}

// Tier names the group a difficulty maps to. The user sets the mapping; this
// package only applies it.
type Tier struct {
	// Models lists what this tier may use, in preference order.
	Models []string
	// Providers, when not empty, limits the tier to those providers.
	Providers []string
}

// Policy is how remaining candidates are ordered.
type Policy struct {
	// PreferExpiringSoon puts plans that end soonest first, so allowance that
	// would otherwise lapse is spent first. Plans that renew are not affected.
	PreferExpiringSoon bool
	// ExpiringWithin is how close an end date has to be to count as soon.
	ExpiringWithin time.Duration
	// PreferLowestCost orders by reported price. It is off by default: cost
	// ordering silently overrides quality.
	PreferLowestCost bool
	// KeepSession prefers the account that answered this conversation before,
	// so the vendor's prompt cache is reused.
	KeepSession bool
	// SessionAccount is that account.
	SessionAccount string
	// Tiers maps a difficulty to the models and providers allowed for it.
	Tiers map[Difficulty]Tier
}

// Request is one decision to make.
type Request struct {
	Needs      Needs
	Difficulty Difficulty
	// Account is the account that answered this conversation before, when the
	// caller knows it.
	Account string
	// Model, when set, is what the user picked: the plan starts with it and
	// never substitutes a different model silently.
	Model string
}

// Rejection says why a candidate was left out, so the UI can explain it.
type Rejection struct {
	Provider string
	Account  string
	Model    string
	Reason   string
}

// Plan is the ordered answer.
type Plan struct {
	Candidates []Candidate
	Rejections []Rejection
}

// Build plans a request from the catalog the clients reported.
//
// Order of work: hard requirements, then the tier the difficulty allows, then
// policy ordering, then a stable tiebreak so the same inputs give the same plan.
func Build(req Request, accounts []Account, models map[string]Candidate, policy Policy, now time.Time) Plan {
	var plan Plan
	if len(accounts) == 0 {
		return plan
	}

	tier, hasTier := policy.Tiers[req.Difficulty]
	// An explicit pick is never broadened: if it cannot answer, the caller has
	// to say so rather than receive a different model's reply.
	if req.Model != "" {
		hasTier = false
	}

	allowed := func(model string, provider string) bool {
		if req.Model != "" {
			return model == req.Model
		}
		if !hasTier {
			return true
		}
		if len(tier.Providers) > 0 {
			found := false
			for _, allowedProvider := range tier.Providers {
				if allowedProvider == provider {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		if len(tier.Models) == 0 {
			return true
		}
		for _, named := range tier.Models {
			if named == model {
				return true
			}
		}
		return false
	}

	for _, account := range accounts {
		if !account.Usable(now) {
			plan.Rejections = append(plan.Rejections, Rejection{Provider: account.Provider, Account: account.ID,
				Reason: unusableReason(account, now)})
			continue
		}
		// A model reaches an account only if that provider serves it.
		for model, candidate := range models {
			if candidate.Provider != account.Provider {
				continue
			}
			if !allowed(model, account.Provider) {
				reason := "not in this task's tier"
				if req.Model != "" {
					// Saying "tier" here would hide the real answer: the user
					// asked for one specific model and this is not it.
					reason = "not the model that was asked for"
				}
				plan.Rejections = append(plan.Rejections, Rejection{Provider: account.Provider, Account: account.ID,
					Model: model, Reason: reason})
				continue
			}
			if !candidate.Capability.Supports(req.Needs) {
				plan.Rejections = append(plan.Rejections, Rejection{Provider: account.Provider, Account: account.ID,
					Model: model, Reason: "cannot meet the requirement"})
				continue
			}
			if allowance, ok := account.AllowanceFor(model); ok && allowance.Exhausted() {
				plan.Rejections = append(plan.Rejections, Rejection{Provider: account.Provider, Account: account.ID,
					Model: model, Reason: "quota used up"})
				continue
			}
			plan.Candidates = append(plan.Candidates, Candidate{
				Provider: account.Provider, Account: account.ID, Model: model,
				Capability: candidate.Capability, Cost: candidate.Cost})
		}
	}

	order(plan.Candidates, accounts, policy, now)
	if len(plan.Candidates) == 0 {
		return plan
	}
	return plan
}

// order sorts candidates by policy, then by provider/account/model so the plan
// is reproducible.
func order(candidates []Candidate, accounts []Account, policy Policy, now time.Time) {
	byAccount := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		byAccount[account.Provider+"\x00"+account.ID] = account
	}
	rank := func(candidate Candidate) (int, int, float64, string) {
		account := byAccount[candidate.Provider+"\x00"+candidate.Account]
		// Resting accounts go last: they may answer, but a healthy one should.
		health := 0
		if account.Health == Resting {
			health = 1
		}
		expiring := 0
		if policy.PreferExpiringSoon && account.Subscription.ExpiresWithin(now, policy.ExpiringWithin) {
			expiring = 1
		}
		cost := 0.0
		if policy.PreferLowestCost {
			cost = candidate.Cost
		}
		return health, -expiring, cost, candidate.Provider + "\x00" + candidate.Account + "\x00" + candidate.Model
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		hi, ei, ci, ki := rank(candidates[i])
		hj, ej, cj, kj := rank(candidates[j])
		if policy.KeepSession {
			// The conversation's account wins before any price or expiry rule:
			// changing it throws away the vendor's cache.
			iSame := candidates[i].Account == policy.SessionAccount
			jSame := candidates[j].Account == policy.SessionAccount
			if iSame != jSame {
				return iSame
			}
		}
		if hi != hj {
			return hi < hj
		}
		if ei != ej {
			return ei < ej
		}
		if ci != cj {
			return ci < cj
		}
		return ki < kj
	})
}

func unusableReason(account Account, now time.Time) string {
	switch {
	case account.Health == Blocked:
		return "the sign-in needs attention"
	case account.Subscription.Expired(now):
		return "the subscription has ended"
	}
	return "not usable"
}

// Explain renders a plan for a log or a UI line: what was chosen and why the
// rest were not.
func (p Plan) Explain() string {
	var out strings.Builder
	if len(p.Candidates) == 0 {
		out.WriteString("no candidate can answer this request")
	} else {
		first := p.Candidates[0]
		fmt.Fprintf(&out, "%s/%s on %s", first.Provider, first.Model, first.Account)
		if len(p.Candidates) > 1 {
			fmt.Fprintf(&out, " (+%d more)", len(p.Candidates)-1)
		}
	}
	if len(p.Rejections) > 0 {
		counts := map[string]int{}
		for _, rejection := range p.Rejections {
			counts[rejection.Reason]++
		}
		reasons := make([]string, 0, len(counts))
		for reason := range counts {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		parts := make([]string, 0, len(reasons))
		for _, reason := range reasons {
			parts = append(parts, fmt.Sprintf("%s ×%d", reason, counts[reason]))
		}
		fmt.Fprintf(&out, "; left out: %s", strings.Join(parts, ", "))
	}
	return out.String()
}
