package magpieplugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDecideClassifiesTheStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		retry  bool
		reason Reason
	}{
		{"nothing failed", nil, false, ReasonCompleted},
		{"caller gone", context.Canceled, false, ReasonCancelled},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), false, ReasonCancelled},
		{"callback stopped", errStopStream, false, ReasonStoppedByCaller},
		{"bad key", &StatusError{Status: 401}, true, ReasonCredential},
		{"forbidden", &StatusError{Status: 403}, true, ReasonCredential},
		{"model missing here", &StatusError{Status: 404}, true, ReasonCredential},
		{"too fast", &StatusError{Status: 429}, true, ReasonRateLimited},
		{"timeout status", &StatusError{Status: 408}, true, ReasonRateLimited},
		{"vendor down", &StatusError{Status: 503}, true, ReasonUnavailable},
		{"no status at all", errors.New("dial tcp: connection reset"), true, ReasonUnavailable},
		{"malformed body", &StatusError{Status: 400}, false, ReasonRejected},
		{"too large", &StatusError{Status: 413}, false, ReasonRejected},
		{"unprocessable", &StatusError{Status: 422}, false, ReasonRejected},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.err, &Attempt{})
			if got.Retry != tc.retry || got.Reason != tc.reason {
				t.Fatalf("Decide(%v) = %+v, want retry=%v reason=%s", tc.err, got, tc.retry, tc.reason)
			}
		})
	}
}

func TestDecideRefusesAfterOutputEvenForARetryableStatus(t *testing.T) {
	attempt := &Attempt{}
	attempt.Saw(Event{Kind: EventText, Text: "partial answer"})

	// 503 would normally move on; once the reader has text, it must not.
	got := Decide(&StatusError{Status: 503}, attempt)
	if got.Retry || got.Reason != ReasonOutputStarted {
		t.Fatalf("Decide = %+v, want stop for output_already_shown", got)
	}
}

// A request the vendor calls malformed is malformed everywhere. Walking the rest
// of the endpoints costs the user a timeout each and cannot succeed.
func TestSendStopsOnRejectedRequest(t *testing.T) {
	host := &countingHost{status: 400}
	sender := &Sender{Host: host}
	req := &ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, err := sender.Send(context.Background(), endpoints(3), req, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if host.calls != 1 {
		t.Fatalf("host was called %d times, want 1: a 400 must not be retried", host.calls)
	}
	var route *RouteError
	if !errors.As(err, &route) {
		t.Fatalf("error is %T, want *RouteError", err)
	}
	if route.Stopped != ReasonRejected {
		t.Fatalf("Stopped = %q, want %q", route.Stopped, ReasonRejected)
	}
	if !strings.Contains(err.Error(), string(ReasonRejected)) {
		t.Fatalf("message %q does not name the reason", err.Error())
	}
}

// A refused key is what a second account exists for.
func TestSendFallsBackOnARejectedKey(t *testing.T) {
	host := &countingHost{status: 401}
	sender := &Sender{Host: host}
	req := &ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, err := sender.Send(context.Background(), endpoints(3), req, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if host.calls != 3 {
		t.Fatalf("host was called %d times, want 3", host.calls)
	}
	var route *RouteError
	if !errors.As(err, &route) {
		t.Fatalf("error is %T, want *RouteError", err)
	}
	if route.Stopped != ReasonBudget {
		t.Fatalf("Stopped = %q, want %q", route.Stopped, ReasonBudget)
	}
	// Every endpoint was tried, so the message stays the one callers know.
	if !strings.Contains(err.Error(), "no endpoint answered") {
		t.Fatalf("message %q should say every endpoint was tried", err.Error())
	}
}

// A cancelled request must not spend another vendor's quota answering nobody.
func TestSendStopsWhenTheCallerIsGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := &countingHost{status: 500}
	sender := &Sender{Host: host}
	req := &ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, err := sender.Send(ctx, endpoints(3), req, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if host.calls != 0 {
		t.Fatalf("host was called %d times with a cancelled context", host.calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error is %v, want context.Canceled", err)
	}
}

func endpoints(n int) []Endpoint {
	out := make([]Endpoint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Endpoint{Name: fmt.Sprintf("endpoint-%d", i), URL: fmt.Sprintf("https://example.invalid/%d", i), Dialect: DialectChat})
	}
	return out
}

// countingHost answers with one status and counts how often it was asked.
type countingHost struct {
	status int
	calls  int
}

func (h *countingHost) Do(ctx context.Context, req *Request) (*Response, error) {
	h.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Response{Status: h.status, Body: []byte(`{"error":"no"}`)}, nil
}

var _ Host = (*countingHost)(nil)
