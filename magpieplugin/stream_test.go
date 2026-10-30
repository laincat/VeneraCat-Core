package magpieplugin

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func drain(t *testing.T, wire string, dialect Dialect) []Event {
	t.Helper()
	reader, err := NewStreamReader(strings.NewReader(wire), dialect, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for {
		event, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, event)
	}
}

func TestChatDialect(t *testing.T) {
	wire := strings.Join([]string{
		`data: {"id":"1","choices":[{"delta":{"content":"你"}}]}`,
		"",
		`data: {"id":"1","choices":[{"delta":{"content":"好"}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":2}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	events := drain(t, wire, DialectChat)
	var text strings.Builder
	for _, e := range events {
		if e.Kind == EventText {
			text.WriteString(e.Text)
		}
	}
	if text.String() != "你好" {
		t.Fatalf("text = %q", text.String())
	}
	last := events[len(events)-1]
	if last.Kind != EventDone || last.Finish != "stop" {
		t.Fatalf("last = %#v", last)
	}
	// The first done carries the vendor's usage; the [DONE] marker adds none.
	var usage *Usage
	for _, e := range events {
		if e.Usage != nil {
			usage = e.Usage
		}
	}
	if usage == nil || usage.Input != "11" || usage.Output != "2" {
		t.Fatalf("usage = %#v", usage)
	}
	if string(usage.Raw) != `{"prompt_tokens":11,"completion_tokens":2}` {
		t.Fatalf("usage raw = %s", usage.Raw)
	}
}

func TestMessagesDialect(t *testing.T) {
	wire := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m"}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"想"}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"答"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	events := drain(t, wire, DialectMessages)
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Kind != EventReasoning || events[0].Text != "想" {
		t.Fatalf("first = %#v", events[0])
	}
	if events[1].Kind != EventText || events[1].Text != "答" {
		t.Fatalf("second = %#v", events[1])
	}
	if events[2].Kind != EventDone || events[2].Finish != "end_turn" {
		t.Fatalf("last = %#v", events[2])
	}
}

func TestResponsesDialect(t *testing.T) {
	wire := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		"",
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}`,
		"",
		// The last block needs its own blank line, or SSE keeps the reply
		// waiting for one that never comes.
		"",
	}, "\n")
	events := drain(t, wire, DialectResponses)
	if len(events) != 2 || events[0].Kind != EventText || events[0].Text != "hi" {
		t.Fatalf("events = %#v", events)
	}
	if events[1].Kind != EventDone || events[1].Usage == nil || events[1].Usage.Output != "1" {
		t.Fatalf("done = %#v", events[1])
	}
}

func TestVendorErrorInsideASuccessfulStream(t *testing.T) {
	wire := strings.Join([]string{
		`data: {"error":{"message":"rate limited","type":"rate_limit"}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	events := drain(t, wire, DialectChat)
	if len(events) != 1 || events[0].Kind != EventError {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Text != "rate limited" {
		t.Fatalf("message = %q", events[0].Text)
	}
}

func TestUnreadableEventIsAnError(t *testing.T) {
	reader, err := NewStreamReader(strings.NewReader("data: not json\n\n"), DialectChat, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "chat") {
		t.Fatalf("error = %v", err)
	}
	if _, err := NewStreamReader(strings.NewReader(""), "grpc", 0); err == nil {
		t.Fatal("unknown dialect accepted")
	}
}

func TestRetryIsRefusedOnceOutputStarted(t *testing.T) {
	attempt := &Attempt{}
	if err := attempt.MayRetry(); err != nil {
		t.Fatalf("a silent attempt must be retryable: %v", err)
	}
	attempt.Saw(Event{Kind: EventText, Text: "你"})
	if err := attempt.MayRetry(); !errors.Is(err, ErrNoRetryAfterOutput) {
		t.Fatalf("retry after output = %v", err)
	}
	// An empty delta is not output; it must not lock the attempt.
	silent := &Attempt{}
	silent.Saw(Event{Kind: EventText, Text: ""})
	silent.Saw(Event{Kind: EventDone})
	if err := silent.MayRetry(); err != nil {
		t.Fatalf("silent attempt not retryable: %v", err)
	}
}

func TestLargeTokenCountsStayExact(t *testing.T) {
	wire := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9007199254740993}}\n\n"
	events := drain(t, wire, DialectChat)
	usage := events[0].Usage
	if usage == nil || usage.Input.String() != "9007199254740993" {
		t.Fatalf("usage = %#v", usage)
	}
	var decoded map[string]json.Number
	dec := json.NewDecoder(strings.NewReader(string(usage.Raw)))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["prompt_tokens"] != "9007199254740993" {
		t.Fatalf("raw number = %v", decoded["prompt_tokens"])
	}
}
