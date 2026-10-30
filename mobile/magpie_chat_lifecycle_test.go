package mobile

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type chatRequestHost struct {
	fakeHost
	mu                sync.Mutex
	pending           map[int64]chan *string
	started           chan int64
	cancelled         chan int64
	refreshes         int
	wantAuthorization string
}

func newChatRequestHost(t *testing.T, authorization string) *chatRequestHost {
	h := &chatRequestHost{pending: map[int64]chan *string{}, started: make(chan int64, 8), cancelled: make(chan int64, 8), wantAuthorization: authorization}
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for id, done := range h.pending {
			done <- nil
			delete(h.pending, id)
		}
	})
	return h
}

func chatWireBody(body string) (string, error) {
	raw, err := json.Marshal(wireResponse{Status: 200, BodyBase64: base64.StdEncoding.EncodeToString([]byte(body))})
	return string(raw), err
}

func (h *chatRequestHost) HTTP(input string) (string, error) {
	var req wireRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", err
	}
	if strings.HasSuffix(req.URL, "/refresh") {
		h.mu.Lock()
		h.refreshes++
		h.mu.Unlock()
		return chatWireBody(`{"key":"refreshed-key"}`)
	}
	authorization := ""
	for name, values := range req.Headers {
		if strings.EqualFold(name, "Authorization") && len(values) != 0 {
			authorization = values[0]
		}
	}
	if authorization != h.wantAuthorization {
		return "", errors.New("request did not carry the current fake credential")
	}
	if strings.HasSuffix(req.URL, "/models") {
		return chatWireBody(`{"data":[{"id":"fixture"}]}`)
	}
	done := make(chan *string, 1)
	h.mu.Lock()
	h.pending[req.ID] = done
	h.mu.Unlock()
	h.started <- req.ID
	result := <-done
	if result == nil {
		return "", errors.New("request cancelled")
	}
	return chatWireBody(*result)
}

func (h *chatRequestHost) CancelHTTP(id int64) {
	h.mu.Lock()
	done := h.pending[id]
	delete(h.pending, id)
	h.mu.Unlock()
	if done != nil {
		done <- nil
		h.cancelled <- id
	}
}

func (h *chatRequestHost) answer(t *testing.T, id int64) {
	t.Helper()
	h.mu.Lock()
	done := h.pending[id]
	delete(h.pending, id)
	h.mu.Unlock()
	if done == nil {
		t.Fatalf("request %d was already cancelled or completed", id)
	}
	body := sseLine(`{"choices":[{"delta":{"content":"fixture reply"}}]}`) + sseLine(`[DONE]`)
	done <- &body
}

func awaitChatRequest(t *testing.T, h *chatRequestHost) int64 {
	t.Helper()
	select {
	case id := <-h.started:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("chat did not reach HTTP")
	}
	return 0
}

func awaitChatText(t *testing.T, m *Magpie, id int64) string {
	t.Helper()
	var text strings.Builder
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := m.ChatNext(id)
		if err != nil {
			t.Fatal(err)
		}
		if raw == "" {
			time.Sleep(time.Millisecond)
			continue
		}
		var event struct{ Kind, Text string }
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Kind {
		case "text":
			text.WriteString(event.Text)
		case "error":
			t.Fatalf("chat failed: %s", event.Text)
		case "end":
			return text.String()
		}
	}
	t.Fatal("chat did not finish")
	return ""
}

func TestMagpieChatCancelDoesNotStopOtherOrSubsequentChats(t *testing.T) {
	h := newChatRequestHost(t, "Bearer fake-key")
	m := NewMagpie(h)
	defer m.Close()
	chatCancelPackage(t, m)
	first := startCancelChat(t, m)
	firstRequest := awaitChatRequest(t, h)
	second := startCancelChat(t, m)
	secondRequest := awaitChatRequest(t, h)
	m.ChatCancel(first)
	select {
	case id := <-h.cancelled:
		if id != firstRequest {
			t.Fatalf("cancelled %d, wanted %d", id, firstRequest)
		}
	case <-time.After(time.Second):
		t.Fatal("first request was not cancelled")
	}
	m.ChatCancel(first) // An old handle must not reach the surviving request.
	h.answer(t, secondRequest)
	if text := awaitChatText(t, m, second); text != "fixture reply" {
		t.Fatalf("second reply=%q", text)
	}
	third := startCancelChat(t, m)
	thirdRequest := awaitChatRequest(t, h)
	h.answer(t, thirdRequest)
	if text := awaitChatText(t, m, third); text != "fixture reply" {
		t.Fatalf("subsequent reply=%q", text)
	}
	if _, err := m.Models(); err != nil {
		t.Fatalf("model hook after cancellation: %v", err)
	}
}

func TestMagpieChatRefreshSurvivesCancellationAndReachesOtherHooks(t *testing.T) {
	h := newChatRequestHost(t, "Bearer refreshed-key")
	m := NewMagpie(h)
	defer m.Close()
	script := `export async function Plugin(input) {
 return {auth:{provider:"refresh-fixture",methods:[],loader:async(getAuth)=>{
  let auth=await getAuth();
  if(auth.key==="expired-key") {
   const reply=await fetch("https://vendor.test/refresh");
   auth={type:"api",key:(await reply.json()).key};
   await input.client.auth.set({path:{id:"refresh-fixture"},body:auth});
  }
  return {baseURL:"https://vendor.test/v1",fetch:(url,init={})=>fetch(url,{...init,headers:{...init.headers,Authorization:"Bearer "+auth.key}})};
 }}};
}`
	files := map[string]string{"package.json": base64.StdEncoding.EncodeToString([]byte(`{"name":"refresh-fixture","main":"./index.mjs"}`)), "index.mjs": base64.StdEncoding.EncodeToString([]byte(script))}
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAuth("refresh-fixture", `{"type":"api","key":"expired-key"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Auth(); err != nil {
		t.Fatal(err)
	} // Create the ordinary hook runtime before the chat refresh.
	first := startCancelChat(t, m)
	request := awaitChatRequest(t, h)
	m.ChatCancel(first)
	select {
	case got := <-h.cancelled:
		if got != request {
			t.Fatal("cancelled another request")
		}
	case <-time.After(time.Second):
		t.Fatal("chat was not cancelled")
	}
	saved, err := m.SavedAuth()
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]string
	if err := json.Unmarshal([]byte(saved), &auth); err != nil {
		t.Fatal(err)
	}
	if auth["key"] != "refreshed-key" {
		t.Fatal("refreshed credential was not saved")
	}
	next := startCancelChat(t, m)
	nextRequest := awaitChatRequest(t, h)
	h.answer(t, nextRequest)
	if text := awaitChatText(t, m, next); text != "fixture reply" {
		t.Fatalf("next reply=%q", text)
	}
	if _, err := m.Models(); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	refreshes := h.refreshes
	h.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refreshed %d times; new chats and the existing model hook must reuse the updated account", refreshes)
	}
}

type chatReadHost struct {
	streamHostStub
	muID      sync.Mutex
	requestID int64
	waiting   chan int64
	cancelled chan int64
	release   chan struct{}
	once      sync.Once
}

func (h *chatReadHost) StreamOpen(input string) (string, error) {
	var req wireRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return "", err
	}
	h.muID.Lock()
	h.requestID = req.ID
	h.muID.Unlock()
	return fmt.Sprintf(`{"id":%d}`, req.ID), nil
}
func (h *chatReadHost) StreamNext(id int64) (string, error) {
	h.waiting <- id
	<-h.release
	return "", errors.New("stream read cancelled")
}
func (h *chatReadHost) StreamClose(id int64) {
	h.muID.Lock()
	matches := id == h.requestID
	h.muID.Unlock()
	if matches {
		h.once.Do(func() { h.cancelled <- id; close(h.release) })
	}
}
func TestMagpieChatCancelReachesBlockedStreamRead(t *testing.T) {
	h := &chatReadHost{waiting: make(chan int64, 1), cancelled: make(chan int64, 1), release: make(chan struct{})}
	m := NewMagpieWithStream(h, h)
	defer m.Close()
	defer h.once.Do(func() { close(h.release) })
	chatCancelPackage(t, m)
	handle := startCancelChat(t, m)
	var id int64
	select {
	case id = <-h.waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("stream reader did not start")
	}
	m.ChatCancel(handle)
	select {
	case got := <-h.cancelled:
		if got != id {
			t.Fatalf("closed %d, reader %d", got, id)
		}
	case <-time.After(time.Second):
		t.Fatal("ChatCancel did not close blocked StreamNext")
	}
}
