package aiapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

type testPolicy func(RouteInput) (RouteDecision, error)

func (testPolicy) APIVersion() string                                               { return ExtensionAPIVersion }
func (f testPolicy) Select(_ context.Context, in RouteInput) (RouteDecision, error) { return f(in) }

type testMasker func(json.RawMessage) (json.RawMessage, MaskSession, error)

func (testMasker) APIVersion() string { return ExtensionAPIVersion }
func (f testMasker) MaskRequest(_ context.Context, _ Protocol, body json.RawMessage) (json.RawMessage, MaskSession, error) {
	return f(body)
}

type testObserver func(UsageEvent)

func (testObserver) APIVersion() string                            { return ExtensionAPIVersion }
func (f testObserver) OnUsage(_ context.Context, event UsageEvent) { f(event) }

type testTransport func(*host.Request) (*host.Response, error)

func (f testTransport) Do(_ context.Context, r *host.Request) (*host.Response, error) { return f(r) }

type testStream func(*host.Request, func(*host.Response) error, func([]byte) error) (*host.Response, error)

func (f testStream) DoStream(_ context.Context, r *host.Request, head func(*host.Response) error, chunk func([]byte) error) (*host.Response, error) {
	return f(r, head, chunk)
}

func TestPipelineAppliesRouteToTransport(t *testing.T) {
	ctx := context.Background()
	used := json.Number("25")
	input := RouteInput{RequestedModel: "model", Candidates: []RouteCandidate{
		{ID: "first", AccountID: "a", Model: "model", Protocol: Chat, Quota: &QuotaSnapshot{UsedPercent: &used}},
		{ID: "second", AccountID: "b", Model: "model", Protocol: Chat},
		{ID: "different", AccountID: "b", Model: "other", Protocol: Chat},
	}}
	defaults, err := ResolveRoute(ctx, nil, input)
	if err != nil || !reflect.DeepEqual(defaults.OrderedIDs, []string{"first", "second", "different"}) {
		t.Fatalf("default order = %+v, %v", defaults, err)
	}
	order := []string{"second", "first"}
	policy := testPolicy(func(in RouteInput) (RouteDecision, error) {
		if in.APIVersion != ExtensionAPIVersion || in.Candidates[1].Quota != nil {
			t.Fatalf("policy snapshot = %+v", in)
		}
		*in.Candidates[0].Quota.UsedPercent = "0"
		return RouteDecision{OrderedIDs: order}, nil
	})
	for range 2 {
		decision, err := ResolveRoute(ctx, policy, input)
		if err != nil || !reflect.DeepEqual(decision.OrderedIDs, order) || used != "25" {
			t.Fatalf("decision = %+v, %v; original quota = %s", decision, err, used)
		}
	}

	calls := 0
	p := Pipeline{Extensions: Extensions{RoutePolicy: policy}, Transport: testTransport(func(req *host.Request) (*host.Response, error) {
		calls++
		if req.Cookies || req.Header.Get("Authorization") != "Bearer mock-selected" || req.URL != "https://b.test/chat/completions" {
			t.Fatal("request did not use the selected account with cookies disabled")
		}
		if !strings.Contains(string(req.Body), `"model":"other"`) || !strings.Contains(string(req.Body), `9007199254740993`) {
			t.Fatalf("routed body = %s", req.Body)
		}
		return &host.Response{Status: 200, Body: []byte(`{"usage":{"completion_tokens":3}}`)}, nil
	})}
	meta := RequestMeta{Route: &input, RequestID: "request", ResolveConfig: func(_ context.Context, candidate RouteCandidate) (Config, error) {
		if candidate.ID != "different" {
			t.Fatalf("unexpected candidate %q", candidate.ID)
		}
		return Config{Protocol: Chat, APIKey: "mock-selected", BaseURL: "https://b.test"}, nil
	}}
	order = []string{"different"}
	body := json.RawMessage(`{"model":"model","messages":[],"seed":9007199254740993}`)
	input.ExplicitModel = true
	if _, err := p.Do(ctx, Config{Protocol: Chat}, body, meta); err == nil || calls > 0 {
		t.Fatalf("allowed explicit model override: %v, %d calls", err, calls)
	}
	input.ExplicitModel = false
	res, err := p.Do(ctx, Config{Protocol: Chat}, body, meta)
	if err != nil || calls != 1 || res.UsageEvent.AccountID != "b" || res.UsageEvent.Model != "other" {
		t.Fatalf("routed result = %+v, %v, calls = %d", res, err, calls)
	}
}

const originalText = "mock private text"
const placeholder = "[MASK_PRIVATE]"

type testMaskSession struct{ pending map[StreamEventKind]string }

func (s *testMaskSession) RestoreBody(raw json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(strings.ReplaceAll(string(raw), placeholder, originalText)), nil
}

func (s *testMaskSession) RestoreDelta(kind StreamEventKind, text string, final bool) (string, error) {
	acc := s.pending[kind] + text
	if acc != "" && len(acc) < len(placeholder) && strings.HasPrefix(placeholder, acc) {
		if final {
			return "", errors.New("incomplete placeholder")
		}
		s.pending[kind] = acc
		return "", nil
	}
	delete(s.pending, kind)
	return strings.ReplaceAll(acc, placeholder, originalText), nil
}

func TestPipelineRestoresWholeAndInterleavedStreamDeltas(t *testing.T) {
	cfg := Config{Protocol: Chat, APIKey: "mock-key"}
	var seen []UsageEvent
	p := Pipeline{Extensions: Extensions{
		Masker: testMasker(func(body json.RawMessage) (json.RawMessage, MaskSession, error) {
			return json.RawMessage(strings.ReplaceAll(string(body), originalText, placeholder)), &testMaskSession{pending: make(map[StreamEventKind]string)}, nil
		}),
		UsageObservers: []UsageObserver{
			testObserver(func(event UsageEvent) { *event.Usage.OutputTokens = "999"; panic("observer panic") }),
			testObserver(func(event UsageEvent) { seen = append(seen, event) }),
		},
	}}
	checkRequest := func(req *host.Request) {
		if req.Cookies || strings.Contains(string(req.Body), originalText) || !strings.Contains(string(req.Body), placeholder) {
			t.Fatal("expected a masked request with cookies disabled")
		}
	}
	p.Transport = testTransport(func(req *host.Request) (*host.Response, error) {
		checkRequest(req)
		return &host.Response{Status: 200, Body: []byte(`{"choices":[{"message":{"content":"[MASK_PRIVATE]"},"finish_reason":"stop"}],"usage":{"completion_tokens":0,"extra":"untrusted"}}`)}, nil
	})
	body := json.RawMessage(`{"model":"model","stream":true,"messages":[{"role":"user","content":"mock private text"}]}`)
	res, err := p.Do(context.Background(), cfg, body)
	if err != nil || res.Text != originalText || !json.Valid(res.Body) {
		t.Fatalf("whole result = %+v, %v", res, err)
	}
	p.StreamTransport = testStream(func(req *host.Request, head func(*host.Response) error, chunk func([]byte) error) (*host.Response, error) {
		checkRequest(req)
		response := &host.Response{Status: 200}
		if err := head(response); err != nil {
			return response, err
		}
		for _, frame := range []string{
			`{"choices":[{"delta":{"content":"[MASK_"}}]}`,
			`{"choices":[{"delta":{"reasoning":"[MASK_"}}]}`,
			`{"choices":[{"delta":{"content":"PRIVATE]"}}]}`,
			`{"choices":[{"delta":{"reasoning":"PRIVATE]"},"finish_reason":"stop"}]}`,
			`{"usage":{"completion_tokens":0}}`, "[DONE]",
		} {
			if err := chunk([]byte("data: " + frame + "\n\n")); err != nil {
				return response, err
			}
		}
		return response, nil
	})
	var events []StreamEvent
	res, err = p.DoStream(context.Background(), cfg, body, func(e StreamEvent) error { events = append(events, e); return nil })
	if err != nil || res.Text != originalText || res.Reasoning != originalText || len(events) != 4 || events[0].Text != originalText {
		t.Fatalf("stream result = %+v, events = %+v, %v", res, events, err)
	}
	if len(seen) != 2 || !seen[1].Streamed {
		t.Fatalf("usage events = %+v", seen)
	}
	for _, event := range seen {
		if event.APIVersion != ExtensionAPIVersion || event.Usage.InputTokens != nil || event.Usage.OutputTokens.String() != "0" || event.Usage.Raw != nil {
			t.Fatalf("usage event = %+v", event)
		}
	}
}

func TestMaskingFailsClosedBeforeTransport(t *testing.T) {
	body := json.RawMessage(`{"model":"model","stream":true,"messages":[]}`)
	for _, masker := range []testMasker{
		func(json.RawMessage) (json.RawMessage, MaskSession, error) { return nil, nil, errors.New(originalText) },
		func(json.RawMessage) (json.RawMessage, MaskSession, error) { panic(originalText) },
		func(json.RawMessage) (json.RawMessage, MaskSession, error) {
			return []byte(`{"model":"model"}`), &testMaskSession{}, nil
		},
		func(raw json.RawMessage) (json.RawMessage, MaskSession, error) { return raw, nil, nil },
	} {
		p := Pipeline{Extensions: Extensions{Masker: masker},
			Transport: testTransport(func(*host.Request) (*host.Response, error) {
				t.Fatal("whole request reached transport")
				return nil, nil
			}),
			StreamTransport: testStream(func(*host.Request, func(*host.Response) error, func([]byte) error) (*host.Response, error) {
				t.Fatal("stream reached transport")
				return nil, nil
			}),
		}
		cfg := Config{Protocol: Chat, APIKey: "mock-key"}
		_, wholeErr := p.Do(context.Background(), cfg, body)
		_, streamErr := p.DoStream(context.Background(), cfg, body, nil)
		for _, err := range []error{wholeErr, streamErr} {
			var pe *ProviderError
			if !errors.As(err, &pe) || pe.Code != ErrCodeMaskingFailed || strings.Contains(err.Error(), originalText) {
				t.Fatalf("expected safe masking error, got %v", err)
			}
		}
	}
}

func TestLateHTTPErrorNeverDeliversStreamEvents(t *testing.T) {
	p := Pipeline{StreamTransport: testStream(func(_ *host.Request, head func(*host.Response) error, chunk func([]byte) error) (*host.Response, error) {
		if err := chunk([]byte(`{"error":{"code":"insufficient_quota","message":"mock credential"}}`)); err != nil {
			return nil, err
		}
		response := &host.Response{Status: 429, Header: http.Header{"Retry-After": {"7"}}}
		return response, head(response)
	})}
	result, err := p.DoStream(context.Background(), Config{Protocol: Chat, APIKey: "mock-key"}, []byte(`{"model":"model","messages":[],"stream":true}`), func(StreamEvent) error {
		t.Fatal("HTTP failure passed a stream event to the caller")
		return nil
	})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Code != ErrCodeQuotaExhausted || pe.Retryable || pe.RetryAfter == nil || *pe.RetryAfter != 7 || result.UsageEvent.Usage != nil {
		t.Fatalf("result = %+v, %v", result, err)
	}
}
