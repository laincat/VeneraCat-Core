package magpieplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

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
