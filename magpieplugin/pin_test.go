package magpieplugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func pinnedEndpoints() []Endpoint {
	return []Endpoint{
		{Name: "key-a", URL: "https://a.invalid", Dialect: DialectChat, Account: "acct-1", Model: "big"},
		{Name: "key-b", URL: "https://b.invalid", Dialect: DialectChat, Account: "acct-2", Model: "big"},
		{Name: "key-c", URL: "https://c.invalid", Dialect: DialectChat, Account: "acct-3", Model: "small"},
	}
}

// A conversation planned around one model must not be answered by another because
// the first account was busy: rotating the key is fine, changing the model is not.
func TestPinModelKeepsTheModelAndRotatesTheAccount(t *testing.T) {
	host := &countingHost{status: 401}
	sender := &Sender{Host: host, Pin: PinModel}
	req := &ChatRequest{Model: "big", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, err := sender.Send(context.Background(), pinnedEndpoints(), req, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if host.calls != 2 {
		t.Fatalf("host was called %d times, want 2: the small model is pinned out", host.calls)
	}
	var route *RouteError
	if !errors.As(err, &route) {
		t.Fatalf("error is %T, want *RouteError", err)
	}
	if route.Stopped != ReasonPinned || route.Pin != PinModel {
		t.Fatalf("stopped = %q pin = %q, want pinned/PinModel", route.Stopped, route.Pin)
	}
	if !strings.Contains(err.Error(), "pin") {
		t.Fatalf("message %q does not say a pin ended the walk", err.Error())
	}
}

// Rotating the address behind one credential is allowed; the credential is not.
func TestPinAccountKeepsTheCredential(t *testing.T) {
	host := &countingHost{status: 401}
	endpoints := []Endpoint{
		{Name: "mirror-1", URL: "https://m1.invalid", Dialect: DialectChat, Account: "acct-1", Model: "big"},
		{Name: "mirror-2", URL: "https://m2.invalid", Dialect: DialectChat, Account: "acct-1", Model: "big"},
		{Name: "other", URL: "https://o.invalid", Dialect: DialectChat, Account: "acct-2", Model: "big"},
	}
	sender := &Sender{Host: host, Pin: PinAccount}
	req := &ChatRequest{Model: "big", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, _ = sender.Send(context.Background(), endpoints, req, nil)
	if host.calls != 2 {
		t.Fatalf("host was called %d times, want 2: only the same account may be retried", host.calls)
	}
}

// The default is unchanged: a caller that said nothing gets the whole list, and
// its failure still reads as "no endpoint answered".
func TestNoPinTriesEveryEndpoint(t *testing.T) {
	host := &countingHost{status: 401}
	sender := &Sender{Host: host}
	req := &ChatRequest{Model: "big", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, err := sender.Send(context.Background(), pinnedEndpoints(), req, nil)
	if host.calls != 3 {
		t.Fatalf("host was called %d times, want 3", host.calls)
	}
	var route *RouteError
	if !errors.As(err, &route) {
		t.Fatalf("error is %T, want *RouteError", err)
	}
	if route.Stopped != ReasonBudget || route.Pin != PinAny {
		t.Fatalf("stopped = %q pin = %q, want budget/any", route.Stopped, route.Pin)
	}
}

// A pin on a label nobody wrote cannot be enforced. Keeping the endpoint means an
// unknown stays "not told" instead of becoming a refusal to try.
func TestPinWithNoLabelKeepsTheEndpoint(t *testing.T) {
	host := &countingHost{status: 401}
	endpoints := []Endpoint{
		{Name: "one", URL: "https://1.invalid", Dialect: DialectChat},
		{Name: "two", URL: "https://2.invalid", Dialect: DialectChat},
	}
	sender := &Sender{Host: host, Pin: PinModel}
	req := &ChatRequest{Model: "big", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, _ = sender.Send(context.Background(), endpoints, req, nil)
	if host.calls != 2 {
		t.Fatalf("host was called %d times, want 2: an unlabelled endpoint must not be dropped", host.calls)
	}
}

// The pin decides where a retry may go; it does not make a malformed request
// retryable. Both rules have to hold at once.
func TestPinDoesNotOverrideARejectedRequest(t *testing.T) {
	host := &countingHost{status: 400}
	sender := &Sender{Host: host, Pin: PinModel}
	req := &ChatRequest{Model: "big", Messages: []Message{{Role: "user", Content: "hi"}}}

	_, err := sender.Send(context.Background(), pinnedEndpoints(), req, nil)
	if host.calls != 1 {
		t.Fatalf("host was called %d times, want 1", host.calls)
	}
	var route *RouteError
	if !errors.As(err, &route) {
		t.Fatalf("error is %T, want *RouteError", err)
	}
	if route.Stopped != ReasonRejected {
		t.Fatalf("stopped = %q, want %q: a 400 stops before the pin matters", route.Stopped, ReasonRejected)
	}
}
