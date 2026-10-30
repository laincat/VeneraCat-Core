package magpieplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// routeHost answers per URL prefix with scripted chunks, so a test can make the
// first endpoint fail and the second answer.
type routeHost struct {
	mu     sync.Mutex
	plan   map[string][]string // URL → chunks; a leading "STATUS n" makes it fail
	bodies map[string][]byte
	heads  map[string]string
}

func (h *routeHost) Do(ctx context.Context, req *Request) (*Response, error) {
	h.mu.Lock()
	chunks, ok := h.plan[req.URL]
	if h.bodies == nil {
		h.bodies = map[string][]byte{}
	}
	h.bodies[req.URL] = req.Body
	if h.heads == nil {
		h.heads = map[string]string{}
	}
	h.heads[req.URL] = authOf(req)
	h.mu.Unlock()
	if !ok {
		return &Response{Status: 404, Body: []byte("no route")}, nil
	}
	if len(chunks) > 0 && strings.HasPrefix(chunks[0], "STATUS ") {
		return &Response{Status: 500, Body: []byte(chunks[0][7:])}, nil
	}
	return &Response{Status: 200, Body: []byte(strings.Join(chunks, ""))}, nil
}

func (h *routeHost) DoStream(ctx context.Context, req *Request, onChunk func([]byte) error) (*Response, error) {
	h.mu.Lock()
	chunks, ok := h.plan[req.URL]
	if h.bodies == nil {
		h.bodies = map[string][]byte{}
	}
	h.bodies[req.URL] = req.Body
	if h.heads == nil {
		h.heads = map[string]string{}
	}
	h.heads[req.URL] = authOf(req)
	h.mu.Unlock()
	if !ok {
		return &Response{Status: 404, Body: []byte("no route")}, nil
	}
	if len(chunks) > 0 && strings.HasPrefix(chunks[0], "STATUS ") {
		return &Response{Status: 500, Body: []byte(chunks[0][7:])}, nil
	}
	head := &Response{Status: 200, Header: map[string][]string{"Content-Type": {"text/event-stream"}}, URL: req.URL}
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return head, err
		}
		// A status in the middle fails the request after the chunks before it,
		// which is how a vendor drops a connection mid-reply.
		if rest, ok := strings.CutPrefix(chunk, "STATUS "); ok {
			return &Response{Status: 500, Body: []byte(rest)}, nil
		}
		if err := onChunk([]byte(chunk)); err != nil {
			return head, err
		}
	}
	return head, nil
}

func authOf(req *Request) string {
	if vs := req.Header["Authorization"]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func collect(t *testing.T, s *Sender, endpoints []Endpoint, req *ChatRequest) (string, []Event, error) {
	t.Helper()
	var events []Event
	var text strings.Builder
	_, err := s.Send(context.Background(), endpoints, req, func(e Event) error {
		events = append(events, e)
		if e.Kind == EventText {
			text.WriteString(e.Text)
		}
		return nil
	})
	return text.String(), events, err
}

func oneTurn() *ChatRequest {
	return &ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, System: "be brief"}
}

var chatChunks = []string{
	"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n",
	"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n",
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
	"data: [DONE]\n\n",
}

// A rejected key is exactly what a second account is for: nothing reached the
// reader, so the next endpoint runs.
func TestSendFallsBackBeforeAnyOutput(t *testing.T) {
	host := &routeHost{plan: map[string][]string{
		"https://a/chat": {"STATUS unauthorized"},
		"https://b/chat": chatChunks,
	}}
	s := &Sender{Host: host}
	text, events, err := collect(t, s, []Endpoint{
		{URL: "https://a/chat", Name: "account A", Dialect: DialectChat},
		{URL: "https://b/chat", Name: "account B", Dialect: DialectChat},
	}, oneTurn())
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}
	if last := events[len(events)-1]; last.Kind != EventDone || last.Finish != "stop" {
		t.Fatalf("last event = %+v", last)
	}
}

// Once the reader has been shown text, a second endpoint would repeat or
// contradict it, so the failure is returned instead.
func TestSendRefusesToFallBackAfterOutput(t *testing.T) {
	host := &routeHost{plan: map[string][]string{
		"https://a/chat": {
			"data: {\"choices\":[{\"delta\":{\"content\":\"Half an ans\"}}]}\n\n",
			"STATUS boom",
		},
		"https://b/chat": chatChunks,
	}}
	s := &Sender{Host: host}
	text, _, err := collect(t, s, []Endpoint{
		{URL: "https://a/chat", Name: "account A", Dialect: DialectChat},
		{URL: "https://b/chat", Name: "account B", Dialect: DialectChat},
	}, oneTurn())
	if !errors.Is(err, ErrNoRetryAfterOutput) {
		t.Fatalf("error = %v", err)
	}
	if text != "Half an ans" {
		t.Fatalf("text = %q", text)
	}
	if strings.Contains(err.Error(), "account B") {
		t.Fatalf("the second endpoint ran: %v", err)
	}
}

// A stream that only ever carried empty deltas showed the reader nothing, so a
// failure after it is still retryable.
func TestSendRetriesAfterAnEmptyStream(t *testing.T) {
	host := &routeHost{plan: map[string][]string{
		"https://a/chat": {
			"data: {\"choices\":[{\"delta\":{}}]}\n\n",
			"STATUS gateway timeout",
		},
		"https://b/chat": chatChunks,
	}}
	s := &Sender{Host: host}
	text, _, err := collect(t, s, []Endpoint{
		{URL: "https://a/chat", Dialect: DialectChat},
		{URL: "https://b/chat", Dialect: DialectChat},
	}, oneTurn())
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}
}

// A client without Streamer is still correct: the whole body arrives at once.
func TestSendReadsWholeResponsesWithoutAStreamer(t *testing.T) {
	host := &wholeHost{body: strings.Join(chatChunks, "")}
	s := &Sender{Host: host}
	text, _, err := collect(t, s, []Endpoint{{URL: "https://a/chat", Dialect: DialectChat}}, oneTurn())
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}
}

type wholeHost struct{ body string }

func (h *wholeHost) Do(context.Context, *Request) (*Response, error) {
	return &Response{Status: 200, Body: []byte(h.body)}, nil
}

// A multi-byte character split across two chunks must still be one character.
func TestSendSurvivesACharacterSplitAcrossChunks(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n"
	cut := strings.Index(body, "好")
	host := &routeHost{plan: map[string][]string{
		"https://a/chat": {body[:cut+1], body[cut+1:]},
	}}
	s := &Sender{Host: host}
	text, _, err := collect(t, s, []Endpoint{{URL: "https://a/chat", Dialect: DialectChat}}, oneTurn())
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if text != "你好" {
		t.Fatalf("text = %q", text)
	}
}

// Every endpoint failing leaves nothing to show, and says which were tried.
func TestSendReportsEveryEndpointTried(t *testing.T) {
	host := &routeHost{plan: map[string][]string{
		"https://a/chat": {"STATUS down"},
		"https://b/chat": {"STATUS down too"},
	}}
	s := &Sender{Host: host}
	_, _, err := collect(t, s, []Endpoint{
		{URL: "https://a/chat", Name: "A", Dialect: DialectChat},
		{URL: "https://b/chat", Name: "B", Dialect: DialectChat},
	}, oneTurn())
	var route *RouteError
	if !errors.As(err, &route) {
		t.Fatalf("error = %v", err)
	}
	if len(route.Tried) != 2 || !strings.Contains(err.Error(), "A, B") {
		t.Fatalf("error = %v", err)
	}
}

// The callback can stop reading; that ends the stream without trying elsewhere.
func TestSendStopsWhenTheCallerStops(t *testing.T) {
	host := &routeHost{plan: map[string][]string{"https://a/chat": chatChunks}}
	s := &Sender{Host: host}
	var seen int
	_, err := s.Send(context.Background(), []Endpoint{{URL: "https://a/chat", Dialect: DialectChat}}, oneTurn(),
		func(Event) error {
			seen++
			return errStopStream
		})
	if !errors.Is(err, errStopStream) {
		t.Fatalf("error = %v", err)
	}
	if seen != 1 {
		t.Fatalf("events = %d", seen)
	}
}

// The body a vendor receives is in its own dialect, and the system turn
// travels where that vendor expects it.
func TestRequestBodyPerDialect(t *testing.T) {
	req := &ChatRequest{
		Model: "m", System: "sys", MaxTokens: 5,
		Messages: []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}},
		Extra:    map[string]json.RawMessage{"temperature": json.RawMessage("0.3")},
	}
	cases := []struct {
		dialect Dialect
		want    map[string]string
		absent  []string
	}{
		{DialectChat, map[string]string{
			"model": `"m"`, "max_tokens": "5", "temperature": "0.3", "stream": "true",
		}, nil},
		{DialectMessages, map[string]string{
			"system": `"sys"`, "max_tokens": "5", "temperature": "0.3",
		}, []string{"instructions"}},
		{DialectResponses, map[string]string{
			"instructions": `"sys"`, "input": `"hi\n\nAssistant: yo"`, "stream": "true",
		}, []string{"max_tokens"}},
	}
	for _, c := range cases {
		body, err := req.Body(c.dialect)
		if err != nil {
			t.Fatalf("%s: %v", c.dialect, err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(body, &obj); err != nil {
			t.Fatalf("%s: %v (%s)", c.dialect, err, body)
		}
		for key, want := range c.want {
			if got := string(obj[key]); got != want {
				t.Fatalf("%s: %s = %s, want %s", c.dialect, key, got, want)
			}
		}
		for _, key := range c.absent {
			if _, ok := obj[key]; ok {
				t.Fatalf("%s: unexpected %s", c.dialect, key)
			}
		}
	}

	// Anthropic refuses a request without a cap, so one is filled in.
	unbounded := &ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}
	body, err := unbounded.Body(DialectMessages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"max_tokens":4096`) {
		t.Fatalf("body = %s", body)
	}
	if _, err := unbounded.Body(Dialect("carrier-pigeon")); err == nil {
		t.Fatal("unknown dialect accepted")
	}
}

// An empty endpoint list is a programming mistake, not a vendor failure.
func TestSendNeedsAnEndpoint(t *testing.T) {
	s := &Sender{Host: &routeHost{}}
	if _, err := s.Send(context.Background(), nil, oneTurn(), nil); err == nil {
		t.Fatal("no endpoints accepted")
	}
}
