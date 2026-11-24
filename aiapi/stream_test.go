package aiapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func chatDelta(text string) string {
	return `data: {"choices":[{"delta":{"content":"` + text + `"}}]}` + "\n\n"
}

func pushAll(t *testing.T, d *StreamDecoder, chunks ...string) []string {
	t.Helper()
	var got []string
	for _, chunk := range chunks {
		texts, err := d.Push([]byte(chunk))
		if err != nil {
			t.Fatalf("Push(%q): %v", chunk, err)
		}
		got = append(got, texts...)
	}
	return got
}

func TestStreamDecoderReadsChatDeltas(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	got := pushAll(t, d, chatDelta("你"), chatDelta("好"))
	if strings.Join(got, "|") != "你|好" {
		t.Fatalf("deltas = %q", got)
	}
	if d.Done() {
		t.Fatal("Done before the provider said so")
	}
	pushAll(t, d, "data: [DONE]\n\n")
	if !d.Done() {
		t.Fatal("[DONE] did not end the stream")
	}
}

// A chunk is not an event: one event may arrive over several chunks and one
// chunk may carry several events. The decoder must not depend on either.
func TestStreamDecoderBuffersAcrossChunks(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	full := chatDelta("half") + chatDelta("and") + chatDelta("half")
	// Split at awkward points: inside "data:", inside the JSON, between events.
	cut1, cut2 := 3, len(full)/2
	got := pushAll(t, d, full[:cut1], full[cut1:cut2], full[cut2:])
	if strings.Join(got, "") != "halfandhalf" {
		t.Fatalf("text = %q from %d deltas", strings.Join(got, ""), len(got))
	}
}

func TestStreamDecoderReadsResponsesDeltas(t *testing.T) {
	d, err := NewStreamDecoder(Responses)
	if err != nil {
		t.Fatal(err)
	}
	got := pushAll(t, d,
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"A\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"B\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	if strings.Join(got, "") != "AB" {
		t.Fatalf("text = %q", got)
	}
	if !d.Done() {
		t.Fatal("response.completed did not end the stream")
	}
}

func TestStreamDecoderReadsAnthropicTextDeltas(t *testing.T) {
	d, err := NewStreamDecoder(Messages)
	if err != nil {
		t.Fatal(err)
	}
	got := pushAll(t, d,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"甲\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"乙\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	if strings.Join(got, "") != "甲乙" {
		// A tool-call delta must not be mistaken for text.
		t.Fatalf("text = %q", got)
	}
	if !d.Done() {
		t.Fatal("message_stop did not end the stream")
	}
}

func TestStreamDecoderReportsProviderError(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Push([]byte(`data: {"error":{"message":"rate limited","type":"rate_limit"}}` + "\n\n")); err == nil {
		t.Fatal("a provider error event was swallowed")
	} else if !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v, want the provider's message", err)
	}
}

func TestStreamDecoderRejectsBrokenEvent(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Push([]byte("data: {not json}\n\n")); err == nil {
		t.Fatal("malformed event was accepted")
	}
}

func TestStreamDecoderBoundsMultilineEvent(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	line := "data: " + strings.Repeat("x", 600000) + "\n"
	if _, err := d.Push([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Push([]byte(line)); err == nil {
		t.Fatal("oversized multiline event was accepted")
	}
}

func TestNewStreamDecoderRejectsUnknownProtocol(t *testing.T) {
	if _, err := NewStreamDecoder("grpc"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}

func TestStreamDecoderSharesBOMAndCRFraming(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	body := "\ufeff: keepalive\r\n\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\r\rdata: [DONE]\r\r"
	for _, b := range []byte(body) {
		parts, err := d.Push([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			text.WriteString(part)
		}
	}
	if text.String() != "Hello" || !d.Done() {
		t.Fatalf("text = %q, done = %v", text.String(), d.Done())
	}
}

func TestRequireStreamNeedsTheBoolean(t *testing.T) {
	if err := RequireStream([]byte(`{"model":"m","stream":true}`)); err != nil {
		t.Fatalf("stream:true rejected: %v", err)
	}
	for _, body := range []string{`{"model":"m"}`, `{"model":"m","stream":false}`, `{"model":"m","stream":"true"}`} {
		if err := RequireStream([]byte(body)); err == nil {
			t.Fatalf("%s was accepted; a whole answer would look like an empty stream", body)
		}
	}
}

func TestStreamDecoderPushEventsChatStructured(t *testing.T) {
	d, err := NewStreamDecoder(Chat)
	if err != nil {
		t.Fatal(err)
	}
	wire := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"think "}}]}`,
		"",
		`data: {"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		"",
		`data: {"choices":[],"usage":{"prompt_tokens":9007199254740993,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":3}}}`,
		"",
		`data: [DONE]`,
		"", "",
	}, "\n")
	events, err := d.PushEvents([]byte(wire))
	if err != nil || len(events) != 4 {
		t.Fatalf("events = %#v, err = %v", events, err)
	}
	if events[0].Kind != StreamEventReasoning || events[0].Text != "think " {
		t.Fatalf("event[0] = %#v", events[0])
	}
	if events[1].Kind != StreamEventText || events[1].Text != "hello" {
		t.Fatalf("event[1] = %#v", events[1])
	}
	if events[2].Kind != StreamEventUsage || events[2].Usage == nil {
		t.Fatalf("event[2] = %#v", events[2])
	}
	u := events[2].Usage
	if u.InputTokens == nil || u.InputTokens.String() != "9007199254740993" {
		t.Fatalf("inputTokens = %v", u.InputTokens)
	}
	if u.OutputTokens == nil || u.OutputTokens.String() != "5" {
		t.Fatalf("outputTokens = %v", u.OutputTokens)
	}
	// TotalTokens was not reported by the provider and MUST stay nil (never 0).
	if u.TotalTokens != nil {
		t.Fatalf("unreported totalTokens should be nil, got %v", *u.TotalTokens)
	}
	if u.CacheReadTokens == nil || u.CacheReadTokens.String() != "2" {
		t.Fatalf("cacheReadTokens = %v", u.CacheReadTokens)
	}
	if u.ReasoningTokens == nil || u.ReasoningTokens.String() != "3" {
		t.Fatalf("reasoningTokens = %v", u.ReasoningTokens)
	}
	if events[3].Kind != StreamEventDone || events[3].FinishReason != "stop" || events[3].Outcome != FinishStop {
		t.Fatalf("done event = %#v", events[3])
	}
	if !d.Done() || d.FinishReason() != "stop" || d.Outcome() != FinishStop {
		t.Fatalf("decoder state done=%v finish=%q outcome=%q", d.Done(), d.FinishReason(), d.Outcome())
	}
}

func TestStreamDecoderPushEventsMessagesMergesNullableUsage(t *testing.T) {
	d, err := NewStreamDecoder(Messages)
	if err != nil {
		t.Fatal(err)
	}
	wire := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m1","usage":{"input_tokens":120,"cache_read_input_tokens":40}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"step 1"}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"answer"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":33}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"", "",
	}, "\n")
	events, err := d.PushEvents([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != StreamEventDone || last.FinishReason != "max_tokens" || last.Outcome != FinishLength || !last.Outcome.Truncated() {
		t.Fatalf("done = %#v", last)
	}
	if last.Usage == nil || last.Usage.InputTokens == nil || last.Usage.InputTokens.String() != "120" ||
		last.Usage.OutputTokens == nil || last.Usage.OutputTokens.String() != "33" ||
		last.Usage.CacheReadTokens == nil || last.Usage.CacheReadTokens.String() != "40" {
		t.Fatalf("merged usage = %#v", last.Usage)
	}
	if last.Usage.TotalTokens != nil || last.Usage.CacheWriteTokens != nil {
		t.Fatalf("unknown usage counters must stay nil: %#v", last.Usage)
	}
	rawUsage, err := json.Marshal(last.Usage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawUsage), `"totalTokens":null`) {
		t.Fatalf("expected explicit null for unknown totalTokens, got %s", rawUsage)
	}
}

func TestStreamDecoderPushEventsResponsesReasoningAndError(t *testing.T) {
	d, err := NewStreamDecoder(Responses)
	if err != nil {
		t.Fatal(err)
	}
	wire := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"plan"}`,
		"",
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"result"}`,
		"",
		`event: response.incomplete`,
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":10,"output_tokens":4}}}`,
		"", "",
	}, "\n")
	events, err := d.PushEvents([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Kind != StreamEventReasoning || events[0].Text != "plan" {
		t.Fatalf("event[0] = %#v", events[0])
	}
	if events[2].Kind != StreamEventDone || events[2].FinishReason != "incomplete" || events[2].FinishDetail != "content_filter" || events[2].Outcome != FinishContentFilter {
		t.Fatalf("done = %#v", events[2])
	}

	dErr, err := NewStreamDecoder(Responses)
	if err != nil {
		t.Fatal(err)
	}
	errEvents, err := dErr.PushEvents([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"too fast\"}}}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(errEvents) != 1 || errEvents[0].Kind != StreamEventError || errEvents[0].Error == nil || errEvents[0].Error.Code != ErrCodeRateLimit || !errEvents[0].Error.Retryable {
		t.Fatalf("errEvents = %#v", errEvents)
	}
	// Legacy Push surfaces the structured *ProviderError.
	dLegacy, _ := NewStreamDecoder(Responses)
	_, legacyErr := dLegacy.Push([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"too fast\"}}}\n\n"))
	var pe *ProviderError
	if !errors.As(legacyErr, &pe) || pe.Code != ErrCodeRateLimit {
		t.Fatalf("legacy Push error = %v, want *ProviderError(rate_limit)", legacyErr)
	}
}

func TestParseResponseAllProtocols(t *testing.T) {
	chatSum, err := ParseResponse(Chat, []byte(`{"choices":[{"message":{"content":"ok","reasoning_content":"why"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if chatSum.Text != "ok" || chatSum.Reasoning != "why" || chatSum.FinishReason != "stop" || chatSum.Outcome != FinishStop || chatSum.Usage != nil {
		t.Fatalf("chat summary = %#v", chatSum)
	}

	respSum, err := ParseResponse(Responses, []byte(`{"status":"completed","output_text":"done","usage":{"input_tokens":7,"output_tokens":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if respSum.Text != "done" || respSum.Outcome != FinishStop || respSum.Usage == nil || respSum.Usage.InputTokens.String() != "7" || respSum.Usage.TotalTokens != nil {
		t.Fatalf("responses summary = %#v", respSum)
	}

	msgSum, err := ParseResponse(Messages, []byte(`{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"yes"}],"usage":{"input_tokens":5,"output_tokens":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if msgSum.Text != "yes" || msgSum.Reasoning != "hmm" || msgSum.FinishReason != "end_turn" || msgSum.Outcome != FinishStop {
		t.Fatalf("messages summary = %#v", msgSum)
	}
}
