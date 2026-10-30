package magpieplugin

import (
	"context"
	"errors"
)

// Reason names why a failed attempt may or may not be handed to the next
// endpoint.
//
// It is part of the API rather than a log line on purpose: the question that
// otherwise costs an hour of reading logs is "why did this stop after the first
// endpoint when three were available", and a caller can now answer it from the
// returned error alone.
type Reason string

const (
	// ReasonCompleted means the attempt succeeded; nothing failed.
	ReasonCompleted Reason = "request_completed"
	// ReasonCancelled means the caller is gone. A cancelled request must not be
	// handed to another vendor: the person is no longer waiting for it, and
	// another attempt would spend someone else's quota answering nobody.
	ReasonCancelled Reason = "cancelled"
	// ReasonStoppedByCaller means the event callback asked the stream to end.
	ReasonStoppedByCaller Reason = "stopped_by_caller"
	// ReasonOutputStarted means the reader has already seen part of the reply.
	ReasonOutputStarted Reason = "output_already_shown"
	// ReasonRejected means the vendor refused the request itself. The same body
	// sent to the next endpoint is refused again, so trying is only slower.
	ReasonRejected Reason = "request_rejected"
	// ReasonCredential means the key or token was refused. Another account is
	// exactly what should be tried next.
	ReasonCredential Reason = "credential_rejected"
	// ReasonRateLimited means the account is out of quota or moving too fast.
	ReasonRateLimited Reason = "rate_limited"
	// ReasonUnavailable means the endpoint failed in a way that another one may
	// not: a 5xx, a timeout, or a transport error with no status at all.
	ReasonUnavailable Reason = "upstream_unavailable"
	// ReasonBudget means there was no endpoint left to try.
	ReasonBudget Reason = "no_endpoints_left"
	// ReasonPinned means the walk ended because a pin, not a failure, kept the
	// remaining endpoints out.
	ReasonPinned Reason = "pinned"
)

// Decision is the answer to "may this attempt fall back to the next endpoint,
// and why".
type Decision struct {
	Retry  bool
	Reason Reason
}

func (d Decision) String() string {
	if d.Retry {
		return "retry: " + string(d.Reason)
	}
	return "stop: " + string(d.Reason)
}

// Decide classifies a failed attempt.
//
// The order matters. A cancelled request outranks a refused key, because there
// is no reader left to serve either way; text already shown outranks a 429,
// because falling back would repeat what was read. Only then is the status
// looked at, and only then does the question become "would another endpoint do
// better".
func Decide(err error, attempt *Attempt) Decision {
	if err == nil {
		return Decision{Retry: false, Reason: ReasonCompleted}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Decision{Retry: false, Reason: ReasonCancelled}
	}
	if errors.Is(err, errStopStream) {
		return Decision{Retry: false, Reason: ReasonStoppedByCaller}
	}
	if locked := attempt.MayRetry(); locked != nil {
		return Decision{Retry: false, Reason: ReasonOutputStarted}
	}

	var status *StatusError
	if !errors.As(err, &status) {
		// No status at all: a dial failure, a reset, a body that ended early.
		// The next endpoint may well answer where this one could not.
		return Decision{Retry: true, Reason: ReasonUnavailable}
	}

	switch {
	case status.Status == 401, status.Status == 403:
		return Decision{Retry: true, Reason: ReasonCredential}
	case status.Status == 404:
		// A model id this vendor does not have is ordinary: the same model is
		// spelled differently upstream. Another endpoint is worth asking.
		return Decision{Retry: true, Reason: ReasonCredential}
	case status.Status == 408, status.Status == 429:
		return Decision{Retry: true, Reason: ReasonRateLimited}
	case status.Status >= 500:
		return Decision{Retry: true, Reason: ReasonUnavailable}
	case status.Status >= 400:
		// 400, 413, 415, 422: the request is the problem, not the vendor. Sending
		// it downstream again produces the same refusal and a slower failure.
		return Decision{Retry: false, Reason: ReasonRejected}
	default:
		// 1xx/3xx should not reach here as errors, but if one does it is not the
		// request's fault.
		return Decision{Retry: true, Reason: ReasonUnavailable}
	}
}
