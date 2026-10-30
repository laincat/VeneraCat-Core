package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/magpieplugin"
)

// sseLine wraps one JSON payload as an SSE data line.
func sseLine(payload string) string { return "data: " + payload + "\n\n" }

// streamHostStub is the client side of a chunked reply. A chat completion is
// delivered piece by piece; every other endpoint is answered whole through the
// parent stub, because a package's sign-in and model list are not streams and
// would fail if they were read as one.
type streamHostStub struct {
	magpieHostStub
	mu sync.Mutex
	// chatBody is what the next chat request returns, piece by piece.
	chatBody []string
	// chunks is what the stream currently being read still has to hand out.
	chunks  []string
	closed  int
	opened  int
	status  int
	headers map[string][]string
	final   string
	failOn  int
	chat    bool
}

func (h *streamHostStub) StreamOpen(requestJSON string) (string, error) {
	var req wireRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened++
	h.chat = strings.HasSuffix(req.URL, "/chat/completions")
	h.final = req.URL
	if h.chat {
		h.chunks = append([]string(nil), h.chatBody...)
		h.status = 200
		h.headers = map[string][]string{"Content-Type": {"text/event-stream"}}
		return `{"id":77}`, nil
	}
	// Whole answer: reuse the parent's routing for sign-in and models.
	answer, err := h.magpieHostStub.HTTP(requestJSON)
	if err != nil {
		return "", err
	}
	var wire wireResponse
	if err := json.Unmarshal([]byte(answer), &wire); err != nil {
		return "", err
	}
	body, err := base64.StdEncoding.DecodeString(wire.BodyBase64)
	if err != nil {
		return "", err
	}
	h.chunks = []string{string(body)}
	h.status = wire.Status
	h.headers = wire.Headers
	h.final = wire.FinalURL
	return `{"id":77}`, nil
}

func (h *streamHostStub) StreamNext(int64) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failOn > 0 && len(h.chunks) == h.failOn {
		return "", errors.New("client read failed")
	}
	if len(h.chunks) == 0 {
		status := h.status
		if status == 0 {
			status = 200
		}
		headers := h.headers
		if headers == nil {
			headers = map[string][]string{"Content-Type": {"text/event-stream"}}
		}
		out, _ := json.Marshal(map[string]any{"done": true, "status": status, "headers": headers, "finalUrl": h.final})
		return string(out), nil
	}
	chunk := h.chunks[0]
	h.chunks = h.chunks[1:]
	out, _ := json.Marshal(map[string]any{"done": false,
		"chunkBase64": base64.StdEncoding.EncodeToString([]byte(chunk))})
	return string(out), nil
}

func (h *streamHostStub) StreamClose(int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed++
}

// setChunks installs the pieces a chat request will be answered with.
func (h *streamHostStub) setChunks(chunks []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.chatBody = chunks
}

func TestStreamBridgeDeliversChunksInOrder(t *testing.T) {
	h := &streamHostStub{}
	h.setChunks([]string{sseLine(`{"a":1}`), sseLine(`{"b":2}`)})
	h.final = "https://gw.test/v1/chat/completions"
	bridge := streamBridge{h: h, assign: nextHostRequestID}
	var got strings.Builder
	pieces := 0
	resp, err := bridge.DoStream(context.Background(), &magpieplugin.Request{
		Method: "POST", URL: "https://gw.test/v1/chat/completions",
		Header: map[string][]string{"Authorization": {"Bearer magpie"}}, Body: []byte(`{"stream":true}`),
	}, func(chunk []byte) error {
		pieces++
		got.Write(chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || resp.URL != "https://gw.test/v1/chat/completions" {
		t.Fatalf("response = %+v", resp)
	}
	if pieces != 2 || !strings.Contains(got.String(), `{"b":2}`) {
		t.Fatalf("chunks = %q in %d pieces", got.String(), pieces)
	}
	if h.opened != 1 || h.closed != 1 {
		t.Fatalf("opened=%d closed=%d", h.opened, h.closed)
	}
}

// A handler that stops the stream must close the request, not leave it open.
func TestStreamBridgeClosesWhenTheConsumerStops(t *testing.T) {
	h := &streamHostStub{}
	h.setChunks([]string{"a", "b", "c"})
	bridge := streamBridge{h: h, assign: nextHostRequestID}
	stop := errors.New("enough")
	if _, err := bridge.DoStream(context.Background(), &magpieplugin.Request{URL: "https://gw.test/chat/completions"}, func([]byte) error {
		return stop
	}); !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
	if h.closed != 1 {
		t.Fatalf("stream was not closed: %d", h.closed)
	}
}

func TestStreamBridgeReportsClientFailure(t *testing.T) {
	h := &streamHostStub{failOn: 1}
	h.setChunks([]string{"a"})
	bridge := streamBridge{h: h, assign: nextHostRequestID}
	if _, err := bridge.DoStream(context.Background(), &magpieplugin.Request{URL: "https://gw.test/chat/completions"}, func([]byte) error {
		return nil
	}); err == nil {
		t.Fatal("a failed read was reported as a complete stream")
	}
	if h.closed != 1 {
		t.Fatalf("stream was not closed after a failure: %d", h.closed)
	}
}

func TestStreamBridgeHonoursCancellation(t *testing.T) {
	h := &streamHostStub{}
	h.setChunks([]string{"a", "b"})
	bridge := streamBridge{h: h, assign: nextHostRequestID}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := bridge.DoStream(ctx, &magpieplugin.Request{URL: "https://gw.test/chat/completions"}, func([]byte) error {
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if h.closed != 1 {
		t.Fatalf("cancelled stream was not closed: %d", h.closed)
	}
}

// The ids come from the same counter as whole requests, so a cancel never names
// another task.
func TestStreamRequestIDsDoNotRepeat(t *testing.T) {
	seen := map[int64]bool{}
	for i := 0; i < 5; i++ {
		id := nextHostRequestID()
		if seen[id] {
			t.Fatalf("id %d was reused", id)
		}
		seen[id] = true
	}
}

// A client that cannot stream keeps getting whole replies: the plugin host must
// not be offered DoStream at all, or every package would fail.
func TestPlainHostIsNotAStreamer(t *testing.T) {
	plain := &mobileHost{host: &fakeHost{data: map[string]string{}}}
	if _, ok := any(plain).(magpieplugin.Streamer); ok {
		t.Fatal("a non-streaming client was offered DoStream")
	}
	streaming := &mobileStreamHost{mobileHost: mobileHost{host: &fakeHost{data: map[string]string{}}}, stream: &streamHostStub{}}
	if _, ok := any(streaming).(magpieplugin.Streamer); !ok {
		t.Fatal("a streaming client was not offered DoStream")
	}
}

// The package itself reads a chunked reply when the host can stream: this is the
// path that makes a chat arrive as it is written.
//
// It used to wedge: the package pushes text through __magpieChatPush while its
// own hook still holds the JavaScript loop, and the push was a blocking pipe
// write. The reply now travels through a buffer that never blocks the writer,
// so the hook always returns.
func TestMagpieChatUsesTheChunkedHost(t *testing.T) {
	h := &streamHostStub{}
	h.setChunks([]string{
		sseLine(`{"choices":[{"delta":{"content":"Hel"}}]}`),
		sseLine(`{"choices":[{"delta":{"content":"lo"}}]}`),
		sseLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		sseLine(`[DONE]`),
	})
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	// ChatStart needs a package whose loader is declared, which is what the
	// provider package itself does; the flow helper is not part of that.
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}

	m := NewMagpieWithStream(h, h)
	defer m.Close()
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"","expires":0}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}

	started, err := m.ChatStart("gpt-4o", `[{"role":"user","content":"hi"}]`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(started), &start); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		event, err := m.ChatNext(start.ID)
		if err != nil {
			t.Fatal(err)
		}
		if event == "" {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		var parsed struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(event), &parsed); err != nil {
			t.Fatalf("event %s: %v", event, err)
		}
		if parsed.Kind == "text" {
			text.WriteString(parsed.Text)
		}
		if parsed.Kind == "error" {
			t.Fatalf("stream failed: %s", parsed.Text)
		}
		if parsed.Kind == "end" {
			break
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q", text.String())
	}
	if h.opened == 0 {
		t.Fatal("the chunked host was never used")
	}
}
