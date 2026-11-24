package anibaka

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
)

type fakeTransport struct {
	handler func(ctx context.Context, req *host.Request) (*host.Response, error)
	reqs    []*host.Request
}

func (f *fakeTransport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.reqs = append(f.reqs, req)
	return f.handler(ctx, req)
}

type fakeSniffer struct {
	res *host.SniffResult
	err error
	got *host.SniffRequest
}

func (f *fakeSniffer) Sniff(ctx context.Context, req *host.SniffRequest) (*host.SniffResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.got = req
	return f.res, f.err
}

func encryptAESCBCBase64(plain, key, iv string) string {
	block, _ := aes.NewCipher([]byte(key))
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append([]byte(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out)
}

func TestThreeStageHTMLPipelineCryptoAndHLS(t *testing.T) {
	const aesKey = "0123456789abcdef"
	const aesIV = "abcdef0123456789"
	encryptedMasterURL := encryptAESCBCBase64("https://stream.example.com/master.m3u8", aesKey, aesIV)

	ruleJSON := `{
		"$schema": "anx-rule/2",
		"key": "anx_demo_html",
		"name": "Demo Anime",
		"version": "1.0.0",
		"baseUrl": "https://anime.example.com",
		"hls": {"filterAds": true, "maxHeight": 720},
		"search": {
			"steps": [
				{"op": "fetch", "url": "/search?q={{keyword|urlEncode}}&p={{cursor}}", "to": "html"},
				{"op": "select", "mode": "css", "selector": ".card", "all": true, "fields": {
					"id": {"selector": "a", "attr": "href"},
					"title": {"selector": "h3", "attr": "text"},
					"cover": {"selector": "img", "attr": "src"}
				}, "to": "items"},
				{"op": "select", "from": "html", "mode": "css", "selector": "a.next", "attr": "data-page", "to": "next"}
			]
		},
		"detail": {
			"steps": [
				{"op": "fetch", "url": "{{id}}", "to": "page"},
				{"op": "select", "mode": "css", "selector": "h1.title", "attr": "text", "to": "title"},
				{"op": "select", "from": "page", "mode": "css", "selector": ".desc", "attr": "text", "to": "description"},
				{"op": "select", "from": "page", "mode": "xpath", "selector": "//ul[@class='eps']/li/a", "all": true, "fields": {
					"id": {"selector": ".", "attr": "href"},
					"title": {"selector": ".", "attr": "text"}
				}, "to": "lines"}
			]
		},
		"play": {
			"steps": [
				{"op": "fetch", "url": "{{episodeId}}"},
				{"op": "regex", "pattern": "data-enc=\"([^\"]+)\"", "group": 1},
				{"op": "crypto", "algorithm": "aes-cbc-decrypt", "key": "` + aesKey + `", "iv": "` + aesIV + `", "to": "masterUrl"},
				{"op": "fetch", "url": "{{masterUrl}}"},
				{"op": "hls", "selectVariant": "maxHeight", "maxHeight": 720, "outputMode": "url", "to": "videoUrl"}
			]
		}
	}`

	rule, err := Parse([]byte(ruleJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tr := &fakeTransport{
		handler: func(_ context.Context, req *host.Request) (*host.Response, error) {
			switch {
			case strings.HasPrefix(req.URL, "https://anime.example.com/search"):
				return &host.Response{
					Status: 200,
					Body: []byte(`<html><body>
						<div class="card"><a href="/show/1"><h3>Frieren</h3><img src="/img/1.jpg"/></a></div>
						<a class="next" data-page="2">Next</a>
					</body></html>`),
					FinalURL: req.URL,
				}, nil
			case req.URL == "https://anime.example.com/show/1":
				return &host.Response{
					Status: 200,
					Body: []byte(`<html><body>
						<h1 class="title">Frieren</h1>
						<div class="desc">Beyond Journey's End</div>
						<ul class="eps">
							<li><a href="/play/1/1">EP 1</a></li>
							<li><a href="/play/1/2">EP 2</a></li>
						</ul>
					</body></html>`),
					FinalURL: req.URL,
				}, nil
			case req.URL == "https://anime.example.com/play/1/1":
				return &host.Response{
					Status:   200,
					Body:     []byte(`<div data-enc="` + encryptedMasterURL + `"></div>`),
					FinalURL: req.URL,
				}, nil
			case req.URL == "https://stream.example.com/master.m3u8":
				return &host.Response{
					Status: 200,
					Body: []byte("#EXTM3U\n" +
						"#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080\n1080.m3u8\n" +
						"#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=1280x720\n720.m3u8\n"),
					FinalURL: req.URL,
				}, nil
			default:
				return &host.Response{Status: 404, FinalURL: req.URL}, nil
			}
		},
	}

	p := New(rule, tr)
	if info := p.Info(); info.Key != "anx_demo_html" || info.Kind != Kind {
		t.Fatalf("Info = %+v", info)
	}

	page, err := p.Search(context.Background(), "frieren", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Frieren" || page.Items[0].Cover != "https://anime.example.com/img/1.jpg" || page.Next != "2" {
		t.Fatalf("Search page = %+v", page)
	}

	details, err := p.Details(context.Background(), page.Items[0].ID)
	if err != nil {
		t.Fatalf("Details: %v", err)
	}
	if details.Title != "Frieren" || details.Description != "Beyond Journey's End" || len(details.Lines) != 1 || len(details.Lines[0].Episodes) != 2 {
		t.Fatalf("Details = %+v", details)
	}

	play, err := p.Play(context.Background(), details.ID, details.Lines[0].ID, details.Lines[0].Episodes[0].ID)
	if err != nil {
		t.Fatalf("Play: %v", err)
	}
	if play.URL != "https://stream.example.com/720.m3u8" || play.Format != video.FormatHLS || play.HLS == nil || !play.HLS.FilterAds {
		t.Fatalf("Play = %+v", play)
	}

	// Verify RewriteHLS removes cue-out ads cleanly via video/hls.
	adPlaylist := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXTINF:5.0,\nseg0.ts\n#EXTINF:5.0,\nseg1.ts\n#EXT-X-CUE-OUT:2.0\n#EXTINF:2.0,\nad.ts\n#EXT-X-CUE-IN\n#EXTINF:5.0,\nseg2.ts\n#EXT-X-ENDLIST\n")
	rw, err := p.RewriteHLS(adPlaylist, "https://stream.example.com/720.m3u8", nil)
	if err != nil {
		t.Fatalf("RewriteHLS: %v", err)
	}
	if len(rw.DroppedGroups) != 1 || strings.Contains(string(rw.Body), "ad.ts") {
		t.Fatalf("expected ad removal, got dropped=%d body=%s", len(rw.DroppedGroups), rw.Body)
	}
}

func TestJSONPipelinePreservesBigIDsAndHMACSigning(t *testing.T) {
	const secret = "s3cr3t"
	wantSigMac := hmac.New(sha256.New, []byte(secret))
	wantSigMac.Write([]byte("9007199254740993"))
	wantSig := hex.EncodeToString(wantSigMac.Sum(nil))

	ruleJSON := `{
		"$schema": "anx-rule/2",
		"name": "API Source",
		"version": "2.0.0",
		"baseUrl": "https://api.example.com",
		"search": {
			"steps": [
				{"op": "fetch", "method": "POST", "url": "/v1/search", "bodyType": "json", "body": "{\"q\":\"{{keyword}}\"}"},
				{"op": "json", "path": "$.data.list[*]", "all": true, "jsonFields": {
					"id": "$.id",
					"title": "$.name"
				}, "to": "items"}
			]
		},
		"detail": {
			"steps": [
				{"op": "fetch", "url": "/v1/detail/{{id}}"},
				{"op": "json", "path": "$.data.title", "to": "title", "keepPipe": true},
				{"op": "json", "path": "$.data.episodes[*]", "all": true, "jsonFields": {
					"id": "$.epId",
					"title": "$.label"
				}, "to": "lines"}
			]
		},
		"play": {
			"steps": [
				{"op": "const", "value": "{{episodeId}}"},
				{"op": "crypto", "algorithm": "hmac-sha256", "key": "` + secret + `", "to": "sig"},
				{"op": "fetch", "url": "/v1/play/{{episodeId}}", "headers": {"X-Sig": "{{sig}}"}},
				{"op": "json", "path": "$.stream", "to": "videoUrl"}
			]
		}
	}`

	rule, err := Parse([]byte(ruleJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr := &fakeTransport{
		handler: func(_ context.Context, req *host.Request) (*host.Response, error) {
			switch req.URL {
			case "https://api.example.com/v1/search":
				return &host.Response{Status: 200, Body: []byte(`{"data":{"list":[{"id":9007199254740993,"name":"BigID Show"}]}}`)}, nil
			case "https://api.example.com/v1/detail/9007199254740993":
				return &host.Response{Status: 200, Body: []byte(`{"data":{"title":"BigID Show","episodes":[{"epId":9007199254740993,"label":"E1"}]}}`)}, nil
			case "https://api.example.com/v1/play/9007199254740993":
				if got := req.Header.Get("X-Sig"); got != wantSig {
					t.Errorf("X-Sig = %q, want %q", got, wantSig)
				}
				return &host.Response{Status: 200, Body: []byte(`{"stream":"https://cdn.example.com/ep1.mp4"}`)}, nil
			default:
				return &host.Response{Status: 404}, nil
			}
		},
	}

	p := New(rule, tr)
	page, err := p.Search(context.Background(), "big", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "9007199254740993" {
		t.Fatalf("Big int ID lost precision: %+v", page.Items)
	}
	det, err := p.Details(context.Background(), page.Items[0].ID)
	if err != nil {
		t.Fatalf("Details: %v", err)
	}
	if det.Lines[0].Episodes[0].ID != "9007199254740993" {
		t.Fatalf("Episode BigID lost precision: %+v", det.Lines)
	}
	play, err := p.Play(context.Background(), det.ID, det.Lines[0].ID, det.Lines[0].Episodes[0].ID)
	if err != nil {
		t.Fatalf("Play: %v", err)
	}
	if play.URL != "https://cdn.example.com/ep1.mp4" || play.Format != video.FormatMP4 {
		t.Fatalf("Play = %+v", play)
	}
}

func TestSniffFallbackAndInlineSniffer(t *testing.T) {
	ruleJSON := `{
		"$schema": "anx-rule/2",
		"key": "anx_sniff",
		"name": "Sniff Site",
		"version": "1.0.0",
		"baseUrl": "https://sniff.example.com",
		"hls": {"filterAds": true},
		"search": {"steps": [{"op": "const", "value": []}]},
		"detail": {"steps": [{"op": "const", "value": []}]},
		"play": {
			"steps": [
				{"op": "fetch", "url": "{{episodeId}}"},
				{"op": "regex", "pattern": "video=\"([^\"]+)\"", "group": 1},
				{"op": "sniff", "whenEmpty": true, "page": "{{episodeId}}", "match": "\\.m3u8", "followIframes": true, "timeoutMs": 15000, "inline": true}
			]
		}
	}`
	rule, err := Parse([]byte(ruleJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr := &fakeTransport{
		handler: func(_ context.Context, req *host.Request) (*host.Response, error) {
			return &host.Response{Status: 200, Body: []byte(`<html><body>obfuscated player</body></html>`), FinalURL: req.URL}, nil
		},
	}

	// Without host.Sniffer, returns Play.Sniff signal.
	pSignal := New(rule, tr)
	play, err := pSignal.Play(context.Background(), "1", "l0", "https://sniff.example.com/watch/1")
	if err != nil {
		t.Fatalf("Play signal: %v", err)
	}
	if play.URL != "" || play.Sniff == nil || play.Sniff.Page != "https://sniff.example.com/watch/1" || !play.Sniff.FollowIframes || play.HLS == nil || !play.HLS.FilterAds {
		t.Fatalf("unexpected sniff play: %+v", play)
	}

	// With host.Sniffer attached, inline sniff resolves directly via host.Sniffer.
	sniffer := &fakeSniffer{
		res: &host.SniffResult{
			URL:    "https://cdn.example.com/sniffed.m3u8",
			Header: http.Header{"Origin": {"https://sniff.example.com"}},
		},
	}
	pInline := NewWithSniffer(rule, tr, sniffer)
	play2, err := pInline.Play(context.Background(), "1", "l0", "https://sniff.example.com/watch/1")
	if err != nil {
		t.Fatalf("Play inline sniff: %v", err)
	}
	if play2.URL != "https://cdn.example.com/sniffed.m3u8" || play2.Sniff != nil || play2.Header.Get("Origin") != "https://sniff.example.com" {
		t.Fatalf("unexpected inline sniff play: %+v", play2)
	}
}

func TestMalformedRulesRejected(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"wrong schema", `{"$schema":"anx-rule/1","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"const"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"bad baseUrl scheme", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"file:///etc/passwd","search":{"steps":[{"op":"const"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"bad key", `{"$schema":"anx-rule/2","key":"bad-key!","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"const"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"unknown op", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"evaljs"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"invalid regex", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"regex","pattern":"[unclosed"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"invalid css", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"select","mode":"css","selector":"div[unclosed"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"invalid xpath", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"select","mode":"xpath","selector":"///[bad"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"invalid crypto", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"https://a.test","search":{"steps":[{"op":"crypto","algorithm":"rot13"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
		{"header injection", `{"$schema":"anx-rule/2","name":"X","version":"1","baseUrl":"https://a.test","headers":{"X-Bad":"a\r\nb"},"search":{"steps":[{"op":"const"}]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`},
	}
	for _, tc := range cases {
		if _, err := Parse([]byte(tc.raw)); err == nil {
			t.Errorf("case %q: expected Parse error, got nil", tc.name)
		}
	}
}

func TestStepLimitLoopLimitAndTimeout(t *testing.T) {
	// Step budget exhaustion inside foreach.
	stepLimitRule := `{
		"$schema": "anx-rule/2",
		"key": "anx_limit",
		"name": "Limit",
		"version": "1",
		"baseUrl": "https://a.test",
		"maxSteps": 10,
		"search": {
			"steps": [
				{"op": "const", "value": "1,2,3,4,5,6,7,8,9,10,11,12"},
				{"op": "split", "separator": ","},
				{"op": "foreach", "steps": [
					{"op": "trim"},
					{"op": "upper"}
				]}
			]
		},
		"detail": {"steps": [{"op": "const", "value": []}]},
		"play": {"steps": [{"op": "const", "value": "https://a.test/v.mp4"}]}
	}`
	r1, err := Parse([]byte(stepLimitRule))
	if err != nil {
		t.Fatalf("Parse limit rule: %v", err)
	}
	p1 := New(r1, &fakeTransport{})
	if _, err := p1.Search(context.Background(), "q", ""); !errors.Is(err, ErrStepLimitExceeded) {
		t.Fatalf("expected ErrStepLimitExceeded, got %v", err)
	}

	// Loop item limit protection.
	loopLimitRule := `{
		"$schema": "anx-rule/2",
		"key": "anx_loop",
		"name": "Loop",
		"version": "1",
		"baseUrl": "https://a.test",
		"maxLoop": 3,
		"search": {
			"steps": [
				{"op": "const", "value": "a,b,c,d"},
				{"op": "split", "separator": ","},
				{"op": "foreach", "steps": [{"op": "trim"}]}
			]
		},
		"detail": {"steps": [{"op": "const", "value": []}]},
		"play": {"steps": [{"op": "const", "value": "https://a.test/v.mp4"}]}
	}`
	r2, err := Parse([]byte(loopLimitRule))
	if err != nil {
		t.Fatalf("Parse loop rule: %v", err)
	}
	if _, err := New(r2, &fakeTransport{}).Search(context.Background(), "q", ""); !errors.Is(err, ErrLoopLimitExceeded) {
		t.Fatalf("expected ErrLoopLimitExceeded, got %v", err)
	}

	// Stage timeout and context cancellation.
	timeoutRule := `{
		"$schema": "anx-rule/2",
		"key": "anx_timeout",
		"name": "Timeout",
		"version": "1",
		"baseUrl": "https://a.test",
		"search": {
			"timeoutMs": 40,
			"steps": [{"op": "fetch", "url": "/slow"}]
		},
		"detail": {"steps": [{"op": "const", "value": []}]},
		"play": {"steps": [{"op": "const", "value": "https://a.test/v.mp4"}]}
	}`
	r3, err := Parse([]byte(timeoutRule))
	if err != nil {
		t.Fatalf("Parse timeout rule: %v", err)
	}
	slowTr := &fakeTransport{
		handler: func(ctx context.Context, _ *host.Request) (*host.Response, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				return &host.Response{Status: 200}, nil
			}
		},
	}
	if _, err := New(r3, slowTr).Search(context.Background(), "q", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(r3, slowTr).Search(canceledCtx, "q", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
