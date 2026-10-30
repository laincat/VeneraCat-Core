package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestChunkedEvents(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(ending, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
			wire := strings.Join([]string{
				"\ufeff: heartbeat", "id: session-1", "event: delta",
				`data: {"id":9007199254740993,`, `data: "text":"你好"}`, "",
				"data: [DONE]", "", "",
			}, ending)
			d, err := NewDecoder(iotest.OneByteReader(strings.NewReader(wire)), 0)
			if err != nil {
				t.Fatal(err)
			}
			first, err := d.Next()
			want := Event{Type: "delta", ID: "session-1", Data: "{\"id\":9007199254740993,\n\"text\":\"你好\"}"}
			if err != nil || first != want {
				t.Fatalf("first = %#v, %v; want %#v", first, err, want)
			}
			last, err := d.Next()
			if err != nil || last != (Event{Type: "message", Data: "[DONE]", ID: "session-1"}) {
				t.Fatalf("last = %#v, %v", last, err)
			}
			if _, err := d.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("end = %v", err)
			}
		})
	}
}

func TestDispatchRules(t *testing.T) {
	cases := []struct {
		name, wire string
		want       []Event
	}{
		{"empty data is an event", "data:\n\n", []Event{{Type: "message"}}},
		{"colonless data", "data\n\n", []Event{{Type: "message"}}},
		{"remove only one space", "data:  a:b\ndata:\n\n", []Event{{Type: "message", Data: " a:b\n"}}},
		{"no data no dispatch", ": keepalive\nevent: ignored\n\ndata: ok\n\n", []Event{{Type: "message", Data: "ok"}}},
		{"id survives silent block", "id: 123\n\ndata: a\n\nid:\ndata: b\n\n", []Event{{Type: "message", Data: "a", ID: "123"}, {Type: "message", Data: "b"}}},
		{"nul id ignored", "id: good\nid: bad\x00id\ndata: a\n\n", []Event{{Type: "message", Data: "a", ID: "good"}}},
		{"unknown fields ignored", "retry: 1000\nother: value\ndata: a\n\n", []Event{{Type: "message", Data: "a"}}},
		{"unterminated block discarded", "data: partial\n", nil},
		{"unterminated line discarded", "data: partial", nil},
		{"utf8 replacement", "data: \xff\n\n", []Event{{Type: "message", Data: "\ufffd"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDecoder(strings.NewReader(tc.wire), 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				got, err := d.Next()
				if err != nil || got != want {
					t.Fatalf("Next = %#v, %v; want %#v", got, err, want)
				}
			}
			if got, err := d.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("end = %#v, %v", got, err)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	for _, wire := range []string{
		"data: " + strings.Repeat("x", 100) + "\n\n",
		"data: a\ndata: b\ndata: c\n\n",
		": comment\n: comment\n: comment\n\n",
	} {
		d, err := NewDecoder(strings.NewReader(wire), 16)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Next(); !errors.Is(err, ErrEventTooLarge) {
			t.Fatalf("limit error = %v", err)
		}
		if _, err := d.Next(); !errors.Is(err, ErrEventTooLarge) {
			t.Fatalf("error must remain terminal: %v", err)
		}
	}
	// Limits reset between blocks, including blocks without data.
	d, _ := NewDecoder(strings.NewReader(": ping\n\n: ping\n\ndata: a\n\ndata: b\n\n"), 8)
	for _, want := range []string{"a", "b"} {
		got, err := d.Next()
		if err != nil || got.Data != want {
			t.Fatalf("boundary event = %#v, %v", got, err)
		}
	}
	for _, limit := range []int{-1, 64<<20 + 1} {
		if _, err := NewDecoder(strings.NewReader(""), limit); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	if _, err := NewDecoder(nil, 0); err == nil {
		t.Fatal("accepted nil reader")
	}
}

func TestReaderFailureIsNotCompletion(t *testing.T) {
	broken := errors.New("connection interrupted")
	r := io.MultiReader(strings.NewReader("data: partial\n"), errReader{broken})
	d, _ := NewDecoder(r, 0)
	if _, err := d.Next(); !errors.Is(err, broken) {
		t.Fatalf("lost transport error: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
