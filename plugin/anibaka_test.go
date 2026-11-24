package plugin

import (
	"context"
	"net/http"
	"testing"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/subscription"
)

type anibakaFakeTransport struct {
	routes map[string]string
}

func (f *anibakaFakeTransport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, ok := f.routes[req.URL]
	if !ok {
		return &host.Response{Status: http.StatusNotFound, FinalURL: req.URL}, nil
	}
	return &host.Response{Status: http.StatusOK, Body: []byte(body), FinalURL: req.URL}, nil
}

const sampleAniBakaRule = `{
	"$schema": "anx-rule/2",
	"key": "anx_sample",
	"name": "Sample AniBaka",
	"version": "1.0.0",
	"baseUrl": "https://sample.test",
	"search": {
		"steps": [
			{"op": "fetch", "url": "/s?q={{keyword}}"},
			{"op": "json", "path": "$.items[*]", "all": true, "jsonFields": {"id": "$.id", "title": "$.title"}, "to": "items"}
		]
	},
	"detail": {
		"steps": [
			{"op": "fetch", "url": "/d/{{id}}"},
			{"op": "json", "path": "$.title", "to": "title", "keepPipe": true},
			{"op": "json", "path": "$.eps[*]", "all": true, "jsonFields": {"id": "$.id", "title": "$.title"}, "to": "lines"}
		]
	},
	"play": {
		"steps": [
			{"op": "const", "value": "https://cdn.sample.test/{{episodeId}}.m3u8", "to": "videoUrl"}
		]
	}
}`

func TestRegistryAniBakaLifecycle(t *testing.T) {
	tr := &anibakaFakeTransport{
		routes: map[string]string{
			"https://sample.test/s?q=cat": `{"items":[{"id":"10","title":"VeneraCat"}]}`,
			"https://sample.test/d/10":    `{"title":"VeneraCat","eps":[{"id":"ep1","title":"EP 1"}]}`,
		},
	}
	reg := New(nil, tr)
	ctx := context.Background()

	idxJSON := `{
		"$schema": "vcvideo/1",
		"name": "Video Index",
		"plugins": [
			{"type": "anibaka-rule", "key": "anx_sample", "name": "Sample AniBaka", "version": "1.0.0",
			 "url": "sample.anx.json", "sha256": "` + digest(sampleAniBakaRule) + `"}
		]
	}`
	idx, err := subscription.Parse([]byte(idxJSON), "https://index.test/vcvideo.json")
	if err != nil {
		t.Fatalf("Parse index: %v", err)
	}
	rec, err := reg.InstallFromVideoIndex(ctx, "https://index.test/vcvideo.json", idx.Plugins[0], sampleAniBakaRule)
	if err != nil {
		t.Fatalf("InstallFromVideoIndex: %v", err)
	}
	if rec.Type != TypeAniBaka || rec.Key != "anx_sample" || !rec.Enabled {
		t.Fatalf("unexpected record: %+v", rec)
	}

	vp, err := reg.Video("anx_sample")
	if err != nil {
		t.Fatalf("Video: %v", err)
	}
	page, err := vp.Search(ctx, "cat", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Title != "VeneraCat" {
		t.Fatalf("Search = %+v err=%v", page, err)
	}
	det, err := vp.Details(ctx, "10")
	if err != nil || len(det.Lines) != 1 || det.Lines[0].Episodes[0].ID != "ep1" {
		t.Fatalf("Details = %+v err=%v", det, err)
	}
	play, err := vp.Play(ctx, "10", det.Lines[0].ID, "ep1")
	if err != nil || play.URL != "https://cdn.sample.test/ep1.m3u8" {
		t.Fatalf("Play = %+v err=%v", play, err)
	}

	// Disable, replace while disabled, re-enable, and restore in a fresh registry.
	if err := reg.SetEnabled(ctx, "anx_sample", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	updated := sampleAniBakaRule
	if _, err := reg.Replace(ctx, "anx_sample", TypeAniBaka, updated, "test", "", ""); err != nil {
		t.Fatalf("Replace while disabled: %v", err)
	}
	if err := reg.SetEnabled(ctx, "anx_sample", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	reg2 := New(nil, tr)
	if failed := reg2.Restore(ctx, reg.List()); len(failed) != 0 {
		t.Fatalf("Restore failed: %v", failed)
	}
	if _, err := reg2.Video("anx_sample"); err != nil {
		t.Fatalf("Video after restore: %v", err)
	}
}
