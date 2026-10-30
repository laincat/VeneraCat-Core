package mobile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/magpieplugin"
)

type headWireHost struct {
	streamHostStub
	mode    string
	next    int
	release chan struct{}
}

func (h *headWireHost) StreamOpen(string) (string, error) {
	if h.mode == "open" {
		return `{"id":77,"status":201,"headers":{"X-Stream":["ready"]},"finalUrl":"https://fixture.test/final"}`, nil
	}
	return `{"id":77}`, nil
}
func (h *headWireHost) StreamNext(int64) (string, error) {
	h.next++
	if h.next == 1 && h.mode != "open" {
		switch h.mode {
		case "head":
			return `{"done":false,"status":201,"headers":{"X-Stream":["ready"]},"finalUrl":"https://fixture.test/final"}`, nil
		case "chunk":
			return `{"done":false,"status":201,"headers":{"X-Stream":["ready"]},"finalUrl":"https://fixture.test/final","chunkBase64":"Zmlyc3Q="}`, nil
		case "legacy":
			return `{"done":false,"chunkBase64":"Zmlyc3Q="}`, nil
		}
	}
	<-h.release
	if h.mode == "legacy" {
		return `{"done":true,"status":201,"headers":{"X-Stream":["ready"]},"finalUrl":"https://fixture.test/final"}`, nil
	}
	return `{"done":true}`, nil // The head already supplied the response metadata.
}
func TestStreamBridgeEarlyHeadWireAndLegacyTiming(t *testing.T) {
	for _, mode := range []string{"open", "head", "chunk", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			h := &headWireHost{mode: mode, release: make(chan struct{})}
			bridge := streamBridge{h: h, assign: nextHostRequestID}
			seen := make(chan string, 3)
			result := make(chan error, 1)
			go func() {
				answer, err := bridge.DoStreamWithHead(context.Background(), &magpieplugin.Request{Method: "GET", URL: "https://fixture.test"}, func(head *magpieplugin.Response) error {
					b, _ := json.Marshal([]any{head.Status, head.Header["X-Stream"][0], head.URL})
					seen <- string(b)
					return nil
				}, func(body []byte) error { seen <- string(body); return nil })
				if err == nil && (answer.Status != 201 || answer.URL != "https://fixture.test/final") {
					err = context.Canceled
				}
				result <- err
			}()
			defer close(h.release)
			expected := []string{`[201,"ready","https://fixture.test/final"]`}
			if mode == "legacy" {
				expected = []string{"first"}
			}
			if mode == "chunk" {
				expected = append(expected, "first")
			}
			for _, want := range expected {
				select {
				case got := <-seen:
					if got != want {
						t.Errorf("event=%q want=%q", got, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("head/body not delivered before release")
				}
			}
			if mode == "legacy" {
				select {
				case got := <-seen:
					t.Errorf("legacy head arrived before final metadata: %s", got)
				default:
				}
			}
			// Sending releases this one StreamNext without closing the channel twice.
			h.release <- struct{}{}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stream did not end")
			}
			if mode == "legacy" {
				select {
				case got := <-seen:
					if !strings.Contains(got, "201") {
						t.Fatal(got)
					}
				default:
					t.Fatal("legacy final head was lost")
				}
			}
		})
	}
}
