// Package hongguo is the built-in provider for 红果短剧
// (https://hongguoduanju.com), written against the public web pages only.
//
// Every page embeds its data as window._ROUTER_DATA = {...}. The provider
// reads the category, detail and player pages and nothing else. It does not
// use the official App's signed APIs or its media-key scheme: an episode
// whose web player page carries no public address is reported as needing the
// official App, and the trial episode the site serves instead is never
// returned in its place.
package hongguo

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/laincat/veneracat-core/attribution"
	"github.com/laincat/veneracat-core/challenge"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
)

const (
	// Key is the provider key.
	Key = "builtin_hongguo"
	// DefaultBaseURL is the site root used when Provider.BaseURL is empty.
	DefaultBaseURL = "https://hongguoduanju.com"
	// LineID and LineName identify the only line the site offers.
	LineID   = "default"
	LineName = "默认"

	// PageSize is the number of entries a full category page holds. A
	// shorter page is the last one.
	PageSize = 24
	// SearchPages bounds how many pages of each category Search reads.
	SearchPages = 5

	maxBody       = 16 << 20
	maxAddressLen = 8192
	maxPage       = 500
	maxAttempts   = 3
)

// Attribution credits the behaviour this provider was rewritten from: the
// web provider of 真果鉴 by Xanerix (GPL-3.0). The App-only signing, device
// registration and DRM key code of that project is not included.
var Attribution = attribution.Info{
	Authors:  []attribution.Author{{Name: "Xanerix", URL: "https://github.com/Xanerix"}, {Name: "VeneraCat Core"}},
	Homepage: "https://github.com/Xanerix/guoapp",
}

var numericID = regexp.MustCompile(`^[0-9]{1,32}$`)

var categories = []video.Category{
	{ID: "real-drama", Name: "真人剧"},
	{ID: "comic-drama", Name: "漫剧"},
	{ID: "ai-drama", Name: "AI剧"},
	{ID: "comic", Name: "动漫"},
}

// Provider reads the Hongguo web site through the client's transport. It
// implements video.Provider and video.Explorer. Use New for sensible defaults.
type Provider struct {
	// BaseURL is the site root; empty means DefaultBaseURL.
	BaseURL   string
	Transport host.Transport
	// RetryDelay is the base back-off between attempts after a transport
	// error or a 5xx/408/429 status; zero means one second.
	RetryDelay time.Duration
}

var (
	_ video.Provider = (*Provider)(nil)
	_ video.Explorer = (*Provider)(nil)
)

// New returns a provider that uses t for all HTTP.
func New(t host.Transport) *Provider {
	return &Provider{BaseURL: DefaultBaseURL, Transport: t}
}

// Info implements video.Provider.
func (p *Provider) Info() video.Info {
	return video.Info{Key: Key, Name: "红果短剧", Kind: "builtin", Version: "0.1.0", BaseURL: p.base(),
		Attribution: Attribution}
}

// Partition isolates the provider's cookies and clearance.
func (p *Provider) Partition() string { return "video:" + Key }

func (p *Provider) base() string {
	if b := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"); b != "" {
		return b
	}
	return DefaultBaseURL
}

// Categories implements video.Explorer.
func (p *Provider) Categories() []video.Category {
	return append([]video.Category(nil), categories...)
}

func categoryKnown(id string) bool {
	for _, c := range categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Explore implements video.Explorer. cursor is a 1-based page number; empty
// means the first page. Next is set while the page was full.
func (p *Provider) Explore(ctx context.Context, categoryID, cursor string) (*video.Page, error) {
	if !categoryKnown(categoryID) {
		return nil, fmt.Errorf("%w: category %q", video.ErrNotFound, categoryID)
	}
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 || n > maxPage {
			return nil, fmt.Errorf("hongguo: invalid cursor %q", cursor)
		}
		page = n
	}
	res, err := p.categoryPage(ctx, categoryID, page)
	if err != nil {
		return nil, err
	}
	out := &video.Page{Items: res.items}
	if res.more(page) {
		out.Next = strconv.Itoa(page + 1)
	}
	return out, nil
}

// Search implements video.Provider.
//
// The web site has no public search; the official App's search API is signed
// and is deliberately not used. Search instead reads up to SearchPages pages
// of every category, stops a category early when a page adds nothing new or
// is not full, and keeps the items whose title contains keyword (case
// insensitive). It therefore finds only titles near the front of the
// catalogue and costs up to SearchPages requests per category. The result
// has no Next cursor; a non-empty cursor yields an empty page. ctx
// cancellation stops it between requests.
func (p *Provider) Search(ctx context.Context, keyword, cursor string) (*video.Page, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || cursor != "" {
		return &video.Page{}, nil
	}
	needle := strings.ToLower(keyword)
	out := &video.Page{Items: []video.Item{}}
	seen := map[string]bool{}
	var failures []error
	for _, c := range categories {
		for page := 1; page <= SearchPages; page++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			res, err := p.categoryPage(ctx, c.ID, page)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				failures = append(failures, fmt.Errorf("%s: %w", c.Name, err))
				break
			}
			added := 0
			for _, it := range res.items {
				if seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				added++
				if strings.Contains(strings.ToLower(it.Title), needle) {
					out.Items = append(out.Items, it)
				}
			}
			if added == 0 || !res.more(page) {
				break
			}
		}
	}
	if len(failures) == len(categories) {
		return nil, errors.Join(failures...)
	}
	return out, nil
}

// Details implements video.Provider. id is the series ID.
func (p *Provider) Details(ctx context.Context, id string) (*video.Details, error) {
	if !numericID.MatchString(id) {
		return nil, fmt.Errorf("hongguo: invalid series id %q", id)
	}
	body, err := p.get(ctx, p.base()+"/detail?series_id="+id)
	if err != nil {
		return nil, err
	}
	detail := asMap(loaderPage(parseRouterData(body), "detail_page", "detail_")["seriesDetail"])
	if len(detail) == 0 {
		return nil, fmt.Errorf("%w: series %s has no detail data", video.ErrNotFound, id)
	}
	var episodes []video.Episode
	for i, v := range asList(detail["vid_list"]) {
		vid := str(v)
		if !numericID.MatchString(vid) {
			continue
		}
		episodes = append(episodes, video.Episode{ID: vid, Title: fmt.Sprintf("第%d集", i+1)})
	}
	if len(episodes) == 0 {
		return nil, fmt.Errorf("hongguo: series %s lists no episodes", id)
	}
	d := []map[string]any{detail}
	return &video.Details{
		ID:          id,
		Title:       firstNonEmpty(pick(d, "series_name", "series_title", "name"), id),
		Cover:       pick(d, "cover", "series_cover"),
		Description: pick(d, "intro", "series_intro"),
		Tags:        tagsOf(detail),
		Lines:       []video.Line{{ID: LineID, Name: LineName, Episodes: episodes}},
	}, nil
}

// Play implements video.Provider. itemID is the series ID and episodeID the
// vid. It returns only the address the web player page publishes for exactly
// that episode.
func (p *Provider) Play(ctx context.Context, itemID, lineID, episodeID string) (*video.Play, error) {
	if !numericID.MatchString(itemID) {
		return nil, fmt.Errorf("hongguo: invalid series id %q", itemID)
	}
	if !numericID.MatchString(episodeID) {
		return nil, fmt.Errorf("hongguo: invalid episode id %q", episodeID)
	}
	if lineID != "" && lineID != LineID {
		return nil, fmt.Errorf("%w: line %q", video.ErrNotFound, lineID)
	}
	body, err := p.get(ctx, p.base()+"/player/"+itemID+"/"+episodeID)
	if err != nil {
		return nil, err
	}
	page := loaderPage(parseRouterData(body), "player_page", "player_")
	if str(page["vid"]) != episodeID || str(page["series_id"]) != itemID {
		return nil, fmt.Errorf("hongguo: the site did not return episode %s of series %s; the web site only allows trial episodes", episodeID, itemID)
	}
	addrs := mediaAddresses(asMap(page["video_player_info"]))
	if len(addrs) == 0 {
		return nil, fmt.Errorf("hongguo: episode %s has no public address on the web site; it needs the official App", episodeID)
	}
	play := &video.Play{
		URL:    addrs[0],
		Format: formatOf(addrs[0]),
		Header: http.Header{"Referer": {p.base() + "/"}},
	}
	for i, a := range addrs {
		play.Variants = append(play.Variants, video.Variant{Name: "地址" + strconv.Itoa(i+1), URL: a})
	}
	return play, nil
}

// --- catalogue ----------------------------------------------------------------

type categoryResult struct {
	items      []video.Item
	raw        int
	totalPages int
}

// more reports whether a page after the given one may exist.
func (r *categoryResult) more(page int) bool {
	if r.raw < PageSize || len(r.items) == 0 {
		return false
	}
	if r.totalPages > 0 && page >= r.totalPages {
		return false
	}
	return page < maxPage
}

func (p *Provider) categoryPage(ctx context.Context, route string, page int) (*categoryResult, error) {
	body, err := p.get(ctx, p.base()+"/category/"+route+"?page="+strconv.Itoa(page))
	if err != nil {
		return nil, err
	}
	data := loaderPage(parseRouterData(body), "category_page", "category_")
	if len(data) == 0 || data["isSuccess"] == false {
		return nil, errors.New("hongguo: category data unavailable; the page layout or access rules may have changed")
	}
	list := asList(data["recommendList"])
	res := &categoryResult{raw: len(list)}
	if n, err := strconv.Atoi(str(asMap(data["pagination"])["totalPages"])); err == nil {
		res.totalPages = n
	}
	seen := map[string]bool{}
	for _, entry := range list {
		it, ok := itemFrom(entry)
		if !ok || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		res.items = append(res.items, it)
	}
	return res, nil
}

func itemFrom(entry any) (video.Item, bool) {
	m := asMap(entry)
	if m == nil {
		return video.Item{}, false
	}
	vd := asMap(m["video_data"])
	if len(vd) == 0 {
		vd = m
	}
	both := []map[string]any{vd, m}
	id := pick(both, "series_id_str", "series_id", "keyword")
	if !numericID.MatchString(id) {
		return video.Item{}, false
	}
	count := pick(both, "episode_cnt")
	remark := pick(both, "episode_right_text")
	if remark == "" && count != "" {
		remark = "共" + count + "集"
	}
	it := video.Item{
		ID:       id,
		Title:    firstNonEmpty(pick(both, "series_title", "series_name", "title", "name"), id),
		Cover:    pick(both, "series_cover", "cover"),
		Subtitle: remark,
		Tags:     tagsOf(vd),
	}
	extra := map[string]string{}
	if count != "" {
		extra["episodes"] = count
	}
	switch pick(both, "series_status") {
	case "1":
		extra["status"] = "finished"
	case "0":
		extra["status"] = "ongoing"
	}
	if len(extra) > 0 {
		it.Extra = extra
	}
	return it, true
}

// --- media addresses ------------------------------------------------------------

// mediaAddresses collects the http(s) addresses a player page publishes,
// decoding Base64-wrapped ones, without duplicates and in page order.
func mediaAddresses(info map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	var add func(any)
	add = func(v any) {
		switch x := v.(type) {
		case string:
			a := strings.TrimSpace(x)
			if len(a) > maxAddressLen {
				return
			}
			if !isHTTPURL(a) {
				raw, err := decodeBase64(a)
				if err != nil {
					return
				}
				a = strings.TrimSpace(string(raw))
			}
			if isHTTPURL(a) && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		case []any:
			for _, item := range x {
				add(item)
			}
		}
	}
	for _, k := range []string{"main_url", "backup_url", "backup_url_1", "backup_url_2", "backup_urls", "url_list"} {
		add(info[k])
	}
	return out
}

func decodeBase64(s string) ([]byte, error) {
	if raw, err := base64.StdEncoding.Strict().DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.RawStdEncoding.Strict().DecodeString(s)
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func formatOf(raw string) video.Format {
	u, err := url.Parse(raw)
	if err != nil {
		return video.FormatAuto
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".m3u8":
		return video.FormatHLS
	case ".mp4":
		return video.FormatMP4
	}
	return video.FormatAuto
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- HTTP -----------------------------------------------------------------------

// get fetches u and returns the body of a 2xx response. Transport errors and
// 5xx/408/429 statuses are retried; challenges and other statuses are not.
func (p *Provider) get(ctx context.Context, u string) ([]byte, error) {
	if p.Transport == nil {
		return nil, host.ErrUnsupported
	}
	if !isHTTPURL(u) {
		return nil, fmt.Errorf("hongguo: invalid base URL %q", p.BaseURL)
	}
	delay := p.RetryDelay
	if delay <= 0 {
		delay = time.Second
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			t := time.NewTimer(time.Duration(attempt-1) * delay)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := p.Transport.Do(ctx, &host.Request{
			Method: http.MethodGet,
			URL:    u,
			Header: http.Header{
				"Referer":         {p.base() + "/"},
				"Accept-Language": {"zh-CN,zh;q=0.9"},
			},
			Cookies:   true,
			Partition: p.Partition(),
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if kind, ok := challenge.Detect(resp); ok {
			return nil, &challenge.ErrChallengeRequired{Kind: kind, URL: u}
		}
		switch {
		case resp.Status >= 200 && resp.Status <= 299:
			if len(resp.Body) > maxBody {
				return nil, fmt.Errorf("hongguo: response larger than %d bytes", maxBody)
			}
			return resp.Body, nil
		case resp.Status >= 500 || resp.Status == 408 || resp.Status == 429:
			lastErr = fmt.Errorf("hongguo: HTTP %d from %s", resp.Status, u)
		default:
			return nil, fmt.Errorf("hongguo: HTTP %d from %s", resp.Status, u)
		}
	}
	return nil, lastErr
}
