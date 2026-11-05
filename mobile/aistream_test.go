package mobile

import (
	"encoding/base64"
	"strings"
	"testing"
)

// A piece of an answer can arrive split anywhere: the decoder is fed pieces, not
// events, and must return each delta exactly once.
func TestAStreamDecoderTakesSplitPieces(t *testing.T) {
	handle, err := AIStreamOpen("chat")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer AIStreamClose(handle)

	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var collected strings.Builder
	// Three bytes at a time, so most events are split across pieces.
	for i := 0; i < len(body); i += 3 {
		end := i + 3
		if end > len(body) {
			end = len(body)
		}
		out, err := AIStreamPush(handle, base64.StdEncoding.EncodeToString([]byte(body[i:end])))
		if err != nil {
			t.Fatalf("push: %v", err)
		}
		for _, delta := range jsonStrings(t, out) {
			collected.WriteString(delta)
		}
	}
	if collected.String() != "Hello" {
		t.Fatalf("deltas = %q", collected.String())
	}
	done, err := AIStreamDone(handle)
	if err != nil || done != "true" {
		t.Fatalf("done = %q, %v", done, err)
	}
}

// An unknown handle is a caller mistake, not something to ignore.
func TestAPushWithoutAHandleIsRefused(t *testing.T) {
	if _, err := AIStreamPush("999", base64.StdEncoding.EncodeToString([]byte("data: {}\n\n"))); err == nil {
		t.Fatal("a push with no decoder was accepted")
	}
	if _, err := AIStreamOpen("no-such-protocol"); err == nil {
		t.Fatal("an unknown protocol opened a decoder")
	}
	if _, err := AIStreamPush("not-a-handle", "AAAA"); err == nil {
		t.Fatal("a non-numeric handle was accepted")
	}
}

func jsonStrings(t *testing.T, raw string) []string {
	t.Helper()
	var out []string
	if err := unmarshalStrings(raw, &out); err != nil {
		t.Fatalf("deltas %q: %v", raw, err)
	}
	return out
}
