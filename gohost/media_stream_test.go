package gohost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
)

func TestMediaStreamReturnsBeforeCompletionAndCancelsHTTP(t *testing.T) {
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		fmt.Fprint(w, "one")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}))
	defer srv.Close()
	tr, _ := newTestTransport(t)
	tr.client.Timeout = time.Nanosecond // Only the media path lifts this whole-response timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := tr.OpenStream(ctx, &host.Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, 3)
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "one" {
		t.Fatalf("first media chunk = %q, %v", first, err)
	}
	select {
	case <-stopped:
		t.Fatal("media only became available after the response ended")
	default:
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("canceling playback did not stop the upstream HTTP request")
	}
}

func TestMediaStreamRetainsCrossOriginRedirectCredentialPolicy(t *testing.T) {
	tr, _ := newTestTransport(t)
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"Authorization", "X-Api-Key", "Cookie"} {
			if r.Header.Get(key) != "" {
				t.Errorf("media redirect forwarded %s across origins", key)
			}
		}
		fmt.Fprint(w, "media")
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := tr.OpenStream(ctx, &host.Request{URL: first.URL, Header: http.Header{
		"Authorization": {"Bearer private"}, "X-Api-Key": {"private"}, "Cookie": {"sid=private"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "media" {
		t.Fatalf("media after redirect = %q, %v", body, err)
	}
}
