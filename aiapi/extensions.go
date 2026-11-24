package aiapi

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// ExtensionAPIVersion is the contract for routing, observation and masking.
const ExtensionAPIVersion = "aiapi-ext/1"

type QuotaSnapshot struct {
	UsedPercent *json.Number `json:"usedPercent"`
	Remaining   *json.Number `json:"remaining"`
	ResetAt     string       `json:"resetAt,omitempty"`
}

// RouteCandidate is public metadata about an account and model. IDs are opaque
// and scoped to this snapshot. Credentials remain in the caller.
type RouteCandidate struct {
	ID        string         `json:"id"`
	AccountID string         `json:"accountId,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	Protocol  Protocol       `json:"protocol"`
	Model     string         `json:"model"`
	Quota     *QuotaSnapshot `json:"quota"`
}

type RouteInput struct {
	APIVersion      string            `json:"apiVersion"`
	Protocol        Protocol          `json:"protocol,omitempty"`
	RequestedModel  string            `json:"requestedModel"`
	ExplicitModel   bool              `json:"explicitModel"`
	ExplicitAccount string            `json:"explicitAccount,omitempty"`
	Candidates      []RouteCandidate  `json:"candidates"`
	Hints           map[string]string `json:"hints,omitempty"`
}

type RouteDecision struct {
	OrderedIDs []string `json:"orderedIds"`
	SelectedID string   `json:"selectedId"`
}

// RoutePolicy ranks a snapshot. Implementations must return the same order for
// the same snapshot. Keep the caller's candidate order when scores tie and use
// Hints for any external inputs. A nil quota or count means unknown.
type RoutePolicy interface {
	APIVersion() string
	Select(context.Context, RouteInput) (RouteDecision, error)
}

// ResolveRoute applies policy or preserves eligible candidates in caller order.
func ResolveRoute(ctx context.Context, policy RoutePolicy, input RouteInput) (decision RouteDecision, err error) {
	if err := ctx.Err(); err != nil {
		return RouteDecision{}, ClassifyError(input.Protocol, err)
	}
	eligible, err := eligibleCandidates(input)
	if err != nil {
		return RouteDecision{}, err
	}
	if policy == nil {
		for _, candidate := range input.Candidates {
			if eligible[candidate.ID] {
				decision.OrderedIDs = append(decision.OrderedIDs, candidate.ID)
			}
		}
		return validateDecision(input.Protocol, eligible, decision)
	}
	defer recoverExtension(input.Protocol, ErrCodeInvalidRequest, &err)
	if policy.APIVersion() != ExtensionAPIVersion {
		return RouteDecision{}, &ProviderError{Code: ErrCodeInvalidRequest, Message: "incompatible route policy apiVersion"}
	}
	input.APIVersion = ExtensionAPIVersion
	input.Hints = maps.Clone(input.Hints)
	input.Candidates = slices.Clone(input.Candidates)
	for i, candidate := range input.Candidates {
		if candidate.Quota != nil {
			quota := *candidate.Quota
			quota.UsedPercent = cloneNumberPtr(quota.UsedPercent)
			quota.Remaining = cloneNumberPtr(quota.Remaining)
			input.Candidates[i].Quota = &quota
		}
	}
	decision, err = policy.Select(ctx, input)
	if ctx.Err() != nil {
		return RouteDecision{}, ClassifyError(input.Protocol, ctx.Err())
	}
	if err != nil {
		return RouteDecision{}, &ProviderError{Code: ErrCodeInvalidRequest, Message: "route policy failed"}
	}
	return validateDecision(input.Protocol, eligible, decision)
}

// ValidateRouteDecision checks an externally produced ranking against the
// original snapshot, including explicit model and account restrictions.
func ValidateRouteDecision(input RouteInput, decision RouteDecision) (RouteDecision, error) {
	eligible, err := eligibleCandidates(input)
	if err != nil {
		return RouteDecision{}, err
	}
	return validateDecision(input.Protocol, eligible, decision)
}

func eligibleCandidates(input RouteInput) (map[string]bool, error) {
	if input.APIVersion != "" && input.APIVersion != ExtensionAPIVersion {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "unsupported route input apiVersion"}
	}
	if input.ExplicitModel && strings.TrimSpace(input.RequestedModel) == "" {
		return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "explicit model is required"}
	}
	eligible := make(map[string]bool, len(input.Candidates))
	for _, candidate := range input.Candidates {
		_, duplicate := eligible[candidate.ID]
		if duplicate || strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Model) == "" {
			return nil, &ProviderError{Code: ErrCodeInvalidRequest, Message: "invalid or duplicate route candidate"}
		}
		eligible[candidate.ID] = (!input.ExplicitModel || candidate.Model == input.RequestedModel) &&
			(input.ExplicitAccount == "" || candidate.AccountID == input.ExplicitAccount) &&
			(input.Protocol == "" || candidate.Protocol == input.Protocol)
	}
	return eligible, nil
}

func validateDecision(protocol Protocol, eligible map[string]bool, decision RouteDecision) (RouteDecision, error) {
	ids := slices.Clone(decision.OrderedIDs)
	if len(ids) == 0 && decision.SelectedID != "" {
		ids = []string{decision.SelectedID}
	}
	if len(ids) == 0 || (decision.SelectedID != "" && decision.SelectedID != ids[0]) {
		return RouteDecision{}, &ProviderError{Code: ErrCodeInvalidRequest, Protocol: protocol, Message: "route decision has no valid first candidate"}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !eligible[id] || seen[id] {
			return RouteDecision{}, &ProviderError{Code: ErrCodeInvalidRequest, Protocol: protocol, Message: "route decision repeats a candidate or violates model, protocol or account constraints"}
		}
		seen[id] = true
	}
	return RouteDecision{OrderedIDs: ids, SelectedID: ids[0]}, nil
}

// UsageEvent describes one attempted transport call. Usage contains cumulative
// provider counts. Opaque correlation IDs must be free of secrets.
type UsageEvent struct {
	APIVersion string         `json:"apiVersion"`
	RequestID  string         `json:"requestId,omitempty"`
	AccountID  string         `json:"accountId,omitempty"`
	Provider   string         `json:"provider,omitempty"`
	Protocol   Protocol       `json:"protocol"`
	Model      string         `json:"model"`
	Streamed   bool           `json:"streamed"`
	Status     int            `json:"status,omitempty"`
	Outcome    FinishReason   `json:"outcome,omitempty"`
	Usage      *TokenUsage    `json:"usage"`
	Error      *ProviderError `json:"error,omitempty"`
}

// UsageObserver receives one terminal event per attempted standard AI request.
// Calls are synchronous and can be concurrent across requests.
type UsageObserver interface {
	APIVersion() string
	OnUsage(context.Context, UsageEvent)
}

// ContentMasker prepares the entire outbound JSON body before any transport
// call. Return a per-request session, even when no sensitive content matched.
// Implementations own the masking algorithm and may leave clean content intact.
type ContentMasker interface {
	APIVersion() string
	MaskRequest(context.Context, Protocol, json.RawMessage) (json.RawMessage, MaskSession, error)
}

// MaskSession restores a whole JSON response or deltas in text and reasoning
// independently. On final=true, flush only that kind's remaining safe text.
// Implementations buffer unresolved placeholder prefixes and reject or discard
// incomplete placeholders. Calls for a session are serial. The session must
// never send restored content to the provider.
type MaskSession interface {
	RestoreBody(json.RawMessage) (json.RawMessage, error)
	RestoreDelta(kind StreamEventKind, text string, final bool) (string, error)
}

func recoverExtension(protocol Protocol, code ErrorCode, err *error) {
	if recover() != nil {
		*err = safelyClassified(protocol, 0, code)
	}
}
