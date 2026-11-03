package aiapi

import (
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

func TestNewStreamDecoderRejectsUnknownProtocol(t *testing.T) {
	if _, err := NewStreamDecoder("grpc"); err == nil {
		t.Fatal("unknown protocol accepted")
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
