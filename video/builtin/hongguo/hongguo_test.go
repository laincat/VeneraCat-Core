package hongguo

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
)

const testBase = "https://hg.test"

// fakeSite serves canned responses by exact URL and records every request.
type fakeSite struct {
	mu    sync.Mutex
	pages map[string]string
	log   []*host.Request
	onDo  func(url string)
}

func newSite() *fakeSite {
	return &fakeSite{pages: map[string]string{}}
}

func (s *fakeSite) Do(ctx context.Context, r *host.Request) (*host.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.log = append(s.log, r)
	cb := s.onDo
	body, ok := s.pages[r.URL]
	s.mu.Unlock()
	if cb != nil {
		cb(r.URL)
	}
	if !ok {
		return &host.Response{Status: 404, Body: []byte("not found")}, nil
	}
	return &host.Response{Status: 200, Body: []byte(body), FinalURL: r.URL}, nil
}

func (s *fakeSite) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.log)
}

type transportFunc func(context.Context, *host.Request) (*host.Response, error)

func (f transportFunc) Do(ctx context.Context, r *host.Request) (*host.Response, error) {
	return f(ctx, r)
}

func html(router string) string {
	return `<!doctype html><html><head><script>window._ROUTER_DATA = ` + router + `;</script></head><body></body></html>`
}

func catURL(route string, page int) string {
	return fmt.Sprintf("%s/category/%s?page=%d", testBase, route, page)
}

// entry builds a recommendList entry with the video_data nesting.
func entry(id int, title string) string {
	return fmt.Sprintf(`{"video_data":{"series_id_str":"%d","series_id":%d,"series_title":%q,"series_cover":"https://img.test/%d.jpg","episode_cnt":30,"series_status":1,"tags":["都市"]}}`, id, id, title, id)
}

func categoryHTML(entries []string, totalPages int) string {
	return html(fmt.Sprintf(`{"loaderData":{"category_page":{"isSuccess":true,"recommendList":[%s],"pagination":{"totalPages":%d}}}}`, strings.Join(entries, ","), totalPages))
}

func entries(from, n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = entry(from+i, fmt.Sprintf("%s%d", prefix, from+i))
	}
	return out
}

func newProvider(s *fakeSite) *Provider {
	return &Provider{BaseURL: testBase + "/", Transport: s, RetryDelay: time.Millisecond}
}

func TestInfoAndCategories(t *testing.T) {
	p := New(nil)
	info := p.Info()
	if info.Key != "builtin_hongguo" || info.Name != "红果短剧" || info.Kind != "builtin" || info.Version != "0.1.0" {
		t.Fatalf("info = %+v", info)
	}
	if p.BaseURL != "https://hongguoduanju.com" || p.Partition() != "video:builtin_hongguo" {
		t.Fatalf("base %q partition %q", p.BaseURL, p.Partition())
	}
	cats := p.Categories()
	if len(cats) != 4 || cats[0].ID != "real-drama" || cats[1].Name != "漫剧" || cats[2].Name != "AI剧" || cats[3].ID != "comic" {
		t.Fatalf("categories = %+v", cats)
	}
	if (&Provider{}).Info().BaseURL != DefaultBaseURL {
		t.Fatal("empty BaseURL must default")
	}
}

func TestExploreParsesVideoDataNesting(t *testing.T) {
	s := newSite()
	flat := `{"series_id":"555","series_name":"平铺剧","cover":"https://img.test/f.jpg","episode_right_text":"更新至5集"}`
	bad := `{"video_data":{"series_title":"没有ID"}}`
	s.pages[catURL("real-drama", 1)] = categoryHTML(append(entries(100, 2, "剧"), flat, bad), 3)
	p := newProvider(s)

	page, err := p.Explore(context.Background(), "real-drama", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("items = %+v", page.Items)
	}
	first := page.Items[0]
	if first.ID != "100" || first.Title != "剧100" || first.Cover != "https://img.test/100.jpg" || first.Subtitle != "共30集" {
		t.Fatalf("first = %+v", first)
	}
	if first.Extra["episodes"] != "30" || first.Extra["status"] != "finished" || len(first.Tags) != 1 || first.Tags[0] != "都市" {
		t.Fatalf("first extra/tags = %+v", first)
	}
	if f := page.Items[2]; f.ID != "555" || f.Title != "平铺剧" || f.Subtitle != "更新至5集" {
		t.Fatalf("flat = %+v", f)
	}
	if page.Next != "" {
		t.Fatalf("short page must be last, Next = %q", page.Next)
	}
	req := s.log[0]
	if req.Method != "GET" || req.Partition != "video:builtin_hongguo" || req.Header.Get("Referer") != testBase+"/" {
		t.Fatalf("request = %+v", req)
	}
}

func TestExploreNextCursor(t *testing.T) {
	s := newSite()
	s.pages[catURL("comic", 1)] = categoryHTML(entries(1, 24, "a"), 2)
	s.pages[catURL("comic", 2)] = categoryHTML(entries(25, 24, "b"), 2)
	p := newProvider(s)
	page, err := p.Explore(context.Background(), "comic", "")
	if err != nil || page.Next != "2" || len(page.Items) != 24 {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	page, err = p.Explore(context.Background(), "comic", "2")
	if err != nil || page.Next != "" {
		t.Fatalf("page 2 is the last by pagination.totalPages: %+v %v", page, err)
	}
	for _, c := range []string{"0", "-1", "x", "501"} {
		if _, err := p.Explore(context.Background(), "comic", c); err == nil {
			t.Errorf("cursor %q accepted", c)
		}
	}
	if _, err := p.Explore(context.Background(), "nope", ""); !errors.Is(err, video.ErrNotFound) {
		t.Fatalf("unknown category: %v", err)
	}
}

func TestExploreUnavailable(t *testing.T) {
	s := newSite()
	s.pages[catURL("comic", 1)] = html(`{"loaderData":{"category_page":{"isSuccess":false,"recommendList":[]}}}`)
	if _, err := newProvider(s).Explore(context.Background(), "comic", ""); err == nil {
		t.Fatal("isSuccess=false must fail")
	}
	s.pages[catURL("comic", 1)] = "<html>no data</html>"
	if _, err := newProvider(s).Explore(context.Background(), "comic", ""); err == nil {
		t.Fatal("missing router data must fail")
	}
}

func TestExploreLoaderPrefixKey(t *testing.T) {
	s := newSite()
	s.pages[catURL("comic", 1)] = html(`{"loaderData":{"category_$route/page":{"isSuccess":true,"recommendList":[` + entry(7, "前缀") + `]}}}`)
	page, err := newProvider(s).Explore(context.Background(), "comic", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "7" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestSearchFiltersDedupesAndStops(t *testing.T) {
	s := newSite()
	// real-drama: page 2 repeats page 1 (adds nothing), so page 3 is never read.
	full := entries(1000, 24, "霸总")
	s.pages[catURL("real-drama", 1)] = categoryHTML(full, 0)
	s.pages[catURL("real-drama", 2)] = categoryHTML(full, 0)
	s.pages[catURL("real-drama", 3)] = categoryHTML(entries(2000, 24, "霸总"), 0)
	// comic-drama: page 1 is short, so page 2 is never read. It repeats id 1001.
	s.pages[catURL("comic-drama", 1)] = categoryHTML([]string{entry(1001, "霸总1001"), entry(3000, "别的")}, 0)
	s.pages[catURL("ai-drama", 1)] = categoryHTML(nil, 0)
	s.pages[catURL("comic", 1)] = categoryHTML([]string{entry(4000, "我的霸总")}, 0)
	p := newProvider(s)

	page, err := p.Search(context.Background(), " 霸总 ", "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	for _, it := range page.Items {
		ids[it.ID]++
	}
	if len(page.Items) != 25 || ids["1001"] != 1 || ids["4000"] != 1 || ids["3000"] != 0 {
		t.Fatalf("items = %d ids=%v", len(page.Items), ids)
	}
	if page.Next != "" {
		t.Fatalf("Next = %q", page.Next)
	}
	for _, r := range s.log {
		if strings.Contains(r.URL, "page=3") || strings.Contains(r.URL, "comic-drama?page=2") {
			t.Errorf("unexpected request %s", r.URL)
		}
	}
	if got := s.requests(); got != 5 {
		t.Fatalf("requests = %d, want 5", got)
	}
	if page, err := p.Search(context.Background(), "霸总", "2"); err != nil || len(page.Items) != 0 {
		t.Fatalf("cursor page: %+v %v", page, err)
	}
	if page, err := p.Search(context.Background(), "  ", ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("empty keyword: %+v %v", page, err)
	}
}

func TestSearchBoundedToFivePages(t *testing.T) {
	s := newSite()
	for ci, c := range categories {
		for pg := 1; pg <= 8; pg++ {
			s.pages[catURL(c.ID, pg)] = categoryHTML(entries(100000*(ci+1)+pg*100, 24, "x"), 0)
		}
	}
	if _, err := newProvider(s).Search(context.Background(), "x", ""); err != nil {
		t.Fatal(err)
	}
	if got := s.requests(); got != 20 {
		t.Fatalf("requests = %d, want 20 (5 pages x 4 categories)", got)
	}
}

func TestSearchCancelStopsPaging(t *testing.T) {
	s := newSite()
	for pg := 1; pg <= 8; pg++ {
		s.pages[catURL("real-drama", pg)] = categoryHTML(entries(pg*100, 24, "x"), 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.onDo = func(u string) {
		if strings.HasSuffix(u, "real-drama?page=2") {
			cancel()
		}
	}
	_, err := newProvider(s).Search(ctx, "x", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := s.requests(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestSearchAllCategoriesFailing(t *testing.T) {
	if _, err := newProvider(newSite()).Search(context.Background(), "x", ""); err == nil {
		t.Fatal("expected error when every category fails")
	}
}

const detailJSON = `{"loaderData":{"detail_page":{"seriesDetail":{"series_name":"大剧","cover":"https://img.test/c.jpg","intro":"简介","vid_list":["7400000000000000011",7400000000000000012,"abc","7400000000000000013"],"tags":["甜宠","逆袭"]}}}}`

func TestDetails(t *testing.T) {
	s := newSite()
	s.pages[testBase+"/detail?series_id=7400000000000000001"] = html(detailJSON)
	p := newProvider(s)
	d, err := p.Details(context.Background(), "7400000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "7400000000000000001" || d.Title != "大剧" || d.Cover != "https://img.test/c.jpg" || d.Description != "简介" {
		t.Fatalf("details = %+v", d)
	}
	if len(d.Tags) != 2 || len(d.Lines) != 1 || d.Lines[0].ID != "default" || d.Lines[0].Name != "默认" {
		t.Fatalf("lines/tags = %+v", d)
	}
	eps := d.Lines[0].Episodes
	if len(eps) != 3 || eps[0] != (video.Episode{ID: "7400000000000000011", Title: "第1集"}) ||
		eps[1] != (video.Episode{ID: "7400000000000000012", Title: "第2集"}) ||
		eps[2] != (video.Episode{ID: "7400000000000000013", Title: "第4集"}) {
		t.Fatalf("episodes = %+v", eps)
	}
}

func TestDetailsEmpty(t *testing.T) {
	s := newSite()
	s.pages[testBase+"/detail?series_id=1"] = html(`{"loaderData":{"detail_page":{}}}`)
	if _, err := newProvider(s).Details(context.Background(), "1"); !errors.Is(err, video.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	s.pages[testBase+"/detail?series_id=2"] = html(`{"loaderData":{"detail_x":{"seriesDetail":{"series_name":"n","vid_list":[]}}}}`)
	if _, err := newProvider(s).Details(context.Background(), "2"); err == nil {
		t.Fatal("no episodes must fail")
	}
}

func playHTML(vid, series, info string) string {
	return html(fmt.Sprintf(`{"loaderData":{"player_page":{"vid":%s,"series_id":%s,"video_player_info":%s}}}`, vid, series, info))
}

func TestPlayBase64AndBackupDeduped(t *testing.T) {
	s := newSite()
	main := "https://cdn.test/a/video.mp4?sig=1"
	b64 := base64.StdEncoding.EncodeToString([]byte(main))
	raw := base64.RawStdEncoding.EncodeToString([]byte("https://cdn2.test/b/index.m3u8"))
	info := fmt.Sprintf(`{"main_url":%q,"backup_url":%q,"backup_url_1":"https://cdn3.test/c.mp4","backup_urls":[%q,"  https://cdn3.test/c.mp4 ","ftp://x/y","not base64!"],"url_list":["javascript:alert(1)"]}`, b64, main, raw)
	s.pages[testBase+"/player/7400000000000000001/7400000000000000011"] = playHTML("7400000000000000011", `"7400000000000000001"`, info)
	p := newProvider(s)

	play, err := p.Play(context.Background(), "7400000000000000001", "default", "7400000000000000011")
	if err != nil {
		t.Fatal(err)
	}
	if err := play.Validate(); err != nil {
		t.Fatal(err)
	}
	if play.URL != main || play.Format != video.FormatMP4 {
		t.Fatalf("play = %+v", play)
	}
	if play.Header.Get("Referer") != testBase+"/" {
		t.Fatalf("headers = %v", play.Header)
	}
	want := []string{main, "https://cdn3.test/c.mp4", "https://cdn2.test/b/index.m3u8"}
	if len(play.Variants) != len(want) {
		t.Fatalf("variants = %+v", play.Variants)
	}
	for i, v := range play.Variants {
		if v.URL != want[i] || v.Name == "" {
			t.Fatalf("variant %d = %+v", i, v)
		}
	}
	if play.Encrypted {
		t.Fatal("must not be marked encrypted")
	}
}

func TestPlayHLSFormat(t *testing.T) {
	s := newSite()
	s.pages[testBase+"/player/5/6"] = html(`{"loaderData":{"player_$id":{"vid":6,"series_id":"5","video_player_info":{"main_url":"https://c.test/x.m3u8"}}}}`)
	play, err := newProvider(s).Play(context.Background(), "5", "", "6")
	if err != nil || play.Format != video.FormatHLS {
		t.Fatalf("%+v %v", play, err)
	}
}

func TestPlayMismatchedEpisode(t *testing.T) {
	s := newSite()
	info := `{"main_url":"https://cdn.test/trial.mp4"}`
	s.pages[testBase+"/player/5/6"] = playHTML("7", `"5"`, info)
	s.pages[testBase+"/player/5/8"] = playHTML("8", `"9"`, info)
	s.pages[testBase+"/player/5/9"] = html(`{"loaderData":{}}`)
	p := newProvider(s)
	for _, ep := range []string{"6", "8", "9"} {
		play, err := p.Play(context.Background(), "5", "default", ep)
		if err == nil || play != nil {
			t.Fatalf("episode %s: play=%+v err=%v", ep, play, err)
		}
		if !strings.Contains(err.Error(), "trial") {
			t.Errorf("episode %s: error should explain trial-only access: %v", ep, err)
		}
	}
}

func TestPlayNoAddress(t *testing.T) {
	s := newSite()
	s.pages[testBase+"/player/5/6"] = playHTML("6", `"5"`, `{"main_url":"","backup_url":"%%%","video_model":"opaque"}`)
	play, err := newProvider(s).Play(context.Background(), "5", "default", "6")
	if err == nil || play != nil {
		t.Fatalf("play=%+v err=%v", play, err)
	}
	if !strings.Contains(err.Error(), "official App") {
		t.Fatalf("error = %v", err)
	}
}

func TestInvalidIDsRejectedBeforeRequest(t *testing.T) {
	s := newSite()
	p := newProvider(s)
	ctx := context.Background()
	bad := []string{"", "abc", "1/2", "1?x=2", "-1", " 1", "1\n", strings.Repeat("9", 33), "１２"}
	for _, id := range bad {
		if _, err := p.Details(ctx, id); err == nil {
			t.Errorf("Details(%q) accepted", id)
		}
		if _, err := p.Play(ctx, id, "default", "1"); err == nil {
			t.Errorf("Play series %q accepted", id)
		}
		if _, err := p.Play(ctx, "1", "default", id); err == nil {
			t.Errorf("Play episode %q accepted", id)
		}
	}
	if _, err := p.Play(ctx, "1", "other", "2"); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("unknown line: %v", err)
	}
	if n := s.requests(); n != 0 {
		t.Fatalf("%d requests were sent for invalid input", n)
	}
	// 32 digits is valid, so it reaches the transport (which answers 404).
	if _, err := p.Details(ctx, strings.Repeat("9", 32)); err == nil || s.requests() != 1 {
		t.Fatalf("32-digit id should be sent: err=%v requests=%d", err, s.requests())
	}
}

func TestLargeSeriesIDRoundTrips(t *testing.T) {
	const id = "7400000000000000001"
	s := newSite()
	// The catalogue gives the ID as a bare JSON number and as a string.
	s.pages[catURL("real-drama", 1)] = html(`{"loaderData":{"category_page":{"isSuccess":true,"recommendList":[{"series_id":7400000000000000001,"series_name":"数字ID"},{"video_data":{"series_id_str":"7400000000000000003","series_title":"字符串ID"}}]}}}`)
	s.pages[testBase+"/detail?series_id="+id] = html(`{"loaderData":{"detail_page":{"seriesDetail":{"series_name":"n","vid_list":[7400000000000000002]}}}}`)
	s.pages[testBase+"/player/"+id+"/7400000000000000002"] = html(`{"loaderData":{"player_page":{"vid":7400000000000000002,"series_id":7400000000000000001,"video_player_info":{"main_url":"https://c.test/x.mp4"}}}}`)
	p := newProvider(s)
	ctx := context.Background()

	page, err := p.Explore(ctx, "real-drama", "")
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != id || page.Items[1].ID != "7400000000000000003" {
		t.Fatalf("explore: %+v %v", page, err)
	}
	d, err := p.Details(ctx, page.Items[0].ID)
	if err != nil || d.ID != id || d.Lines[0].Episodes[0].ID != "7400000000000000002" {
		t.Fatalf("details: %+v %v", d, err)
	}
	play, err := p.Play(ctx, d.ID, d.Lines[0].ID, d.Lines[0].Episodes[0].ID)
	if err != nil || play.URL != "https://c.test/x.mp4" {
		t.Fatalf("play: %+v %v", play, err)
	}
}

func TestRetryAndErrors(t *testing.T) {
	var calls int
	tr := transportFunc(func(_ context.Context, r *host.Request) (*host.Response, error) {
		calls++
		switch {
		case strings.Contains(r.URL, "series_id=1"):
			if calls < 3 {
				return &host.Response{Status: 503}, nil
			}
			return &host.Response{Status: 200, Body: []byte(html(detailJSON))}, nil
		case strings.Contains(r.URL, "series_id=2"):
			return &host.Response{Status: 404}, nil
		default:
			return nil, errors.New("boom")
		}
	})
	p := &Provider{BaseURL: testBase, Transport: tr, RetryDelay: time.Millisecond}
	if _, err := p.Details(context.Background(), "1"); err != nil || calls != 3 {
		t.Fatalf("retry: err=%v calls=%d", err, calls)
	}
	calls = 0
	if _, err := p.Details(context.Background(), "2"); err == nil || calls != 1 {
		t.Fatalf("404 must not retry: err=%v calls=%d", err, calls)
	}
	calls = 0
	if _, err := p.Details(context.Background(), "3"); err == nil || calls != 3 {
		t.Fatalf("transport error: err=%v calls=%d", err, calls)
	}
	if _, err := (&Provider{}).Details(context.Background(), "1"); !errors.Is(err, host.ErrUnsupported) {
		t.Fatalf("nil transport: %v", err)
	}
}

func TestChallengeIsReported(t *testing.T) {
	tr := transportFunc(func(_ context.Context, _ *host.Request) (*host.Response, error) {
		return &host.Response{Status: 403, Header: http.Header{"Cf-Mitigated": {"challenge"}}}, nil
	})
	p := &Provider{BaseURL: testBase, Transport: tr, RetryDelay: time.Millisecond}
	_, err := p.Details(context.Background(), "1")
	if err == nil || !strings.Contains(err.Error(), "challenge") {
		t.Fatalf("err = %v", err)
	}
}
