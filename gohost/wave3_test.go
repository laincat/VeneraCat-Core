package gohost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/plugin"
	"github.com/laincat/veneracat-core/video/danmaku"
	"github.com/laincat/veneracat-core/video/offline"
)

func TestWave3OfflineDanmakuAndAniBaka(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"list":[{"id":"100","title":"Starlight"}]}`)
		case "/detail/100":
			fmt.Fprint(w, `{"title":"Starlight","episodes":[{"id":"ep1","title":"EP 1"}]}`)
		case "/play/ep1":
			fmt.Fprintf(w, `{"url":"%s/stream/index.m3u8"}`, "http://"+r.Host)
		case "/stream/index.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nseg0.ts\n#EXTINF:4.0,\nseg1.ts\n#EXT-X-ENDLIST\n")
		case "/stream/seg0.ts":
			fmt.Fprint(w, "segment-zero-bytes")
		case "/stream/seg1.ts":
			fmt.Fprint(w, "segment-one-bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := Open(Options{DataDir: t.TempDir(), AppVersion: "1.6.3"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. AniBaka rule installed and invoked through gohost.
	rule := fmt.Sprintf(`{
		"$schema": "anx-rule/2",
		"key": "anx_gohost_test",
		"name": "Gohost AniBaka",
		"version": "1.0.0",
		"baseUrl": %q,
		"search": {"steps": [
			{"op": "fetch", "url": "/search"},
			{"op": "json", "path": "$.list[*]", "all": true, "jsonFields": {"id": "$.id", "title": "$.title"}, "to": "items"}
		]},
		"detail": {"steps": [
			{"op": "fetch", "url": "/detail/{{id}}"},
			{"op": "json", "path": "$.title", "to": "title", "keepPipe": true},
			{"op": "json", "path": "$.episodes[*]", "all": true, "jsonFields": {"id": "$.id", "title": "$.title"}, "to": "lines"}
		]},
		"play": {"steps": [
			{"op": "fetch", "url": "/play/{{episodeId}}"},
			{"op": "json", "path": "$.url", "to": "videoUrl"}
		]}
	}`, srv.URL)

	rec, err := c.InstallTyped(ctx, plugin.TypeAniBaka, rule, "file", "")
	if err != nil {
		t.Fatalf("InstallTyped anibaka: %v", err)
	}
	page, err := c.VideoSearch(ctx, rec.Key, "star", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("VideoSearch = %+v err=%v", page, err)
	}
	det, err := c.VideoDetails(ctx, rec.Key, "100")
	if err != nil || len(det.Lines) != 1 {
		t.Fatalf("VideoDetails = %+v err=%v", det, err)
	}
	play, err := c.VideoPlay(ctx, rec.Key, "100", det.Lines[0].ID, "ep1")
	if err != nil || !strings.HasSuffix(play.URL, "/stream/index.m3u8") {
		t.Fatalf("VideoPlay = %+v err=%v", play, err)
	}

	// 2. Offline cache plan, download, verify, list, remove using default cache dir.
	req := offline.TaskRequest{
		TaskID: "starlight_ep1",
		URL:    play.URL,
	}
	plan, err := c.PlanVideoOffline(ctx, req)
	if err != nil || len(plan.Items) != 2 {
		t.Fatalf("PlanVideoOffline = %+v err=%v", plan, err)
	}
	res, err := c.DownloadVideoOffline(ctx, req, nil)
	if err != nil || res.Status != offline.StatusCompleted || res.CompletedItems != 2 {
		t.Fatalf("DownloadVideoOffline = %+v err=%v", res, err)
	}
	vr, err := c.VerifyVideoOffline("", "starlight_ep1")
	if err != nil || !vr.Valid {
		t.Fatalf("VerifyVideoOffline = %+v err=%v", vr, err)
	}
	list, err := c.ListVideoOffline("")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListVideoOffline = %+v err=%v", list, err)
	}
	freed, err := c.RemoveVideoOffline("", "starlight_ep1")
	if err != nil || freed <= 0 {
		t.Fatalf("RemoveVideoOffline = %d err=%v", freed, err)
	}

	// 3. Local danmaku parsing, window query and layout.
	xmlData := []byte(`<i><d p="1.5,1,25,16777215,0,0,u1,1">First</d><d p="1.5,1,25,16711680,0,0,u2,2">Second</d></i>`)
	tl, err := c.ParseDanmaku(danmaku.FormatAuto, xmlData)
	if err != nil || len(tl.Comments) != 2 {
		t.Fatalf("ParseDanmaku = %+v err=%v", tl, err)
	}
	win, err := c.QueryDanmakuWindow(danmaku.FormatXML, xmlData, 1000, 2000, 0)
	if err != nil || len(win) != 2 {
		t.Fatalf("QueryDanmakuWindow = %+v err=%v", win, err)
	}
	layout, err := c.LayoutDanmaku(danmaku.FormatXML, xmlData, danmaku.LayoutOptions{ViewportWidth: 1280, ViewportHeight: 720, MaxTracks: 4})
	if err != nil || len(layout.Placed) != 2 || layout.Placed[0].Track == layout.Placed[1].Track {
		t.Fatalf("LayoutDanmaku = %+v err=%v", layout, err)
	}
}
