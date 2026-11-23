package gohost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/plugin"
)

func TestVideoProviderAndMediaPreserveRangeHeadersAndSourceCookies(t *testing.T) {
	var mediaRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "video-login", Path: "/"})
			fmt.Fprint(w, "signed in")
			return
		}
		mediaRequests.Add(1)
		if r.Header.Get("Referer") != "https://video.example.test/" || r.Header.Get("Range") != "bytes=2-4" || r.Header.Get("Accept-Encoding") != "identity" {
			http.Error(w, "missing playback headers", http.StatusForbidden)
			return
		}
		cookie, err := r.Cookie("sid")
		if err != nil || cookie.Value != "video-login" {
			http.Error(w, "source login did not reach media", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 2-4/6")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, "cde")
	}))
	defer srv.Close()
	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	script := fmt.Sprintf(`class VideoProbe extends VideoSource {
 name = "Desktop video probe"; key = "desktop_video_probe"; version = "1.0.0"
 search = {load: async () => {
   await Network.get(%q + "/login", {})
   return {videos: [{id: "clip", title: "Probe Clip"}], maxPage: 1}
 }}
 video = {
  loadInfo: async () => ({id: "clip", title: "Probe Clip", lines: [{id: "line1", name: "Line 1", episodes: [{id: "ep1", title: "Episode 1"}]}]}),
  loadPlay: async () => ({url: %q + "/clip.mp4", format: "mp4", headers: {Referer: "https://video.example.test/"}})
 }
}`, srv.URL, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record, err := c.InstallTyped(ctx, plugin.TypeVideoJS, script, "file", "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.VideoSearch(ctx, record.Key, "probe", "")
	if err != nil || page == nil || len(page.Items) != 1 || page.Items[0].ID != "clip" {
		t.Fatalf("video search = %+v, %v", page, err)
	}
	details, err := c.VideoDetails(ctx, record.Key, "clip")
	if err != nil || details == nil || len(details.Lines) != 1 || details.Lines[0].Name != "Line 1" {
		t.Fatalf("video details = %+v, %v", details, err)
	}
	play, err := c.VideoPlay(ctx, record.Key, "clip", "line1", "ep1")
	if err != nil {
		t.Fatal(err)
	}
	headers := play.Header.Clone()
	headers.Set("Range", "bytes=2-4")
	response, err := c.OpenVideoMedia(ctx, record.Key, play.URL, headers)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusPartialContent || string(body) != "cde" || response.Header.Get("Content-Range") != "bytes 2-4/6" {
		t.Fatalf("media response = %d %q, %v", response.StatusCode, body, readErr)
	}
	if play.Header.Get("Range") != "" {
		t.Fatal("the media request mutated the provider's header declaration")
	}
	if err := c.SetEnabled(ctx, record.Key, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenVideoMedia(ctx, record.Key, play.URL, headers); err == nil {
		t.Fatal("a disabled video provider still opened media")
	}
	if got := mediaRequests.Load(); got != 1 {
		t.Fatalf("media requests = %d, want 1", got)
	}
}
