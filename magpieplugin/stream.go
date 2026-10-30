// Stream events from a provider to our own message model. A provider plugin
// speaks whichever API its vendor does; this turns the three dialects Magpie
// serves into the same small set of events, so the client never parses a
// vendor's JSON.
package magpieplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/laincat/veneracat-core/host/sse"
)

// EventKind is what a streamed piece says.
type EventKind string

const (
	// EventText is assistant text.
	EventText EventKind = "text"
	// EventReasoning is thinking the vendor streamed separately.
	EventReasoning EventKind = "reasoning"
	// EventDone ends the message; Text and Reason carry what is known.
	EventDone EventKind = "done"
	// EventError is a vendor error inside a successful response.
	EventError EventKind = "error"
)

// Event is one piece of a streamed reply.
type Event struct {
	Kind EventKind
	// Text is this piece's text, or an error's message.
	Text string
	// Finish is why the message ended, in the vendor's own words: "stop",
	// "completed" and "end_turn" all mean the same thing.
	Finish string
	// Outcome is the same ending in this core's vocabulary, so a caller does not
	// carry one table per dialect.
	Outcome FinishReason
	// Usage is the token count the vendor reported, when it reported one.
	Usage *Usage
}

// Usage is a vendor's token accounting. Numbers are kept as json.Number so a
// large count is not rounded through a float.
type Usage struct {
	Input      json.Number
	Output     json.Number
	CacheRead  json.Number
	CacheWrite json.Number
	Raw        json.RawMessage
}

// Dialect is the wire API a provider speaks.
type Dialect string

const (
	DialectChat      Dialect = "chat"      // OpenAI Chat Completions
	DialectResponses Dialect = "responses" // OpenAI Responses
	DialectMessages  Dialect = "messages"  // Anthropic Messages
)

// ErrStreamEnded reports reading past the end of a stream.
var ErrStreamEnded = errors.New("magpieplugin: the stream has ended")

// StreamReader turns one streamed reply into events. It reads the caller's
// reader through host/sse; it opens nothing.
type StreamReader struct {
	decoder *sse.Decoder
	dialect Dialect
	ended   bool
	// A chat chunk can carry the last text and the ending together. The text goes
	// out with that event; the ending waits here so it is not lost in a chunk the
	// caller has already been given.
	pendingFinish string
	pendingUsage  *Usage
}

// NewStreamReader reads dialect's events from r. maxEventBytes bounds one event
// block, as the client's own limit does.
func NewStreamReader(r io.Reader, dialect Dialect, maxEventBytes int) (*StreamReader, error) {
	switch dialect {
	case DialectChat, DialectResponses, DialectMessages:
	default:
		return nil, fmt.Errorf("magpieplugin: unknown dialect %q", dialect)
	}
	d, err := sse.NewDecoder(r, maxEventBytes)
	if err != nil {
		return nil, err
	}
	return &StreamReader{decoder: d, dialect: dialect}, nil
}

// Next returns the next event. A vendor's own end marker produces one EventDone
// and then io.EOF; an unreadable event is an error naming the dialect, never a
// silent skip.
func (s *StreamReader) Next() (Event, error) {
	for {
		if s.pendingFinish != "" {
			finish, usage := s.pendingFinish, s.pendingUsage
			s.pendingFinish, s.pendingUsage = "", nil
			s.ended = true
			return Event{Kind: EventDone, Finish: finish,
				Outcome: NormalizeFinish(s.dialect, finish), Usage: usage}, nil
		}
		if s.ended {
			return Event{}, io.EOF
		}
		raw, err := s.decoder.Next()
		if errors.Is(err, io.EOF) {
			// The stream ended without a marker after a chunk that carried its
			// own ending: report it rather than losing it.
			if s.pendingFinish != "" {
				continue
			}
			return Event{}, err
		}
		if err != nil {
			return Event{}, err
		}
		data := strings.TrimSpace(raw.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			s.ended = true
			return Event{Kind: EventDone}, nil
		}
		event, done, err := s.parse(raw.Type, data)
		if err != nil {
			return Event{}, err
		}
		if done {
			s.ended = true
		}
		if event.Kind == "" && !done {
			continue
		}
		return event, nil
	}
}

// parse maps one vendor event. done reports that the vendor ended the message.
func (s *StreamReader) parse(eventType, data string) (Event, bool, error) {
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return Event{}, false, fmt.Errorf("magpieplugin: %s event: %w", s.dialect, err)
	}
	var event Event
	var done bool
	var err error
	switch s.dialect {
	case DialectChat:
		event, done, err = parseChat(payload)
		if !done && event.Kind == EventText {
			// The chunk that carried the last text may also carry the ending.
			// Sending the text now and the ending next keeps both: dropping the
			// ending here would make a reply cut short by the token limit look
			// like a reply that finished.
			if finish := chatFinish(payload); finish != "" {
				s.pendingFinish = finish
				s.pendingUsage = usageOf(payload["usage"])
			}
		}
	case DialectResponses:
		event, done, err = parseResponses(payload, eventType)
	default:
		event, done, err = parseMessages(payload, eventType)
	}
	// One place, because the dialects disagree and each parser only knows its
	// own: Chat says "stop", Responses "completed", Anthropic "end_turn".
	if event.Finish != "" {
		event.Outcome = NormalizeFinish(s.dialect, event.Finish)
	}
	return event, done, err
}

// chatFinish reads the ending out of a chat chunk without consuming it.
func chatFinish(payload map[string]json.RawMessage) string {
	var choices []map[string]json.RawMessage
	if raw, ok := payload["choices"]; ok {
		if err := json.Unmarshal(raw, &choices); err != nil || len(choices) == 0 {
			return ""
		}
	}
	if len(choices) == 0 {
		return ""
	}
	return stringOf(choices[0]["finish_reason"])
}

func parseChat(payload map[string]json.RawMessage) (Event, bool, error) {
	if raw, ok := payload["error"]; ok {
		return Event{Kind: EventError, Text: messageOf(raw)}, false, nil
	}
	var choices []map[string]json.RawMessage
	if raw, ok := payload["choices"]; ok {
		if err := json.Unmarshal(raw, &choices); err != nil {
			return Event{}, false, fmt.Errorf("magpieplugin: chat choices: %w", err)
		}
	}
	if len(choices) > 0 {
		choice := choices[0]
		if raw, ok := choice["delta"]; ok {
			var delta map[string]json.RawMessage
			if err := json.Unmarshal(raw, &delta); err == nil {
				if text := stringOf(delta["content"]); text != "" {
					return Event{Kind: EventText, Text: text}, false, nil
				}
			}
		}
		if text := stringOf(choice["text"]); text != "" {
			return Event{Kind: EventText, Text: text}, false, nil
		}
		if finish := stringOf(choice["finish_reason"]); finish != "" {
			return Event{Kind: EventDone, Finish: finish, Usage: usageOf(payload["usage"])}, true, nil
		}
	}
	if usage := usageOf(payload["usage"]); usage != nil {
		return Event{Kind: EventDone, Usage: usage}, true, nil
	}
	return Event{}, false, nil
}

func parseResponses(payload map[string]json.RawMessage, eventType string) (Event, bool, error) {
	kind := stringOf(payload["type"])
	if kind == "" {
		kind = eventType
	}
	switch kind {
	case "response.output_text.delta":
		return Event{Kind: EventText, Text: stringOf(payload["delta"])}, false, nil
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return Event{Kind: EventReasoning, Text: stringOf(payload["delta"])}, false, nil
	case "response.completed", "response.incomplete":
		event := Event{Kind: EventDone}
		if raw, ok := payload["response"]; ok {
			var response map[string]json.RawMessage
			if json.Unmarshal(raw, &response) == nil {
				event.Usage = usageOf(response["usage"])
				event.Finish = stringOf(response["status"])
			}
		}
		return event, true, nil
	case "response.failed", "error":
		return Event{Kind: EventError, Text: messageOf(payload["error"])}, false, nil
	}
	return Event{}, false, nil
}

func parseMessages(payload map[string]json.RawMessage, eventType string) (Event, bool, error) {
	kind := stringOf(payload["type"])
	if kind == "" {
		kind = eventType
	}
	switch kind {
	case "content_block_delta":
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(payload["delta"], &delta); err != nil {
			return Event{}, false, nil
		}
		switch stringOf(delta["type"]) {
		case "text_delta":
			return Event{Kind: EventText, Text: stringOf(delta["text"])}, false, nil
		case "thinking_delta":
			return Event{Kind: EventReasoning, Text: stringOf(delta["thinking"])}, false, nil
		}
		return Event{}, false, nil
	case "message_delta":
		var delta map[string]json.RawMessage
		_ = json.Unmarshal(payload["delta"], &delta)
		finish := stringOf(delta["stop_reason"])
		usage := usageOf(payload["usage"])
		return Event{Kind: EventDone, Finish: finish, Usage: usage}, true, nil
	case "message_stop":
		return Event{Kind: EventDone}, true, nil
	case "error":
		return Event{Kind: EventError, Text: messageOf(payload["error"])}, false, nil
	}
	return Event{}, false, nil
}

func usageOf(raw json.RawMessage) *Usage {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return nil
	}
	usage := &Usage{Raw: append(json.RawMessage(nil), raw...)}
	pick := func(dst *json.Number, names ...string) {
		for _, name := range names {
			if v, ok := fields[name]; ok {
				*dst = numberOf(v)
				return
			}
		}
	}
	pick(&usage.Input, "prompt_tokens", "input_tokens")
	pick(&usage.Output, "completion_tokens", "output_tokens")
	pick(&usage.CacheRead, "cache_read_input_tokens")
	pick(&usage.CacheWrite, "cache_creation_input_tokens")
	return usage
}

func numberOf(raw json.RawMessage) json.Number {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var n json.Number
	if err := dec.Decode(&n); err != nil {
		return ""
	}
	return n
}

func stringOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// messageOf reads an error's message from the shapes vendors use: a string, or
// an object with message.
func messageOf(raw json.RawMessage) string {
	if s := stringOf(raw); s != "" {
		return s
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		if s := stringOf(fields["message"]); s != "" {
			return s
		}
		if s := stringOf(fields["type"]); s != "" {
			return s
		}
	}
	return "the vendor reported an error"
}
