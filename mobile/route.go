package mobile

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/laincat/veneracat-core/magpierouting"
)

// routeInput is what a client sends to PlanRoute: the accounts it has, the models
// those accounts' providers report, the request's requirements, and the user's
// policy. Everything the decision needs is here, including the time, so the same
// input always gives the same plan.
type routeInput struct {
	// Now is Unix milliseconds. Zero means the client did not pass one, which is
	// refused rather than filled in: a plan that depends on the machine clock
	// cannot be reproduced.
	Now      int64          `json:"now"`
	Accounts []routeAccount `json:"accounts"`
	Models   []routeModel   `json:"models"`
	Request  routeRequest   `json:"request"`
	Policy   routePolicy    `json:"policy"`
}

type routeAccount struct {
	ID           string       `json:"id"`
	Provider     string       `json:"provider"`
	Health       string       `json:"health"`
	RetryAt      int64        `json:"retryAt"`
	Subscription routePlan    `json:"subscription"`
	Allowances   []routeQuota `json:"allowances"`
}

type routePlan struct {
	ExpiresAt int64 `json:"expiresAt"`
	AutoRenew bool  `json:"autoRenew"`
}

type routeQuota struct {
	Used    *float64 `json:"used"`
	Limit   *float64 `json:"limit"`
	ResetAt int64    `json:"resetAt"`
	Models  []string `json:"models"`
}

type routeModel struct {
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	Vision    bool     `json:"vision"`
	Tools     bool     `json:"tools"`
	Reasoning bool     `json:"reasoning"`
	Context   int64    `json:"context"`
	Dialects  []string `json:"dialects"`
	Cost      float64  `json:"cost"`
}

type routeRequest struct {
	Vision     bool   `json:"vision"`
	Tools      bool   `json:"tools"`
	Reasoning  bool   `json:"reasoning"`
	MinContext int64  `json:"minContext"`
	Dialect    string `json:"dialect"`
	Difficulty string `json:"difficulty"`
	Model      string `json:"model"`
	Account    string `json:"account"`
}

type routePolicy struct {
	PreferExpiringSoon bool                 `json:"preferExpiringSoon"`
	ExpiringWithinSecs int64                `json:"expiringWithinSecs"`
	PreferLowestCost   bool                 `json:"preferLowestCost"`
	KeepSession        bool                 `json:"keepSession"`
	Tiers              map[string]routeTier `json:"tiers"`
}

type routeTier struct {
	Models    []string `json:"models"`
	Providers []string `json:"providers"`
}

// routePlanOut is the answer: the candidates in order, the rejections with their
// reasons, and one sentence a UI can show.
type routePlanOut struct {
	Candidates []routeCandidateOut `json:"candidates"`
	Rejections []routeRejectionOut `json:"rejections"`
	Explain    string              `json:"explain"`
}

type routeCandidateOut struct {
	Provider string  `json:"provider"`
	Account  string  `json:"account"`
	Model    string  `json:"model"`
	Cost     float64 `json:"cost"`
}

type routeRejectionOut struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Model    string `json:"model"`
	Reason   string `json:"reason"`
}

// PlanRoute chooses the account and model that should answer a request.
//
// It takes no network and no clock of its own: the accounts, the models their
// providers reported, and now all come from the client. Without that, a plan
// could not be explained after the fact or compared between runs.
func (m *Magpie) PlanRoute(inputJSON string) (string, error) {
	var in routeInput
	if err := json.Unmarshal([]byte(inputJSON), &in); err != nil {
		return "", fmt.Errorf("mobile: route input: %w", err)
	}
	if in.Now == 0 {
		return "", fmt.Errorf("mobile: route input has no now: a plan needs the caller's time")
	}
	now := time.UnixMilli(in.Now).UTC()

	accounts := make([]magpierouting.Account, 0, len(in.Accounts))
	for _, a := range in.Accounts {
		if a.ID == "" || a.Provider == "" {
			return "", fmt.Errorf("mobile: route account needs an id and a provider")
		}
		account := magpierouting.Account{
			ID:       a.ID,
			Provider: a.Provider,
			Health:   healthOf(a.Health),
			Subscription: magpierouting.Subscription{
				AutoRenew: a.Subscription.AutoRenew,
			},
		}
		if a.RetryAt > 0 {
			account.RetryAt = time.UnixMilli(a.RetryAt).UTC()
		}
		if a.Subscription.ExpiresAt > 0 {
			account.Subscription.ExpiresAt = time.UnixMilli(a.Subscription.ExpiresAt).UTC()
		}
		for _, quota := range a.Allowances {
			allowance := magpierouting.Allowance{Used: quota.Used, Limit: quota.Limit, Models: quota.Models}
			if quota.ResetAt > 0 {
				allowance.ResetAt = time.UnixMilli(quota.ResetAt).UTC()
			}
			account.Allowances = append(account.Allowances, allowance)
		}
		accounts = append(accounts, account)
	}

	catalogue := make(map[string]magpierouting.Candidate, len(in.Models))
	for _, model := range in.Models {
		if model.Provider == "" || model.Model == "" {
			return "", fmt.Errorf("mobile: route model needs a provider and an id")
		}
		catalogue[model.Model] = magpierouting.Candidate{
			Provider: model.Provider,
			Model:    model.Model,
			Capability: magpierouting.Capability{
				Vision: model.Vision, Tools: model.Tools, Reasoning: model.Reasoning,
				Context: model.Context, Dialects: model.Dialects,
			},
			Cost: model.Cost,
		}
	}

	policy := magpierouting.Policy{
		PreferExpiringSoon: in.Policy.PreferExpiringSoon,
		ExpiringWithin:     time.Duration(in.Policy.ExpiringWithinSecs) * time.Second,
		PreferLowestCost:   in.Policy.PreferLowestCost,
		KeepSession:        in.Policy.KeepSession,
		SessionAccount:     in.Request.Account,
		Tiers:              map[magpierouting.Difficulty]magpierouting.Tier{},
	}
	for name, tier := range in.Policy.Tiers {
		policy.Tiers[difficultyOf(name)] = magpierouting.Tier{Models: tier.Models, Providers: tier.Providers}
	}

	plan := magpierouting.Build(magpierouting.Request{
		Needs: magpierouting.Needs{
			Vision: in.Request.Vision, Tools: in.Request.Tools, Reasoning: in.Request.Reasoning,
			MinContext: in.Request.MinContext, Dialect: in.Request.Dialect,
		},
		Difficulty: difficultyOf(in.Request.Difficulty),
		Model:      in.Request.Model,
		Account:    in.Request.Account,
	}, accounts, catalogue, policy, now)

	out := routePlanOut{Candidates: []routeCandidateOut{}, Rejections: []routeRejectionOut{}, Explain: plan.Explain()}
	for _, candidate := range plan.Candidates {
		out.Candidates = append(out.Candidates, routeCandidateOut{Provider: candidate.Provider,
			Account: candidate.Account, Model: candidate.Model, Cost: candidate.Cost})
	}
	for _, rejection := range plan.Rejections {
		out.Rejections = append(out.Rejections, routeRejectionOut{Provider: rejection.Provider,
			Account: rejection.Account, Model: rejection.Model, Reason: rejection.Reason})
	}
	return marshalJSON(out)
}

func healthOf(value string) magpierouting.Health {
	switch value {
	case "blocked":
		return magpierouting.Blocked
	case "resting":
		return magpierouting.Resting
	}
	return magpierouting.Healthy
}

func difficultyOf(value string) magpierouting.Difficulty {
	switch value {
	case "simple":
		return magpierouting.Simple
	case "complex":
		return magpierouting.Complex
	}
	return magpierouting.Normal
}
