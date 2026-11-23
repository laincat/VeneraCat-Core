package aiapi

// Reading a streamed answer.
//
// The three protocols all use server-sent events, but they disagree on what an
// event means: Chat Completions puts the next words in choices[0].delta.content,
// Responses emits output_text.delta events, and Messages nests a text_delta in
// content_block_delta. Only the text is extracted here; tool calls, reasoning
// blocks and usage are not returned by this decoder.
//
// A network chunk is not an event. One chunk may carry half a line, and one
// event may arrive over several chunks, so the decoder buffers until a blank
// line closes the event. That is the only state it keeps.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/laincat/veneracat-core/host/sse"
)

// StreamDecoder turns a protocol's server-sent events into text deltas.
//
// Create one per request; it is not safe for concurrent use, which matches how
// the chunk callback is called.
type StreamDecoder struct {
	protocol Protocol
	frame    sse.Framer
	// done records that the provider said the answer is complete.
	done bool
}

// NewStreamDecoder returns a decoder for one protocol.
func NewStreamDecoder(protocol Protocol) (*StreamDecoder, error) {
	if _, err := pathFor(protocol); err != nil {
		return nil, err
	}
	return &StreamDecoder{protocol: protocol}, nil
}

// Done reports whether the provider ended the answer. A transport EOF alone
// does not complete the protocol: callers must reject an unfinished reply.
func (d *StreamDecoder) Done() bool { return d.done }

// Push takes the next piece of the body and returns the text deltas it
// completed, in order. A piece may complete none, one, or several events.
func (d *StreamDecoder) Push(chunk []byte) ([]string, error) {
	out := make([]string, 0, 2)
	err := d.frame.Push(chunk, func(event sse.Event) error {
		text, err := d.dispatch(event.Data)
		if text != "" {
			out = append(out, text)
		}
		return err
	})
	if errors.Is(err, sse.ErrEventTooLarge) {
		err = errors.New("aiapi: the stream sent an event larger than 1 MiB")
	}
	return out, err
}

func (d *StreamDecoder) dispatch(payload string) (string, error) {
	if payload == "[DONE]" {
		d.done = true
		return "", nil
	}
	if strings.TrimSpace(payload) == "" {
		return "", nil
	}
	var event struct {
		Type  string          `json:"type"`
		Delta json.RawMessage `json:"delta"`
		// Chat Completions wraps its delta in choices.
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		// Some providers report a mid-stream failure as an event body.
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return "", errors.New("aiapi: the stream sent an event that is not JSON")
	}
	if event.Error != nil {
		message := event.Error.Message
		if message == "" {
			message = event.Error.Type
		}
		if message == "" {
			message = "the provider reported an error"
		}
		return "", fmt.Errorf("aiapi: %s", message)
	}
	switch d.protocol {
	case Chat:
		if len(event.Choices) == 0 {
			return "", nil
		}
		return event.Choices[0].Delta.Content, nil
	case Responses:
		if event.Type != "response.output_text.delta" {
			if event.Type == "response.completed" {
				d.done = true
			}
			return "", nil
		}
		var text string
		if json.Unmarshal(event.Delta, &text) != nil {
			return "", nil
		}
		return text, nil
	case Messages:
		switch event.Type {
		case "message_stop":
			d.done = true
			return "", nil
		case "content_block_delta":
			var delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Delta, &delta) != nil || delta.Type != "text_delta" {
				return "", nil
			}
			return delta.Text, nil
		}
	}
	return "", nil
}

// RequireStream says whether the body asks for a streamed answer. The streamed
// path needs "stream": true, because a provider that was not asked for events
// answers with one JSON object and the decoder would return no text at all —
// an empty reply that looks like a silent provider instead of a caller mistake.
func RequireStream(body json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(body), &fields); err != nil {
		return errors.New("aiapi: the request body is not valid JSON")
	}
	if string(bytes.TrimSpace(fields["stream"])) != "true" {
		return errors.New(`aiapi: a streamed request needs "stream": true in its body`)
	}
	return nil
}
