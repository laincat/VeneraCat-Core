package aiapi

// Reading a streamed or whole answer from a standard AI protocol.
//
// The three protocols all use server-sent events, but they disagree on event
// structure: Chat Completions puts deltas in choices[0].delta, Responses emits
// typed response.* events, and Messages emits message_start, content_block_delta,
// message_delta and message_stop.
//
// Push keeps text-only compatibility for existing callers; PushEvents decodes
// full structured events (text, reasoning, nullable exact token usage, raw and
// normalized finish reasons, and structured provider errors). Unknown token
// counters stay nil and are never fabricated as zero.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/laincat/veneracat-core/host/sse"
)

// StreamEventKind identifies a structured event emitted by StreamDecoder.
type StreamEventKind string

const (
	// StreamEventText carries an assistant text delta.
	StreamEventText StreamEventKind = "text"
	// StreamEventReasoning carries a reasoning or thinking delta.
	StreamEventReasoning StreamEventKind = "reasoning"
	// StreamEventUsage carries token accounting reported by the provider.
	StreamEventUsage StreamEventKind = "usage"
	// StreamEventDone marks protocol-level completion with finish reason and usage.
	StreamEventDone StreamEventKind = "done"
	// StreamEventError carries a structured provider error received mid-stream.
	StreamEventError StreamEventKind = "error"
)

// FinishReason is a protocol-independent completion outcome.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishCancelled     FinishReason = "cancelled"
	FinishError         FinishReason = "error"
	FinishInProgress    FinishReason = "in_progress"
)

// NormalizeFinish maps a protocol's raw finish_reason / stop_reason / status
// string into Core's FinishReason vocabulary. Unknown values are kept verbatim
// rather than collapsed into FinishStop.
func NormalizeFinish(protocol Protocol, raw string) FinishReason {
	if raw == "" {
		return ""
	}
	switch protocol {
	case Chat:
		switch raw {
		case "stop":
			return FinishStop
		case "length":
			return FinishLength
		case "tool_calls", "function_call":
			return FinishToolCalls
		case "content_filter":
			return FinishContentFilter
		}
	case Responses:
		switch raw {
		case "completed":
			return FinishStop
		case "incomplete":
			return FinishLength
		case "failed":
			return FinishError
		case "cancelled", "canceled":
			return FinishCancelled
		case "in_progress", "queued":
			return FinishInProgress
		}
	case Messages:
		switch raw {
		case "end_turn", "stop_sequence":
			return FinishStop
		case "max_tokens", "pause_turn":
			return FinishLength
		case "tool_use":
			return FinishToolCalls
		case "refusal":
			return FinishContentFilter
		}
	}
	return FinishReason(raw)
}

// Truncated reports whether the reply was cut short by a token limit.
func (r FinishReason) Truncated() bool { return r == FinishLength }

// TokenUsage stores exact, nullable token counters reported by a provider.
// Any field not reported by the provider remains nil (encoded as JSON null),
// never fabricated as 0.
type TokenUsage struct {
	InputTokens      *json.Number    `json:"inputTokens"`
	OutputTokens     *json.Number    `json:"outputTokens"`
	TotalTokens      *json.Number    `json:"totalTokens"`
	CacheReadTokens  *json.Number    `json:"cacheReadTokens"`
	CacheWriteTokens *json.Number    `json:"cacheWriteTokens"`
	ReasoningTokens  *json.Number    `json:"reasoningTokens"`
	Raw              json.RawMessage `json:"raw,omitempty"`
}

func cloneNumberPtr(n *json.Number) *json.Number {
	if n == nil {
		return nil
	}
	v := *n
	return &v
}

// Clone returns a deep copy of u, or nil when u is nil.
func (u *TokenUsage) Clone() *TokenUsage {
	if u == nil {
		return nil
	}
	out := &TokenUsage{
		InputTokens:      cloneNumberPtr(u.InputTokens),
		OutputTokens:     cloneNumberPtr(u.OutputTokens),
		TotalTokens:      cloneNumberPtr(u.TotalTokens),
		CacheReadTokens:  cloneNumberPtr(u.CacheReadTokens),
		CacheWriteTokens: cloneNumberPtr(u.CacheWriteTokens),
		ReasoningTokens:  cloneNumberPtr(u.ReasoningTokens),
	}
	if len(u.Raw) > 0 {
		out.Raw = append(json.RawMessage(nil), u.Raw...)
	}
	return out
}

// StreamEvent is one structured event decoded from a standard AI SSE stream.
type StreamEvent struct {
	Kind StreamEventKind `json:"kind"`
	// Text carries assistant text, reasoning text, or a sanitized error message.
	Text string `json:"text,omitempty"`
	// FinishReason is the provider's raw ending word (finish_reason / stop_reason / status).
	FinishReason string `json:"finishReason,omitempty"`
	// FinishDetail is the Responses API's incomplete_details.reason, if present.
	FinishDetail string `json:"finishDetail,omitempty"`
	// Outcome is the normalized completion reason across protocols.
	Outcome FinishReason `json:"outcome,omitempty"`
	// Usage is non-nil only when the provider reported token accounting.
	Usage *TokenUsage `json:"usage,omitempty"`
	// Error is non-nil on StreamEventError.
	Error *ProviderError `json:"error,omitempty"`
}

// ResponseSummary is the structured summary parsed from a non-streaming
// standard AI JSON response.
type ResponseSummary struct {
	Text         string       `json:"text,omitempty"`
	Reasoning    string       `json:"reasoning,omitempty"`
	FinishReason string       `json:"finishReason,omitempty"`
	FinishDetail string       `json:"finishDetail,omitempty"`
	Outcome      FinishReason `json:"outcome,omitempty"`
	Usage        *TokenUsage  `json:"usage"`
}

// StreamDecoder turns a protocol's server-sent events into text deltas or
// structured StreamEvents.
//
// Create one per request; it is not safe for concurrent use, which matches how
// the chunk callback is called.
type StreamDecoder struct {
	protocol     Protocol
	frame        sse.Framer
	done         bool
	finishReason string
	finishDetail string
	outcome      FinishReason
	usage        *TokenUsage
	lastErr      *ProviderError
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

// FinishReason returns the provider's raw finish_reason / stop_reason / status,
// or "" if the provider has not reported one.
func (d *StreamDecoder) FinishReason() string { return d.finishReason }

// FinishDetail returns the Responses API's raw reason for an incomplete reply.
func (d *StreamDecoder) FinishDetail() string { return d.finishDetail }

// Outcome returns the normalized completion outcome, or "" if none was reported.
func (d *StreamDecoder) Outcome() FinishReason { return d.outcome }

// Usage returns a copy of the reported token usage, or nil if the provider
// never reported usage.
func (d *StreamDecoder) Usage() *TokenUsage { return d.usage.Clone() }

// Error returns the last structured provider error decoded from the stream, if any.
func (d *StreamDecoder) Error() *ProviderError { return d.lastErr.clone() }

// Push takes the next piece of the body and returns the text deltas it
// completed, in order. Provider error events are returned as *ProviderError.
// This preserves backward compatibility for text-only callers.
func (d *StreamDecoder) Push(chunk []byte) ([]string, error) {
	events, err := d.PushEvents(chunk)
	out := make([]string, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case StreamEventText:
			if ev.Text != "" {
				out = append(out, ev.Text)
			}
		case StreamEventError:
			return out, ev.Error
		}
	}
	return out, err
}

var errTerminalEvent = errors.New("aiapi: the stream reached a terminal event")

// PushEvents feeds the next piece of the SSE body and returns all structured
// StreamEvents completed by this piece, in arrival order.
//
// Framing errors (invalid JSON or an event larger than 1 MiB) return an error.
// Mid-stream provider error payloads are decoded as StreamEventError entries.
func (d *StreamDecoder) PushEvents(chunk []byte) ([]StreamEvent, error) {
	out := make([]StreamEvent, 0, 2)
	if d.done || d.lastErr != nil {
		return out, nil
	}
	err := d.frame.Push(chunk, func(event sse.Event) error {
		events, err := d.DecodeEvent(event)
		out = append(out, events...)
		if d.done || d.lastErr != nil {
			return errTerminalEvent
		}
		return err
	})
	if errors.Is(err, errTerminalEvent) {
		err = nil
	}
	if errors.Is(err, sse.ErrEventTooLarge) {
		err = d.invalidEvent("stream event exceeds the size limit")
	}
	return out, err
}

// DecodeEvent decodes an already framed SSE event. Use this with host/sse.Decoder
// when consuming an io.Reader, or use PushEvents with raw network chunks.
func (d *StreamDecoder) DecodeEvent(event sse.Event) ([]StreamEvent, error) {
	if d.done || d.lastErr != nil {
		return nil, nil
	}
	eventType := event.Type
	trimmed := strings.TrimSpace(event.Data)
	if trimmed == "" {
		return nil, nil
	}
	if trimmed == "[DONE]" {
		d.done = true
		return []StreamEvent{{
			Kind:         StreamEventDone,
			FinishReason: d.finishReason,
			Outcome:      d.outcome,
			Usage:        d.usage.Clone(),
		}}, nil
	}

	obj := parseObject([]byte(trimmed))
	if obj == nil {
		return nil, d.invalidEvent("stream event is not a JSON object")
	}
	if rawErr := obj["error"]; len(rawErr) > 0 && !isJSONNull(rawErr) {
		return d.providerFailure(ClassifyStreamError(d.protocol, []byte(trimmed))), nil
	}
	if eventType == "error" || jsonStringField(obj["type"]) == "error" {
		return d.providerFailure(ClassifyStreamError(d.protocol, []byte(trimmed))), nil
	}

	switch d.protocol {
	case Chat:
		return d.dispatchChat(obj)
	case Responses:
		return d.dispatchResponses(eventType, obj)
	case Messages:
		return d.dispatchMessages(eventType, obj)
	}
	return nil, nil
}

func (d *StreamDecoder) invalidEvent(message string) error {
	return &ProviderError{Code: ErrCodeInvalidFormat, Protocol: d.protocol, Message: fmt.Sprintf("%s: %s", d.protocol, message)}
}

func (d *StreamDecoder) providerFailure(err *ProviderError) []StreamEvent {
	d.lastErr = err
	return []StreamEvent{{Kind: StreamEventError, Text: err.Message, Error: err.clone(),
		FinishReason: d.finishReason, Outcome: d.outcome, Usage: d.usage.Clone()}}
}

func (d *StreamDecoder) dispatchChat(obj map[string]json.RawMessage) ([]StreamEvent, error) {
	var events []StreamEvent
	if rawChoices, ok := obj["choices"]; ok && !isJSONNull(rawChoices) {
		var choices []map[string]json.RawMessage
		if err := json.Unmarshal(rawChoices, &choices); err != nil {
			return nil, d.invalidEvent("invalid stream choices")
		}
		if len(choices) > 0 {
			choice := choices[0]
			if rawDelta, ok := choice["delta"]; ok && !isJSONNull(rawDelta) {
				var delta map[string]json.RawMessage
				if err := json.Unmarshal(rawDelta, &delta); err == nil {
					if reasoning := jsonStringField(delta["reasoning_content"]); reasoning != "" {
						events = append(events, StreamEvent{Kind: StreamEventReasoning, Text: reasoning})
					} else if reasoning := jsonStringField(delta["reasoning"]); reasoning != "" {
						events = append(events, StreamEvent{Kind: StreamEventReasoning, Text: reasoning})
					}
					if text := jsonStringField(delta["content"]); text != "" {
						events = append(events, StreamEvent{Kind: StreamEventText, Text: text})
					}
				}
			}
			if text := jsonStringField(choice["text"]); text != "" && len(events) == 0 {
				events = append(events, StreamEvent{Kind: StreamEventText, Text: text})
			}
			if finish := jsonStringField(choice["finish_reason"]); finish != "" {
				d.finishReason = finish
				d.outcome = NormalizeFinish(Chat, finish)
			}
		}
	}
	if usage := parseTokenUsage(obj["usage"]); usage != nil {
		d.mergeUsage(usage)
		events = append(events, StreamEvent{Kind: StreamEventUsage, Usage: d.usage.Clone()})
	}
	return events, nil
}

func (d *StreamDecoder) dispatchResponses(eventType string, obj map[string]json.RawMessage) ([]StreamEvent, error) {
	kind := jsonStringField(obj["type"])
	if kind == "" {
		kind = strings.TrimSpace(eventType)
	}
	switch kind {
	case "response.output_text.delta":
		if text := jsonStringField(obj["delta"]); text != "" {
			return []StreamEvent{{Kind: StreamEventText, Text: text}}, nil
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if text := jsonStringField(obj["delta"]); text != "" {
			return []StreamEvent{{Kind: StreamEventReasoning, Text: text}}, nil
		}
	case "response.completed", "response.incomplete":
		finish := ""
		var incompleteReason string
		if rawResp, ok := obj["response"]; ok && !isJSONNull(rawResp) {
			var resp map[string]json.RawMessage
			if json.Unmarshal(rawResp, &resp) == nil {
				finish = jsonStringField(resp["status"])
				if rawInc, ok := resp["incomplete_details"]; ok && !isJSONNull(rawInc) {
					var inc map[string]json.RawMessage
					if json.Unmarshal(rawInc, &inc) == nil {
						incompleteReason = jsonStringField(inc["reason"])
					}
				}
				if usage := parseTokenUsage(resp["usage"]); usage != nil {
					d.mergeUsage(usage)
				}
			}
		}
		if finish == "" {
			if kind == "response.incomplete" {
				finish = "incomplete"
			} else {
				finish = "completed"
			}
		}
		outcome := NormalizeFinish(Responses, finish)
		if incompleteReason == "content_filter" {
			outcome = FinishContentFilter
		}
		d.finishReason, d.finishDetail, d.outcome = finish, incompleteReason, outcome
		d.done = true
		return []StreamEvent{{Kind: StreamEventDone, FinishReason: finish, FinishDetail: incompleteReason,
			Outcome: outcome, Usage: d.usage.Clone()}}, nil
	case "response.failed", "response.cancelled", "response.canceled":
		resp := parseObject(obj["response"])
		d.finishReason = jsonStringField(resp["status"])
		if d.finishReason == "" {
			d.finishReason = strings.TrimPrefix(kind, "response.")
		}
		d.outcome = NormalizeFinish(Responses, d.finishReason)
		d.mergeUsage(parseTokenUsage(resp["usage"]))
		failure := ClassifyStreamError(Responses, resp["error"])
		if d.outcome == FinishCancelled {
			failure = safelyClassified(Responses, 0, ErrCodeCancelled)
		} else if failure.Code == ErrCodeUnknown {
			failure = safelyClassified(Responses, 0, ErrCodeUpstream)
		}
		return d.providerFailure(failure), nil
	}
	return nil, nil
}

func (d *StreamDecoder) dispatchMessages(eventType string, obj map[string]json.RawMessage) ([]StreamEvent, error) {
	kind := jsonStringField(obj["type"])
	if kind == "" {
		kind = strings.TrimSpace(eventType)
	}
	switch kind {
	case "message_start":
		if rawMsg, ok := obj["message"]; ok && !isJSONNull(rawMsg) {
			var msg map[string]json.RawMessage
			if json.Unmarshal(rawMsg, &msg) == nil {
				if usage := parseTokenUsage(msg["usage"]); usage != nil {
					d.mergeUsage(usage)
					return []StreamEvent{{Kind: StreamEventUsage, Usage: d.usage.Clone()}}, nil
				}
			}
		}
	case "content_block_delta":
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(obj["delta"], &delta); err != nil {
			return nil, nil
		}
		switch jsonStringField(delta["type"]) {
		case "text_delta":
			if text := jsonStringField(delta["text"]); text != "" {
				return []StreamEvent{{Kind: StreamEventText, Text: text}}, nil
			}
		case "thinking_delta":
			if thinking := jsonStringField(delta["thinking"]); thinking != "" {
				return []StreamEvent{{Kind: StreamEventReasoning, Text: thinking}}, nil
			}
		}
	case "message_delta":
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(obj["delta"], &delta); err == nil {
			if stop := jsonStringField(delta["stop_reason"]); stop != "" {
				d.finishReason = stop
				d.outcome = NormalizeFinish(Messages, stop)
			}
		}
		if usage := parseTokenUsage(obj["usage"]); usage != nil {
			d.mergeUsage(usage)
			return []StreamEvent{{Kind: StreamEventUsage, Usage: d.usage.Clone()}}, nil
		}
	case "message_stop":
		d.done = true
		return []StreamEvent{{
			Kind:         StreamEventDone,
			FinishReason: d.finishReason,
			Outcome:      d.outcome,
			Usage:        d.usage.Clone(),
		}}, nil
	}
	return nil, nil
}

func (d *StreamDecoder) mergeUsage(next *TokenUsage) {
	if next == nil {
		return
	}
	if d.usage == nil {
		d.usage = next.Clone()
		return
	}
	if next.InputTokens != nil {
		d.usage.InputTokens = cloneNumberPtr(next.InputTokens)
	}
	if next.OutputTokens != nil {
		d.usage.OutputTokens = cloneNumberPtr(next.OutputTokens)
	}
	if next.TotalTokens != nil {
		d.usage.TotalTokens = cloneNumberPtr(next.TotalTokens)
	}
	if next.CacheReadTokens != nil {
		d.usage.CacheReadTokens = cloneNumberPtr(next.CacheReadTokens)
	}
	if next.CacheWriteTokens != nil {
		d.usage.CacheWriteTokens = cloneNumberPtr(next.CacheWriteTokens)
	}
	if next.ReasoningTokens != nil {
		d.usage.ReasoningTokens = cloneNumberPtr(next.ReasoningTokens)
	}
	// Raw retains the latest wire usage object. The normalized fields retain
	// counts reported in earlier events, such as Anthropic's input tokens.
	d.usage.Raw = append(d.usage.Raw[:0], next.Raw...)
}

// ParseResponse extracts text, reasoning, finish reason, outcome and nullable
// exact token usage from a non-streaming standard AI JSON response body.
func ParseResponse(protocol Protocol, body []byte) (*ResponseSummary, error) {
	if _, err := pathFor(protocol); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	obj := parseObject(trimmed)
	if obj == nil {
		return nil, safelyClassified(protocol, 0, ErrCodeInvalidFormat)
	}
	summary := &ResponseSummary{Usage: parseTokenUsage(obj["usage"])}
	if raw := obj["error"]; len(raw) > 0 && !isJSONNull(raw) {
		if protocol == Responses {
			summary.FinishReason = jsonStringField(obj["status"])
			summary.Outcome = NormalizeFinish(Responses, summary.FinishReason)
		}
		return summary, ClassifyStreamError(protocol, trimmed)
	}
	switch protocol {
	case Chat:
		if rawChoices, ok := obj["choices"]; ok && !isJSONNull(rawChoices) {
			var choices []map[string]json.RawMessage
			if json.Unmarshal(rawChoices, &choices) == nil && len(choices) > 0 {
				choice := choices[0]
				summary.FinishReason = jsonStringField(choice["finish_reason"])
				summary.Outcome = NormalizeFinish(Chat, summary.FinishReason)
				if rawMsg, ok := choice["message"]; ok && !isJSONNull(rawMsg) {
					var msg map[string]json.RawMessage
					if json.Unmarshal(rawMsg, &msg) == nil {
						summary.Text = extractMessageText(msg["content"])
						summary.Reasoning = jsonStringField(msg["reasoning_content"])
						if summary.Reasoning == "" {
							summary.Reasoning = jsonStringField(msg["reasoning"])
						}
					}
				}
			}
		}
	case Responses:
		summary.FinishReason = jsonStringField(obj["status"])
		summary.Outcome = NormalizeFinish(Responses, summary.FinishReason)
		switch summary.Outcome {
		case FinishError:
			return summary, safelyClassified(protocol, 0, ErrCodeUpstream)
		case FinishCancelled:
			return summary, safelyClassified(protocol, 0, ErrCodeCancelled)
		}
		summary.FinishDetail = jsonStringField(parseObject(obj["incomplete_details"])["reason"])
		if summary.FinishDetail == "content_filter" {
			summary.Outcome = FinishContentFilter
		}
		summary.Text = jsonStringField(obj["output_text"])
		if rawOut, ok := obj["output"]; ok && !isJSONNull(rawOut) {
			var items []map[string]json.RawMessage
			if json.Unmarshal(rawOut, &items) == nil {
				var textBuf, reasonBuf strings.Builder
				for _, item := range items {
					switch jsonStringField(item["type"]) {
					case "message":
						if rawParts, ok := item["content"]; ok {
							var parts []map[string]json.RawMessage
							if json.Unmarshal(rawParts, &parts) == nil {
								for _, part := range parts {
									if jsonStringField(part["type"]) == "output_text" {
										textBuf.WriteString(jsonStringField(part["text"]))
									}
								}
							}
						}
					case "reasoning":
						if rawSum, ok := item["summary"]; ok {
							var parts []map[string]json.RawMessage
							if json.Unmarshal(rawSum, &parts) == nil {
								for _, part := range parts {
									reasonBuf.WriteString(jsonStringField(part["text"]))
								}
							}
						}
					}
				}
				if summary.Text == "" {
					summary.Text = textBuf.String()
				}
				summary.Reasoning = reasonBuf.String()
			}
		}
	case Messages:
		summary.FinishReason = jsonStringField(obj["stop_reason"])
		summary.Outcome = NormalizeFinish(Messages, summary.FinishReason)
		if rawContent, ok := obj["content"]; ok && !isJSONNull(rawContent) {
			var blocks []map[string]json.RawMessage
			if json.Unmarshal(rawContent, &blocks) == nil {
				var textBuf, reasonBuf strings.Builder
				for _, block := range blocks {
					switch jsonStringField(block["type"]) {
					case "text":
						textBuf.WriteString(jsonStringField(block["text"]))
					case "thinking":
						reasonBuf.WriteString(jsonStringField(block["thinking"]))
					}
				}
				summary.Text = textBuf.String()
				summary.Reasoning = reasonBuf.String()
			}
		}
	}
	return summary, nil
}

func extractMessageText(raw json.RawMessage) string {
	if s := jsonStringField(raw); s != "" {
		return s
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if jsonStringField(p["type"]) == "text" {
				b.WriteString(jsonStringField(p["text"]))
			}
		}
		return b.String()
	}
	return ""
}

func parseTokenUsage(raw json.RawMessage) *TokenUsage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || isJSONNull(trimmed) {
		return nil
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil || len(fields) == 0 {
		return nil
	}
	usage := &TokenUsage{Raw: append(json.RawMessage(nil), trimmed...)}
	pick := func(names ...string) *json.Number {
		for _, name := range names {
			if v, ok := fields[name]; ok {
				if n := parseJSONNumber(v); n != nil {
					return n
				}
			}
		}
		return nil
	}
	usage.InputTokens = pick("prompt_tokens", "input_tokens")
	usage.OutputTokens = pick("completion_tokens", "output_tokens")
	usage.TotalTokens = pick("total_tokens")
	usage.CacheReadTokens = pick("cache_read_input_tokens")
	usage.CacheWriteTokens = pick("cache_creation_input_tokens")
	usage.ReasoningTokens = pick("reasoning_tokens")

	// OpenAI Chat & Responses nest cached / reasoning counts under *_details.
	if usage.CacheReadTokens == nil {
		if details := parseObject(fields["prompt_tokens_details"]); details != nil {
			usage.CacheReadTokens = parseJSONNumber(details["cached_tokens"])
		}
	}
	if usage.CacheReadTokens == nil {
		if details := parseObject(fields["input_tokens_details"]); details != nil {
			usage.CacheReadTokens = parseJSONNumber(details["cached_tokens"])
		}
	}
	if usage.ReasoningTokens == nil {
		if details := parseObject(fields["completion_tokens_details"]); details != nil {
			usage.ReasoningTokens = parseJSONNumber(details["reasoning_tokens"])
		}
	}
	if usage.ReasoningTokens == nil {
		if details := parseObject(fields["output_tokens_details"]); details != nil {
			usage.ReasoningTokens = parseJSONNumber(details["reasoning_tokens"])
		}
	}
	return usage
}

func parseObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}

func parseJSONNumber(raw json.RawMessage) *json.Number {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || isJSONNull(trimmed) {
		return nil
	}
	for _, b := range trimmed {
		if b < '0' || b > '9' {
			return nil
		}
	}
	n := json.Number(trimmed)
	return &n
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
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
