package mobile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Use the actual bundled provider template and the public mobile entry points.
// A direct mobileHost cancellation check does not exercise ChatCancel's wiring.
func chatCancelPackage(t *testing.T, m *Magpie) {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"package.json", "index.mjs"} {
		data, err := os.ReadFile(filepath.Join("..", "magpieplugin", "testdata", "openai-compatible", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = base64.StdEncoding.EncodeToString(data)
	}
	raw, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAuth("openai-compatible", `{"type":"api","key":"fake-key","metadata":{"baseURL":"https://vendor.test/v1"}}`); err != nil {
		t.Fatal(err)
	}
}

func startCancelChat(t *testing.T, m *Magpie) int64 {
	t.Helper()
	raw, err := m.ChatStart("fixture", `[{"role":"user","content":"hello"}]`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var handle struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &handle); err != nil {
		t.Fatal(err)
	}
	if handle.ID <= 0 {
		t.Fatal("chat has no handle")
	}
	return handle.ID
}

func TestMagpieChatCancelReachesBlockedHTTP(t *testing.T) {
	h := &magpieBlockingHost{started: make(chan int64, 1), cancelled: make(chan int64, 1), release: make(chan struct{})}
	m := NewMagpie(h)
	defer m.Close()
	defer close(h.release)
	chatCancelPackage(t, m)
	handle := startCancelChat(t, m)
	var requestID int64
	select {
	case requestID = <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("chat HTTP did not start")
	}
	m.ChatCancel(handle)
	select {
	case got := <-h.cancelled:
		if got != requestID || got <= 0 {
			t.Fatalf("cancelled request %d, expected %d", got, requestID)
		}
	case <-time.After(time.Second):
		t.Fatal("ChatCancel removed its handle but did not cancel the blocked HTTP request")
	}
}

func TestMagpieChatCancelReachesBlockedStreamOpen(t *testing.T) {
	h := &blockingOpenStreamHost{started: make(chan struct{}), released: make(chan struct{})}
	m := NewMagpieWithStream(h, h)
	defer m.Close()
	defer h.once.Do(func() { close(h.released) })
	chatCancelPackage(t, m)
	handle := startCancelChat(t, m)
	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("chat stream did not start")
	}
	m.ChatCancel(handle)
	select {
	case <-h.released:
	case <-time.After(time.Second):
		t.Fatal("ChatCancel did not close the chat's blocked StreamOpen request")
	}
}
