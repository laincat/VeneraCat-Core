package magpieplugin

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host/sse"
)

// ProviderError and ErrorCode are shared with the standard AI entry points.
type ProviderError = aiapi.ProviderError
type ErrorCode = aiapi.ErrorCode

// EventKind identifies a streamed reply event.
type EventKind string

const (
	EventText      EventKind = "text"
	EventReasoning EventKind = "reasoning"
	EventDone      EventKind = "done"
	EventError     EventKind = "error"
)

// Event is a provider reply delta or terminal event.
type Event struct {
	Kind    EventKind
	Text    string
	Finish  string
	Outcome FinishReason
	Usage   *Usage
	Error   *ProviderError
}

// Usage is the legacy Go token accounting shape. Empty numbers are unknown.
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
	DialectChat      Dialect = "chat"
	DialectResponses Dialect = "responses"
	DialectMessages  Dialect = "messages"
)

// ErrStreamEnded reports reading past the end of a stream.
var ErrStreamEnded = errors.New("magpieplugin: the stream has ended")

// StreamReader turns SSE read from a caller's reader into provider events.
type StreamReader struct {
	framer  *sse.Decoder
	decoder *aiapi.StreamDecoder
	pending []aiapi.StreamEvent
	ended   bool
}

func NewStreamReader(r io.Reader, dialect Dialect, maxEventBytes int) (*StreamReader, error) {
	decoder, err := aiapi.NewStreamDecoder(aiapi.Protocol(dialect))
	if err != nil {
		return nil, err
	}
	framer, err := sse.NewDecoder(r, maxEventBytes)
	if err != nil {
		return nil, err
	}
	return &StreamReader{framer: framer, decoder: decoder}, nil
}

// Next returns the next event. Some existing packages close the body after
// the finish or usage frame, so their EOF also completes a reply with that data.
func (s *StreamReader) Next() (Event, error) {
	for !s.ended {
		if len(s.pending) > 0 {
			next := s.pending[0]
			s.pending = s.pending[1:]
			switch next.Kind {
			case aiapi.StreamEventText, aiapi.StreamEventReasoning:
				return Event{Kind: EventKind(next.Kind), Text: next.Text}, nil
			case aiapi.StreamEventDone:
				return s.completion(), nil
			case aiapi.StreamEventError:
				s.ended = true
				return Event{Kind: EventError, Text: next.Text, Error: next.Error}, nil
			}
			continue
		}
		raw, err := s.framer.Next()
		if errors.Is(err, io.EOF) && (s.decoder.FinishReason() != "" || s.decoder.Usage() != nil) {
			return s.completion(), nil
		}
		if err != nil {
			return Event{}, err
		}
		s.pending, err = s.decoder.DecodeEvent(raw)
		if err != nil {
			return Event{}, err
		}
	}
	return Event{}, io.EOF
}

func (s *StreamReader) completion() Event {
	s.ended = true
	out := Event{Kind: EventDone, Finish: s.decoder.FinishReason(), Outcome: s.decoder.Outcome()}
	if usage := s.decoder.Usage(); usage != nil {
		value := func(n *json.Number) json.Number {
			if n == nil {
				return ""
			}
			return *n
		}
		out.Usage = &Usage{Input: value(usage.InputTokens), Output: value(usage.OutputTokens),
			CacheRead: value(usage.CacheReadTokens), CacheWrite: value(usage.CacheWriteTokens), Raw: usage.Raw}
	}
	return out
}
