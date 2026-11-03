package mobile

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// aiStreamStub answers a standard AI request piece by piece. It shares the
// whole-answer host for everything else, so building a Core does not need a
// second fake.
type aiStreamStub struct {
	*fakeHost
	mu        sync.Mutex
	chunks    []string
	status    int
	final     string
	opened    int
	closed    int
	partition string
	lastBody  string
	lastAuth  string
	failNext  bool
}

func (h *aiStreamStub) StreamOpen(requestJSON string) (string, error) {
	var req streamRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened++
	h.partition = req.Partition
	h.lastAuth = req.Headers.Get("Authorization")
	body, err := base64.StdEncoding.DecodeString(req.BodyBase64)
	if err != nil {
		return "", err
	}
	h.lastBody = string(body)
	return `{"id":91}`, nil
}

func (h *aiStreamStub) StreamNext(int64) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failNext {
		return "", errStreamBroken
	}
	if len(h.chunks) == 0 {
		status := h.status
		if status == 0 {
			status = 200
		}
		out, _ := json.Marshal(map[string]any{"done": true, "status": status,
			"headers": map[string][]string{"Content-Type": {"text/event-stream"}}, "finalUrl": h.final})
		return string(out), nil
	}
	chunk := h.chunks[0]
	h.chunks = h.chunks[1:]
	out, _ := json.Marshal(map[string]any{"done": false,
		"chunkBase64": base64.StdEncoding.EncodeToString([]byte(chunk))})
	return string(out), nil
}

func (h *aiStreamStub) StreamClose(int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed++
}

var errStreamBroken = &streamTestError{"client read failed"}

type streamTestError struct{ message string }

func (e *streamTestError) Error() string { return e.message }

const aiStreamConfig = `{"protocol":"chat","baseUrl":"https://gw.test/v1","apiKey":"sk-test"}`

const aiStreamBody = `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func newAIStreamCore(t *testing.T, stub *aiStreamStub) *Core {
	t.Helper()
	core, err := openCore("{}", stub.fakeHost, stub)
	if err != nil {
		t.Fatal(err)
	}
	return core
}

func TestSendAIRequestStreamCollectsDeltas(t *testing.T) {
	stub := &aiStreamStub{fakeHost: &fakeHost{}, final: "https://gw.test/v1/chat/completions"}
	stub.chunks = []string{
		// Deliberately split inside "data:" to prove the decoder buffers.
		`da`,
		`ta: {"choices":[{"delta":{"content":"你好"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"，世界"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	core := newAIStreamCore(t, stub)
	defer core.Close()
	answer, err := core.SendAIRequestStream(core.NewCall(), aiStreamConfig, aiStreamBody)
	if err != nil {
		t.Fatal(err)
	}
	var got aiStreamResponse
	if err := json.Unmarshal([]byte(answer), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != "你好，世界" {
		t.Fatalf("text = %q", got.Text)
	}
	if !got.Streamed || got.Status != 200 {
		t.Fatalf("answer = %+v", got)
	}
	if got.FinalURL != stub.final {
		t.Fatalf("finalUrl = %q", got.FinalURL)
	}
	if stub.lastBody != aiStreamBody {
		t.Fatalf("the body Core sent is not the body the caller wrote: %q", stub.lastBody)
	}
	if stub.lastAuth != "Bearer sk-test" {
		t.Fatalf("authorization = %q", stub.lastAuth)
	}
	if stub.partition != "" {
		t.Fatalf("partition = %q; an AI request must not borrow a comic source's jar", stub.partition)
	}
	if stub.closed != 1 {
		t.Fatalf("stream closed %d times", stub.closed)
	}
}

func TestSendAIRequestStreamNeedsTheStreamingBody(t *testing.T) {
	stub := &aiStreamStub{fakeHost: &fakeHost{}}
	core := newAIStreamCore(t, stub)
	defer core.Close()
	_, err := core.SendAIRequestStream(core.NewCall(), aiStreamConfig,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatalf("err = %v, want a complaint about the missing stream flag", err)
	}
	if stub.opened != 0 {
		t.Fatal("a request was opened before the body was checked")
	}
}

func TestSendAIRequestStreamNeedsAStreamingCore(t *testing.T) {
	// New (not NewWithStream): the whole-answer constructor must say so rather
	// than read a stream as an empty reply.
	core, err := New("{}", &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	_, err = core.SendAIRequestStream(core.NewCall(), aiStreamConfig, aiStreamBody)
	if err == nil || !strings.Contains(err.Error(), "cannot stream") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendAIRequestStreamSurfacesProviderError(t *testing.T) {
	stub := &aiStreamStub{fakeHost: &fakeHost{}}
	stub.chunks = []string{`data: {"error":{"message":"invalid api key","type":"auth"}}` + "\n\n"}
	core := newAIStreamCore(t, stub)
	defer core.Close()
	_, err := core.SendAIRequestStream(core.NewCall(), aiStreamConfig, aiStreamBody)
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("err = %v, want the provider's message", err)
	}
	if stub.closed != 1 {
		t.Fatalf("a failed stream was not closed (closed=%d)", stub.closed)
	}
}

func TestSendAIRequestStreamSurfacesTransportFailure(t *testing.T) {
	stub := &aiStreamStub{fakeHost: &fakeHost{}, failNext: true}
	core := newAIStreamCore(t, stub)
	defer core.Close()
	_, err := core.SendAIRequestStream(core.NewCall(), aiStreamConfig, aiStreamBody)
	if err == nil || !strings.Contains(err.Error(), "client read failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendAIRequestStreamStopsWhenCancelled(t *testing.T) {
	stub := &aiStreamStub{fakeHost: &fakeHost{}}
	stub.chunks = []string{
		`data: {"choices":[{"delta":{"content":"first"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"second"}}]}` + "\n\n",
	}
	core := newAIStreamCore(t, stub)
	defer core.Close()
	id := core.NewCall()
	core.Cancel(id)
	_, err := core.SendAIRequestStream(id, aiStreamConfig, aiStreamBody)
	if err == nil {
		t.Fatal("a cancelled call still returned an answer")
	}
	if stub.closed != 0 && stub.opened != 0 {
		// Either the call never opened or it opened and was closed; what must
		// not happen is an answer after cancellation.
		t.Logf("opened=%d closed=%d", stub.opened, stub.closed)
	}
}

// The streamed answer is JSON on the wire even when it carries no text.
func TestSendAIRequestStreamEmptyAnswerIsStillJSON(t *testing.T) {
	stub := &aiStreamStub{fakeHost: &fakeHost{}, final: "https://gw.test/v1/chat/completions"}
	stub.chunks = []string{"data: [DONE]\n\n"}
	core := newAIStreamCore(t, stub)
	defer core.Close()
	answer, err := core.SendAIRequestStream(core.NewCall(), aiStreamConfig, aiStreamBody)
	if err != nil {
		t.Fatal(err)
	}
	var got aiStreamResponse
	if err := json.Unmarshal([]byte(answer), &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != "" {
		t.Fatalf("text = %q", got.Text)
	}
	if got.Headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("headers = %v", got.Headers)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("status = %d", got.Status)
	}
}
