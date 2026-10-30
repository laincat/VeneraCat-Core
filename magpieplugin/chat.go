// Sending a conversation to whichever endpoint answers, and stopping when a
// retry would contradict what the reader already saw.
package magpieplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is one turn of a conversation. Roles are "user", "assistant" and
// "system"; a vendor that has no system turn gets its text folded into the
// first user message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is one conversation turn to send.
type ChatRequest struct {
	Model    string
	Messages []Message
	// System is separate because Anthropic Messages takes it out of band.
	System string
	// MaxTokens caps the reply; vendors disagree on whether it is required,
	// so zero leaves it out.
	MaxTokens int
	// Extra holds vendor parameters (temperature and the like) copied
	// verbatim into the body.
	Extra map[string]json.RawMessage
}

// Endpoint is one place a conversation may go: a URL the client's transport can
// reach, the headers the account needs, and the wire dialect it speaks.
type Endpoint struct {
	URL     string
	Header  map[string][]string
	Dialect Dialect
	// Name is what the client calls this endpoint in a message about it. It is
	// never sent.
	Name string
	// Account names the credential behind this endpoint. A retry may rotate it;
	// nothing else is sent, and it is only read when a pin is set.
	Account string
	// Model is what this endpoint was built to ask for. It is the label a
	// PinModel retry is allowed to keep.
	Model string
}

// Label names the endpoint in errors.
func (e Endpoint) Label() string {
	if e.Name != "" {
		return e.Name
	}
	return e.URL
}

// StatusError is an endpoint answering with a status that is not a success.
// The body is kept small: enough to recognise the vendor's complaint.
type StatusError struct {
	Endpoint Endpoint
	Status   int
	Body     string
}

func (e *StatusError) Error() string {
	body := e.Body
	if len(body) > 200 {
		body = body[:200] + "…"
	}
	if body == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Endpoint.Label(), e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Endpoint.Label(), e.Status, body)
}

// RouteError reports why the walk over endpoints ended and what the last
// failure was.
type RouteError struct {
	Tried []Endpoint
	Last  error
	// Stopped is the reason the walk ended before the endpoints ran out. Empty
	// or ReasonBudget means all of them were tried.
	Stopped Reason
	// Pin is the rule that kept other endpoints out, when one did.
	Pin Pin
}

func (e *RouteError) Error() string {
	names := make([]string, 0, len(e.Tried))
	for _, ep := range e.Tried {
		names = append(names, ep.Label())
	}
	if e.Stopped == ReasonPinned && e.Pin != PinAny {
		return fmt.Sprintf("%s (%s): only %s was allowed; last failure: %v",
			"stopped by the pin", e.Pin, strings.Join(names, ", "), e.Last)
	}
	if e.Stopped != "" && e.Stopped != ReasonBudget {
		return fmt.Sprintf("stopped after %s (%s): %v",
			strings.Join(names, ", "), e.Stopped, e.Last)
	}
	return fmt.Sprintf("no endpoint answered (%s): %v", strings.Join(names, ", "), e.Last)
}

func (e *RouteError) Unwrap() error { return e.Last }

// Sender sends conversations through the client's transport and reports the
// result as stream events.
type Sender struct {
	// Host sends the requests. When it also implements Streamer, events arrive
	// as the vendor writes them; otherwise the whole reply is read at once.
	Host Host
	// MaxEventBytes caps one stream event; zero uses the decoder's default.
	MaxEventBytes int
	// Pin limits how far a failure may wander. Zero value is PinAny.
	Pin Pin
}

// Send tries the endpoints in order until one answers, and calls onEvent for
// each piece.
//
// A failure before anything reached the reader moves to the next endpoint: a
// rejected key or a busy host is exactly what a second account is for. Once
// text or reasoning has been reported, a failure is returned with
// ErrNoRetryAfterOutput instead, because a second attempt would either repeat
// what the reader already read or contradict it.
func (s *Sender) Send(ctx context.Context, endpoints []Endpoint, req *ChatRequest, onEvent func(Event) error) (Endpoint, error) {
	if len(endpoints) == 0 {
		return Endpoint{}, errors.New("magpieplugin: no endpoint to send to")
	}
	if onEvent == nil {
		onEvent = func(Event) error { return nil }
	}
	// Before the first request, not just between them: a request nobody is
	// waiting for should not open a connection at all.
	if err := ctx.Err(); err != nil {
		return Endpoint{}, err
	}
	// The pin is applied once, against the first endpoint: it is the route the
	// caller planned, and every retry is compared with it rather than with the
	// previous failure.
	route := endpoints
	if s.Pin != PinAny && len(endpoints) > 0 {
		first := endpoints[0]
		route = make([]Endpoint, 0, len(endpoints))
		for _, ep := range endpoints {
			if s.Pin.allows(first, ep) {
				route = append(route, ep)
			}
		}
	}

	var tried []Endpoint
	var last error
	for _, ep := range route {
		body, err := req.Body(ep.Dialect)
		if err != nil {
			return ep, err
		}
		attempt := &Attempt{}
		err = s.sendOne(ctx, ep, body, attempt, onEvent)
		if err == nil {
			return ep, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ep, ctxErr
		}
		if errors.Is(err, errStopStream) {
			// The callback asked to stop, so no other endpoint should run.
			return ep, err
		}
		tried = append(tried, ep)
		last = err
		decision := Decide(err, attempt)
		if !decision.Retry {
			if decision.Reason == ReasonOutputStarted {
				// Still the sentinel callers already look for: this is the one
				// stop they have to handle themselves.
				return ep, fmt.Errorf("%w (after %s: %v)", ErrNoRetryAfterOutput, ep.Label(), err)
			}
			return ep, &RouteError{Tried: tried, Last: err, Stopped: decision.Reason}
		}
	}
	// Every endpoint that the pin allowed was tried. When the pin is what ended
	// the walk, saying so is the difference between "nothing else works" and
	// "something else was never asked".
	stopped := ReasonBudget
	if len(route) < len(endpoints) {
		stopped = ReasonPinned
	}
	return Endpoint{}, &RouteError{Tried: tried, Last: last, Stopped: stopped, Pin: s.Pin}
}

// errStopStream says the caller was done reading and asked the stream to end.
var errStopStream = errors.New("magpieplugin: stream stopped by the caller")

func (s *Sender) sendOne(ctx context.Context, ep Endpoint, body []byte, attempt *Attempt, onEvent func(Event) error) error {
	req := &Request{Method: http.MethodPost, URL: ep.URL, Header: cloneHeader(ep.Header), Body: body}
	deliver := func(r io.Reader) error {
		reader, err := NewStreamReader(r, ep.Dialect, s.MaxEventBytes)
		if err != nil {
			return err
		}
		for {
			event, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if event.Kind == "" {
				continue
			}
			attempt.Saw(event)
			if err := onEvent(event); err != nil {
				return err
			}
		}
	}

	streamer, streaming := s.Host.(Streamer)
	if !streaming {
		resp, err := s.Host.Do(ctx, req)
		if err != nil {
			return err
		}
		if resp.Status < 200 || resp.Status > 299 {
			return &StatusError{Endpoint: ep, Status: resp.Status, Body: string(resp.Body)}
		}
		return deliver(bytes.NewReader(resp.Body))
	}

	pr, pw := io.Pipe()
	var callbackErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		callbackErr = deliver(pr)
		// The reader is done, either because the stream ended or because it
		// gave up. Closing its side makes any later write fail, which is how a
		// transport still writing learns to stop.
		pr.CloseWithError(callbackErr)
	}()
	resp, err := streamer.DoStream(ctx, req, func(chunk []byte) error {
		_, werr := pw.Write(chunk)
		return werr
	})
	pw.Close()
	<-done
	switch {
	case callbackErr != nil:
		return callbackErr
	case resp != nil && (resp.Status < 200 || resp.Status > 299):
		return &StatusError{Endpoint: ep, Status: resp.Status, Body: string(resp.Body)}
	case err != nil && !errors.Is(err, io.ErrClosedPipe):
		// A closed pipe after the reader finished is not a failure: some vendors
		// keep writing after the last event we needed.
		return err
	}
	return nil
}

// Body encodes the request in a dialect's own shape.
func (r *ChatRequest) Body(d Dialect) ([]byte, error) {
	switch d {
	case DialectChat, "":
		return r.bodyChat()
	case DialectResponses:
		return r.bodyResponses()
	case DialectMessages:
		return r.bodyMessages()
	}
	return nil, fmt.Errorf("magpieplugin: unknown dialect %q", d)
}

func (r *ChatRequest) bodyChat() ([]byte, error) {
	body := struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		Stream    bool      `json:"stream"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}{Model: r.Model, Messages: r.messagesWithSystem(), Stream: true, MaxTokens: r.MaxTokens}
	return r.encode(body)
}

func (r *ChatRequest) bodyResponses() ([]byte, error) {
	// Responses takes instructions out of band and calls the user's turns
	// "input"; a single string is enough for a conversation without tools.
	body := struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions,omitempty"`
		Input        string `json:"input"`
		Stream       bool   `json:"stream"`
	}{Model: r.Model, Instructions: r.System, Input: r.prompt(), Stream: true}
	return r.encode(body)
}

func (r *ChatRequest) bodyMessages() ([]byte, error) {
	body := struct {
		Model     string    `json:"model"`
		System    string    `json:"system,omitempty"`
		Messages  []Message `json:"messages"`
		Stream    bool      `json:"stream"`
		MaxTokens int       `json:"max_tokens"`
	}{Model: r.Model, System: r.System, Messages: r.userAndAssistant(), Stream: true, MaxTokens: r.maxTokens()}
	return r.encode(body)
}

// maxTokens is what Anthropic needs; without a cap the request is refused.
func (r *ChatRequest) maxTokens() int {
	if r.MaxTokens > 0 {
		return r.MaxTokens
	}
	return 4096
}

// messagesWithSystem folds a system turn in, for vendors that have one.
func (r *ChatRequest) messagesWithSystem() []Message {
	if r.System == "" {
		return r.userAndAssistant()
	}
	out := make([]Message, 0, len(r.Messages)+1)
	out = append(out, Message{Role: "system", Content: r.System})
	return append(out, r.userAndAssistant()...)
}

// userAndAssistant drops any system turn: it travels separately.
func (r *ChatRequest) userAndAssistant() []Message {
	out := make([]Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Role == "system" {
			continue
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		out = append(out, Message{Role: role, Content: m.Content})
	}
	return out
}

// prompt flattens the conversation for an API that takes one input string.
func (r *ChatRequest) prompt() string {
	var b strings.Builder
	for _, m := range r.Messages {
		if m.Role == "system" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if m.Role == "assistant" {
			b.WriteString("Assistant: ")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

// encode merges Extra into the encoded body without disturbing key order: the
// vendor's own keys stay where the struct put them.
func (r *ChatRequest) encode(body any) ([]byte, error) {
	base, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return base, nil
	}
	extra, err := json.Marshal(r.Extra)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	var add map[string]json.RawMessage
	if err := json.Unmarshal(extra, &add); err != nil {
		return nil, err
	}
	for k, v := range add {
		if _, exists := obj[k]; exists {
			continue
		}
		obj[k] = v
	}
	return json.Marshal(obj)
}

func cloneHeader(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, vs := range in {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
