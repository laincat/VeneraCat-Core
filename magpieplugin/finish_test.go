package magpieplugin

import (
	"strings"
	"testing"
)

func TestNormalizeFinishSpeaksOneVocabulary(t *testing.T) {
	cases := []struct {
		dialect Dialect
		raw     string
		want    FinishReason
	}{
		// The same ending, three spellings.
		{DialectChat, "stop", FinishStop},
		{DialectResponses, "completed", FinishStop},
		{DialectMessages, "end_turn", FinishStop},
		{DialectMessages, "stop_sequence", FinishStop},

		// Cut short.
		{DialectChat, "length", FinishLength},
		{DialectResponses, "incomplete", FinishLength},
		{DialectMessages, "max_tokens", FinishLength},
		// Anthropic's resumable server-side loop is not an ending.
		{DialectMessages, "pause_turn", FinishLength},

		// Tools.
		{DialectChat, "tool_calls", FinishToolCalls},
		{DialectChat, "function_call", FinishToolCalls},
		{DialectMessages, "tool_use", FinishToolCalls},

		// Suppressed or failed.
		{DialectChat, "content_filter", FinishContentFilter},
		{DialectMessages, "refusal", FinishContentFilter},
		{DialectResponses, "failed", FinishError},
		{DialectResponses, "cancelled", FinishCancelled},
		{DialectResponses, "canceled", FinishCancelled},
		{DialectResponses, "in_progress", FinishInProgress},

		// Nothing was reported.
		{DialectChat, "", ""},
	}

	for _, tc := range cases {
		got := NormalizeFinish(tc.dialect, tc.raw)
		if got != tc.want {
			t.Errorf("NormalizeFinish(%s, %q) = %q, want %q", tc.dialect, tc.raw, got, tc.want)
		}
	}
}

// An ending nobody has seen must read as itself, not as "finished": calling an
// unknown outcome a clean stop is the one answer that is certainly wrong.
func TestNormalizeFinishKeepsUnknownWords(t *testing.T) {
	for _, dialect := range []Dialect{DialectChat, DialectResponses, DialectMessages} {
		got := NormalizeFinish(dialect, "brand_new_reason")
		if string(got) != "brand_new_reason" {
			t.Fatalf("%s: unknown reason became %q", dialect, got)
		}
		if got.Truncated() {
			t.Fatalf("%s: an unknown reason must not claim truncation", dialect)
		}
	}
}

// The token limit is the question a caller asks most often, so it is answered in
// every dialect by a reader that was told which one it is reading.
func TestReaderReportsTruncationInEveryDialect(t *testing.T) {
	cases := []struct {
		name    string
		dialect Dialect
		stream  string
	}{
		{"chat", DialectChat,
			`data: {"choices":[{"delta":{"content":"a"},"finish_reason":"length"}]}` + "\n\n"},
		{"messages", DialectMessages,
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n"},
		{"responses", DialectResponses,
			"event: response.completed\ndata: {\"response\":{\"status\":\"incomplete\"}}\n\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := NewStreamReader(strings.NewReader(tc.stream), tc.dialect, 0)
			if err != nil {
				t.Fatal(err)
			}
			var outcome FinishReason
			for {
				event, err := reader.Next()
				if err != nil {
					break
				}
				if event.Kind == EventDone {
					outcome = event.Outcome
					break
				}
			}
			if outcome != FinishLength {
				t.Fatalf("outcome = %q, want %q (raw endings differ by dialect)", outcome, FinishLength)
			}
			if !outcome.Truncated() {
				t.Fatal("a length ending must answer yes to Truncated")
			}
		})
	}
}
