package mobile

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/video/danmaku"
	"github.com/laincat/veneracat-core/video/offline"
)

func TestMobileWave3OfflineCancelResumeAndCleanup(t *testing.T) {
	h := &fakeHost{
		routes: map[string]string{
			"https://cdn.test/index.m3u8": "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nseg0.ts\n#EXTINF:4.0,\nseg1.ts\n#EXT-X-ENDLIST\n",
			"https://cdn.test/seg0.ts":    "seg0-data",
			"https://cdn.test/seg1.ts":    "seg1-data",
		},
		data: map[string]string{},
	}
	c := newCore(t, h)
	root := t.TempDir()

	// Pre-canceled callID must abort VideoOfflinePlan and release reservation.
	canceledID := c.NewCall()
	c.Cancel(canceledID)
	planReq, _ := json.Marshal(offline.TaskRequest{
		RootDir: root,
		TaskID:  "mob_ep1",
		URL:     "https://cdn.test/index.m3u8",
	})
	if _, err := c.VideoOfflinePlan(canceledID, string(planReq)); err == nil {
		t.Fatal("expected canceled VideoOfflinePlan to fail")
	}

	// Early return on malformed JSON must release callID reservation.
	badID := c.NewCall()
	if _, err := c.VideoOfflineDownload(badID, "{bad-json"); err == nil {
		t.Fatal("expected bad JSON to fail")
	}
	c.mu.Lock()
	if len(c.cancels) != 0 || len(c.reserved) != 0 {
		c.mu.Unlock()
		t.Fatalf("leaked call reservations: cancels=%d reserved=%d", len(c.cancels), len(c.reserved))
	}
	c.mu.Unlock()

	// Plan -> Download -> Inspect -> Verify -> List -> Prune -> Remove.
	planID := c.NewCall()
	planRaw, err := c.VideoOfflinePlan(planID, string(planReq))
	if err != nil {
		t.Fatalf("VideoOfflinePlan: %v", err)
	}
	var plan offline.Plan
	if err := json.Unmarshal([]byte(planRaw), &plan); err != nil || len(plan.Items) != 2 {
		t.Fatalf("plan = %s err=%v", planRaw, err)
	}

	dlID := c.NewCall()
	dlRaw, err := c.VideoOfflineDownload(dlID, string(planReq))
	if err != nil {
		t.Fatalf("VideoOfflineDownload: %v", err)
	}
	var dlRes offline.Result
	if err := json.Unmarshal([]byte(dlRaw), &dlRes); err != nil || dlRes.Status != offline.StatusCompleted || dlRes.CompletedItems != 2 {
		t.Fatalf("download result = %s err=%v", dlRaw, err)
	}

	if _, err := c.VideoOfflineInspect(root, "mob_ep1"); err != nil {
		t.Fatalf("VideoOfflineInspect: %v", err)
	}
	verRaw, err := c.VideoOfflineVerify(root, "mob_ep1")
	if err != nil {
		t.Fatalf("VideoOfflineVerify: %v", err)
	}
	var vr offline.VerifyResult
	if err := json.Unmarshal([]byte(verRaw), &vr); err != nil || !vr.Valid {
		t.Fatalf("verify = %s err=%v", verRaw, err)
	}
	listRaw, err := c.VideoOfflineList(root)
	if err != nil || !strings.Contains(listRaw, `"mob_ep1"`) {
		t.Fatalf("list = %s err=%v", listRaw, err)
	}
	if _, err := c.VideoOfflineVerify(root, "../escape"); err == nil {
		t.Fatal("path traversal was accepted by VideoOfflineVerify")
	}
	if _, err := c.VideoOfflinePrune(root, `{"removeIncomplete":true}`); err != nil {
		t.Fatalf("VideoOfflinePrune: %v", err)
	}
	remRaw, err := c.VideoOfflineRemove(root, "mob_ep1")
	if err != nil || !strings.Contains(remRaw, `"reclaimedBytes"`) {
		t.Fatalf("remove = %s err=%v", remRaw, err)
	}
	if _, err := c.VideoOfflineInspect(root, "mob_ep1"); !errors.Is(err, offline.ErrTaskNotFound) {
		t.Fatalf("inspect after remove = %v, want ErrTaskNotFound", err)
	}
}

func TestMobileWave3DanmakuAndAniBaka(t *testing.T) {
	h := &fakeHost{
		routes: map[string]string{
			"https://anx.test/search?q=eva": `{"items":[{"id":"e1","title":"Eva"}]}`,
			"https://anx.test/detail/e1":    `{"title":"Eva","eps":[{"id":"https://anx.test/watch/1","title":"EP1"}]}`,
			"https://anx.test/watch/1":      `<html><body>no direct url</body></html>`,
		},
		data: map[string]string{},
	}
	c := newCore(t, h)

	// 1. Danmaku parse, window query, layout & window layout.
	xmlRaw := `<i><d p="2.0,1,25,16777215,0,0,u1,1">Scroll A</d><d p="2.0,1,25,16711680,0,0,u2,2">Scroll B</d><d p="10.0,5,25,65280,0,0,u3,3">Top C</d></i>`
	parsed, err := c.ParseDanmaku("auto", xmlRaw)
	if err != nil {
		t.Fatalf("ParseDanmaku: %v", err)
	}
	var tl danmaku.Timeline
	if err := json.Unmarshal([]byte(parsed), &tl); err != nil || len(tl.Comments) != 3 {
		t.Fatalf("timeline = %s err=%v", parsed, err)
	}
	winRaw, err := c.DanmakuQueryWindow("json", parsed, 1500, 3000, 10)
	if err != nil || !strings.Contains(winRaw, "Scroll A") || strings.Contains(winRaw, "Top C") {
		t.Fatalf("DanmakuQueryWindow = %s err=%v", winRaw, err)
	}
	layoutRaw, err := c.DanmakuLayout("xml", xmlRaw, `{"viewportWidth":1280,"viewportHeight":720,"maxTracks":4}`)
	if err != nil {
		t.Fatalf("DanmakuLayout: %v", err)
	}
	var lr danmaku.LayoutResult
	if err := json.Unmarshal([]byte(layoutRaw), &lr); err != nil || len(lr.Placed) != 3 || lr.Placed[0].Track == lr.Placed[1].Track {
		t.Fatalf("DanmakuLayout = %s err=%v", layoutRaw, err)
	}
	winLayoutRaw, err := c.DanmakuLayoutWindow("json", parsed, 9000, 11000, `{"viewportWidth":1280,"viewportHeight":720,"maxTracks":4}`)
	if err != nil || !strings.Contains(winLayoutRaw, "Top C") {
		t.Fatalf("DanmakuLayoutWindow = %s err=%v", winLayoutRaw, err)
	}

	// 2. AniBaka rule install + Search / Details / Play (sniff fallback).
	rule := `{
		"$schema": "anx-rule/2",
		"key": "anx_mobile_test",
		"name": "Mobile AniBaka",
		"version": "1.0.0",
		"baseUrl": "https://anx.test",
		"hls": {"filterAds": true},
		"search": {"steps": [
			{"op": "fetch", "url": "/search?q={{keyword}}"},
			{"op": "json", "path": "$.items[*]", "all": true, "jsonFields": {"id": "$.id", "title": "$.title"}, "to": "items"}
		]},
		"detail": {"steps": [
			{"op": "fetch", "url": "/detail/{{id}}"},
			{"op": "json", "path": "$.title", "to": "title", "keepPipe": true},
			{"op": "json", "path": "$.eps[*]", "all": true, "jsonFields": {"id": "$.id", "title": "$.title"}, "to": "lines"}
		]},
		"play": {"steps": [
			{"op": "fetch", "url": "{{episodeId}}"},
			{"op": "regex", "pattern": "src=\"([^\"]+\\.m3u8)\"", "group": 1},
			{"op": "sniff", "whenEmpty": true, "page": "{{episodeId}}", "match": "\\.m3u8"}
		]}
	}`
	if _, err := c.Install(c.NewCall(), "anibaka-rule", rule, "file", "", ""); err != nil {
		t.Fatalf("Install anibaka-rule: %v", err)
	}
	searchRaw, err := c.VideoSearch(c.NewCall(), "anx_mobile_test", "eva", "")
	if err != nil || !strings.Contains(searchRaw, `"Eva"`) {
		t.Fatalf("VideoSearch = %s err=%v", searchRaw, err)
	}
	detRaw, err := c.VideoDetails(c.NewCall(), "anx_mobile_test", "e1")
	if err != nil || !strings.Contains(detRaw, `"EP1"`) {
		t.Fatalf("VideoDetails = %s err=%v", detRaw, err)
	}
	playRaw, err := c.VideoPlay(c.NewCall(), "anx_mobile_test", "e1", "line0", "https://anx.test/watch/1")
	if err != nil || !strings.Contains(playRaw, `"sniff"`) || !strings.Contains(playRaw, `"filterAds":true`) {
		t.Fatalf("VideoPlay = %s err=%v", playRaw, err)
	}
}
