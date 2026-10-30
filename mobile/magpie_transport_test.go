package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/magpieplugin"
)

// CoreHost on iOS requires a request id to decode the request and cancel its
// URLSession task. A permissive map-based fake would miss an absent id.
type magpieWireHost struct {
	fakeHost
	requestMu sync.Mutex
	requests  []wireRequest
}

func (h *magpieWireHost) HTTP(input string) (string, error) {
	var request wireRequest
	if err := json.Unmarshal([]byte(input), &request); err != nil {
		return "", err
	}
	if request.ID <= 0 {
		return "", errors.New("the iOS host requires a positive request id")
	}
	h.requestMu.Lock()
	h.requests = append(h.requests, request)
	h.requestMu.Unlock()
	body, err := json.Marshal(wireResponse{
		Status: http.StatusCreated, Headers: http.Header{"X-Reply": {"kept"}},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"n":9007199254740993}`)),
		FinalURL:   "https://vendor.test/final",
	})
	return string(body), err
}

func TestMagpieHTTPMatchesTheIOSWireContract(t *testing.T) {
	h := &magpieWireHost{}
	bridge := &mobileHost{host: h}
	response, err := bridge.Do(context.Background(), &magpieplugin.Request{
		Method: http.MethodPost, URL: "https://vendor.test/messages",
		Header: map[string][]string{"X-Test": {"one", "two"}}, Body: []byte(`{"model":"m"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := h.requests[0]
	body, err := base64.StdEncoding.DecodeString(request.BodyBase64)
	if err != nil || string(body) != `{"model":"m"}` {
		t.Fatalf("request body was not preserved: %q, %v", body, err)
	}
	if request.Cookies || request.Partition != "magpie" || request.Method != http.MethodPost ||
		request.URL != "https://vendor.test/messages" || len(request.Headers["X-Test"]) != 2 {
		t.Fatalf("wire request lost fields or enabled cookies: %+v", request)
	}
	if response.Status != http.StatusCreated || http.Header(response.Header).Get("X-Reply") != "kept" ||
		response.URL != "https://vendor.test/final" || string(response.Body) != `{"n":9007199254740993}` {
		t.Fatalf("wire response was not preserved: %+v", response)
	}
}

func TestComicAndMagpieRequestsHaveDistinctHostIDs(t *testing.T) {
	h := &magpieWireHost{}
	for range 2 {
		comic := &hostBridge{h: h}
		if _, err := comic.Do(context.Background(), &host.Request{URL: "https://vendor.test/comic"}); err != nil {
			t.Fatal(err)
		}
		magpie := &mobileHost{host: h}
		if _, err := magpie.Do(context.Background(), &magpieplugin.Request{URL: "https://vendor.test/ai"}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	for _, request := range h.requests {
		if seen[request.ID] {
			t.Fatalf("host id %d was reused across bridges; cancellation could stop another task", request.ID)
		}
		seen[request.ID] = true
	}
}

type magpieBlockingHost struct {
	fakeHost
	started   chan int64
	cancelled chan int64
	release   chan struct{}
}

func (h *magpieBlockingHost) HTTP(input string) (string, error) {
	var request wireRequest
	if err := json.Unmarshal([]byte(input), &request); err != nil {
		return "", err
	}
	h.started <- request.ID
	<-h.release
	return `{"status":200,"bodyBase64":""}`, nil
}

func (h *magpieBlockingHost) CancelHTTP(id int64) { h.cancelled <- id }

func TestMagpieCancellationReachesTheSameHostRequest(t *testing.T) {
	h := &magpieBlockingHost{started: make(chan int64, 1), cancelled: make(chan int64, 1), release: make(chan struct{})}
	defer close(h.release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&mobileHost{host: h}).Do(ctx, &magpieplugin.Request{URL: "https://vendor.test/slow"})
		done <- err
	}()
	var id int64
	select {
	case id = <-h.started:
	case <-time.After(time.Second):
		t.Fatal("host request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled request returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Magpie still waited for the whole host response after cancellation")
	}
	select {
	case got := <-h.cancelled:
		if got != id || got <= 0 {
			t.Fatalf("cancelled id %d, active request %d", got, id)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach Host.CancelHTTP")
	}
}

func TestMagpiePrecancelledRequestDoesNotReachTheHost(t *testing.T) {
	h := &magpieWireHost{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&mobileHost{host: h}).Do(ctx, &magpieplugin.Request{URL: "https://vendor.test/never"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled request returned %v", err)
	}
	if len(h.requests) != 0 {
		t.Fatal("pre-cancelled request reached the host")
	}
}
