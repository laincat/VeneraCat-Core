package kazumi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/laincat/veneracat-core/challenge"
	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
)

// MaxBody bounds a page Core will parse.
const MaxBody = 16 << 20

// Provider runs one Kazumi rule. It implements video.Provider.
type Provider struct {
	Rule      *Rule
	Transport host.Transport
}

// New returns a provider for rule using the client's transport. Wrap the
// transport with challenge.NewTransport to get Cloudflare handling.
func New(rule *Rule, t host.Transport) *Provider {
	return &Provider{Rule: rule, Transport: t}
}

// Info implements video.Provider.
func (p *Provider) Info() video.Info {
	return video.Info{Key: p.Rule.Key(), Name: p.Rule.Name, Version: p.Rule.Version, Kind: "kazumi-rule", BaseURL: p.Rule.BaseURL,
		Attribution: p.Rule.Attribution}
}

// Partition isolates this rule's cookies and clearance.
func (p *Provider) Partition() string { return "video:" + p.Info().Key }

// ErrCaptchaRequired means the site served its verification page.
type ErrCaptchaRequired struct{ Rule string }

func (e *ErrCaptchaRequired) Error() string {
	return fmt.Sprintf("kazumi: %s: site verification required", e.Rule)
}

// Search implements video.Provider. Kazumi rules have no paging, so cursor
// must be empty and Next is never set.
func (p *Provider) Search(ctx context.Context, keyword, cursor string) (*video.Page, error) {
	if cursor != "" {
		return &video.Page{}, nil
	}
	r := p.Rule
	var req *host.Request
	var err error
	if r.SearchMode == ModeAPI {
		req, err = p.apiRequest(r.SearchAPI.Request, map[string]any{"keyword": keyword})
	} else {
		req, err = p.xpathSearchRequest(keyword)
	}
	if err != nil {
		return nil, err
	}
	body, err := p.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	var items []video.Item
	if r.SearchMode == ModeAPI {
		items, err = p.parseAPISearch(body)
	} else {
		items, err = p.parseXPathSearch(body)
	}
	if err != nil {
		return nil, err
	}
	return &video.Page{Items: items}, nil
}

// Details implements video.Provider. id is the detail-page URL from Search.
func (p *Provider) Details(ctx context.Context, id string) (*video.Details, error) {
	r := p.Rule
	var req *host.Request
	var err error
	if r.ChapterMode == ModeAPI {
		req, err = p.apiRequest(r.ChapterAPI.Request, map[string]any{"source": id})
	} else {
		u := NormalizeEpisodeURL(r.BaseURL, id)
		if !isHTTPURL(u) {
			return nil, fmt.Errorf("kazumi: %s: invalid detail URL %q", r.Name, u)
		}
		// Kazumi sends stored cookies only for search and API requests.
		req = p.baseRequest("GET", u, false)
	}
	if err != nil {
		return nil, err
	}
	body, err := p.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	var lines []video.Line
	if r.ChapterMode == ModeAPI {
		lines, err = p.parseAPIChapters(body, id)
	} else {
		lines, err = p.parseXPathChapters(body)
	}
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("kazumi: %s: no playable episodes", r.Name)
	}
	return &video.Details{ID: id, Lines: lines}, nil
}

// Play implements video.Provider. Kazumi rules do not describe the media
// URL, so the episode page is handed to the client's WebView sniffer.
func (p *Provider) Play(_ context.Context, _, _, episodeID string) (*video.Play, error) {
	if !isHTTPURL(episodeID) {
		return nil, fmt.Errorf("kazumi: %s: invalid episode URL %q", p.Rule.Name, episodeID)
	}
	play := &video.Play{
		Sniff: &video.Sniff{Page: episodeID, FollowIframes: p.Rule.UseLegacyParser, TimeoutMillis: 30000},
	}
	if ref := p.referer(); ref != "" {
		play.Header = http.Header{"Referer": {ref}}
	}
	if p.Rule.AdBlocker {
		play.HLS = &video.HLSOptions{FilterAds: true}
	}
	return play, nil
}

func (p *Provider) referer() string {
	if p.Rule.Referer != "" {
		return p.Rule.Referer
	}
	if p.Rule.BaseURL != "" {
		return strings.TrimRight(p.Rule.BaseURL, "/") + "/"
	}
	return ""
}

// --- requests ---------------------------------------------------------------

func (p *Provider) baseRequest(method, u string, cookies bool) *host.Request {
	h := http.Header{}
	if ref := strings.TrimRight(p.Rule.BaseURL, "/"); ref != "" {
		h.Set("Referer", ref+"/")
	}
	if p.Rule.UserAgent != "" {
		h.Set("User-Agent", p.Rule.UserAgent)
	}
	return &host.Request{Method: method, URL: u, Header: h, Cookies: cookies, Partition: p.Partition()}
}

func (p *Provider) xpathSearchRequest(keyword string) (*host.Request, error) {
	r := p.Rule
	raw := strings.ReplaceAll(r.SearchURL, "@keyword", url.QueryEscape(keyword))
	u, err := url.Parse(raw)
	if err != nil || !isHTTPURL(raw) {
		return nil, fmt.Errorf("kazumi: %s: invalid search URL %q", r.Name, raw)
	}
	if !r.UsePost {
		return p.baseRequest("GET", raw, true), nil
	}
	form := orderedForm(u.RawQuery)
	u.RawQuery = ""
	req := p.baseRequest("POST", u.String(), true)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Body = []byte(form)
	return req, nil
}

// orderedForm turns a raw query string into a form body without reordering
// the fields: some sites sign or validate fields in the order the rule wrote
// them. A repeated key keeps its first position and its last value.
func orderedForm(rawQuery string) string {
	type pair struct{ k, v string }
	var pairs []pair
	at := map[string]int{}
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		dk, err1 := url.QueryUnescape(k)
		dv, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			dk, dv = k, v
		}
		if i, ok := at[dk]; ok {
			pairs[i].v = dv
			continue
		}
		at[dk] = len(pairs)
		pairs = append(pairs, pair{dk, dv})
	}
	var b strings.Builder
	for i, kv := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(kv.k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(kv.v))
	}
	return b.String()
}

var placeholder = regexp.MustCompile(`(^|[^A-Za-z0-9_])@([A-Za-z_][A-Za-z0-9_]*)`)
var exactPlaceholder = regexp.MustCompile(`^@([A-Za-z_][A-Za-z0-9_]*)$`)

// render replaces @name placeholders. A placeholder directly after a letter,
// digit or underscore is literal text (an e-mail address, or the second
// placeholder of "@a@b"), like Kazumi's (?<![A-Za-z0-9_])@name. With encode,
// values are percent-encoded like Dart's Uri.encodeComponent.
func render(tpl string, vars map[string]any, encode bool) (string, error) {
	var missing string
	out := placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		v, ok := vars[sub[2]]
		if !ok {
			missing = sub[2]
			return m
		}
		s := valueString(v)
		if encode {
			s = encodeComponent(s)
		}
		return sub[1] + s
	})
	if missing != "" {
		return "", fmt.Errorf("missing template variable @%s", missing)
	}
	return out, nil
}

// encodeComponent percent-encodes like Dart's Uri.encodeComponent: every
// byte except A-Z a-z 0-9 - _ . ! ~ * ' ( ) is escaped, so a space is %20 and
// "/", "?", "&", "=", "+" are escaped too. (url.QueryEscape differs on
// ! * ' ( ) and url.PathEscape leaves / ? & = alone.)
func encodeComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', strings.IndexByte("-_.!~*'()", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// valueString converts a template value to text. Unlike stringValue it does
// not trim strings: Kazumi sends a keyword or ID exactly as given.
func valueString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return stringValue(v)
}

// renderValue keeps the JSON type of an exact "@name" value and renders
// strings inside arrays and objects.
func renderValue(v any, vars map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		if m := exactPlaceholder.FindStringSubmatch(x); m != nil {
			val, ok := vars[m[1]]
			if !ok {
				return nil, fmt.Errorf("missing template variable @%s", m[1])
			}
			return val, nil
		}
		return render(x, vars, false)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			r, err := renderValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			r, err := renderValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	}
	return v, nil
}

// sortedKeys returns the keys of m in order, so requests built from rule maps
// do not depend on Go's random map iteration.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (p *Provider) apiRequest(cfg APIRequest, vars map[string]any) (*host.Request, error) {
	name := p.Rule.Name
	u, err := render(strings.TrimSpace(cfg.URL), vars, true)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: %w", name, err)
	}
	parsed, err := url.Parse(u)
	if err != nil || !isHTTPURL(u) {
		return nil, fmt.Errorf("kazumi: %s: invalid API URL %q", name, u)
	}
	if len(cfg.Query) > 0 {
		q := parsed.Query()
		for _, k := range sortedKeys(cfg.Query) {
			rv, err := renderValue(cfg.Query[k], vars)
			if err != nil {
				return nil, fmt.Errorf("kazumi: %s: %w", name, err)
			}
			q.Set(k, valueString(rv))
		}
		parsed.RawQuery = q.Encode()
	}
	req := p.baseRequest(cfg.Method, parsed.String(), true)
	for _, k := range sortedKeys(cfg.Headers) {
		rv, err := renderValue(cfg.Headers[k], vars)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: %w", name, err)
		}
		// A rule header replaces the default of the same name.
		for existing := range req.Header {
			if strings.EqualFold(existing, k) {
				delete(req.Header, existing)
			}
		}
		req.Header.Set(k, valueString(rv))
	}
	if cfg.Method == "POST" && cfg.BodyType != "none" && len(cfg.Body) > 0 {
		var raw any
		dec := json.NewDecoder(bytes.NewReader(cfg.Body))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("kazumi: %s: invalid body template: %w", name, err)
		}
		body, err := renderValue(raw, vars)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: %w", name, err)
		}
		switch cfg.BodyType {
		case "json":
			req.Body, _ = json.Marshal(body)
			if host.HeaderValue(req.Header, "Content-Type") == "" {
				req.Header.Set("Content-Type", "application/json")
			}
		case "form":
			form := url.Values{}
			if m, ok := body.(map[string]any); ok {
				for k, v := range m {
					form.Set(k, valueString(v))
				}
			}
			req.Body = []byte(form.Encode())
			if host.HeaderValue(req.Header, "Content-Type") == "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		}
	}
	return req, nil
}

// fetch sends req and returns a 2xx body. A verification page is reported as
// ErrCaptchaRequired (rule anti-crawler) or challenge.ErrChallengeRequired
// (Cloudflare, from the challenge transport).
func (p *Provider) fetch(ctx context.Context, req *host.Request) ([]byte, error) {
	if p.Transport == nil {
		return nil, host.ErrUnsupported
	}
	resp, err := p.Transport.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Body) > MaxBody {
		return nil, fmt.Errorf("kazumi: %s: response larger than %d bytes", p.Rule.Name, MaxBody)
	}
	if p.isCaptcha(resp.Body) {
		return nil, &ErrCaptchaRequired{Rule: p.Rule.Name}
	}
	if kind, ok := challenge.Detect(resp); ok {
		return nil, &challenge.ErrChallengeRequired{Kind: kind, URL: req.URL}
	}
	if resp.Status < 200 || resp.Status > 299 {
		return nil, fmt.Errorf("kazumi: %s: HTTP %d from %s", p.Rule.Name, resp.Status, req.URL)
	}
	return resp.Body, nil
}

func (p *Provider) isCaptcha(body []byte) bool {
	ac := p.Rule.AntiCrawler
	if !ac.Enabled {
		return false
	}
	if v := strings.TrimSpace(ac.CaptchaDetectValue); v != "" {
		switch ac.CaptchaDetectType {
		case DetectText:
			return bytes.Contains(body, []byte(v))
		case DetectRegex:
			re, err := regexp.Compile("(?is)" + v)
			return err == nil && re.Match(body)
		default:
			doc, err := parseHTML(body)
			if err != nil {
				return false
			}
			n, err := queryOne(doc, v)
			return err == nil && n != nil
		}
	}
	doc, err := parseHTML(body)
	if err != nil {
		return false
	}
	for _, expr := range []string{ac.CaptchaImage, ac.CaptchaButton} {
		if strings.TrimSpace(expr) == "" {
			continue
		}
		if n, err := queryOne(doc, expr); err == nil && n != nil {
			return true
		}
	}
	return false
}

// --- XPath parsing ------------------------------------------------------------

func (p *Provider) parseXPathSearch(body []byte) ([]video.Item, error) {
	r := p.Rule
	doc, err := parseHTML(body)
	if err != nil {
		return nil, err
	}
	nodes, err := queryAll(doc, r.SearchList)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: searchList: %w", r.Name, err)
	}
	items := []video.Item{}
	seen := map[string]bool{}
	for _, n := range nodes {
		nameNode, err := queryOne(n, r.SearchName)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: searchName: %w", r.Name, err)
		}
		linkNode, err := queryOne(n, r.SearchResult)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: searchResult: %w", r.Name, err)
		}
		name := strings.TrimSpace(nodeText(nameNode))
		src := strings.TrimSpace(attr(linkNode, "href"))
		if name == "" || src == "" {
			continue
		}
		id := NormalizeEpisodeURL(r.BaseURL, src)
		if seen[id] {
			continue
		}
		seen[id] = true
		items = append(items, video.Item{ID: id, Title: name})
	}
	return items, nil
}

// stripSpace removes every whitespace character. Go's \s is ASCII only, but
// Dart's matches the ideographic space U+3000, NBSP and BOM that Chinese
// sites put inside episode titles.
func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) && r != 0x85 || r == 0xFEFF {
			return -1
		}
		return r
	}, s)
}

func (p *Provider) parseXPathChapters(body []byte) ([]video.Line, error) {
	r := p.Rule
	doc, err := parseHTML(body)
	if err != nil {
		return nil, err
	}
	roads, err := queryAll(doc, r.ChapterRoads)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: chapterRoads: %w", r.Name, err)
	}
	var lines []video.Line
	for _, road := range roads {
		eps, err := queryAll(road, r.ChapterResult)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: chapterResult: %w", r.Name, err)
		}
		line := video.Line{}
		for i, ep := range eps {
			href := strings.TrimSpace(attr(ep, "href"))
			if href == "" {
				continue
			}
			title := stripSpace(nodeText(ep))
			if title == "" {
				title = "第" + strconv.Itoa(i+1) + "集"
			}
			line.Episodes = append(line.Episodes, video.Episode{ID: NormalizeEpisodeURL(r.BaseURL, href), Title: title})
		}
		if len(line.Episodes) == 0 {
			continue
		}
		line.Name = "播放线路" + strconv.Itoa(len(lines)+1)
		line.ID = "road" + strconv.Itoa(len(lines))
		lines = append(lines, line)
	}
	return lines, nil
}

// --- API parsing ----------------------------------------------------------------

func (p *Provider) parseAPISearch(body []byte) ([]video.Item, error) {
	r := p.Rule
	cfg := r.SearchAPI
	doc, err := decodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	nodes, err := readAll(doc, cfg.ListPath)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	items := []video.Item{}
	for _, n := range nodes {
		nv, _ := readFirst(n, cfg.NamePath)
		sv, _ := readFirst(n, cfg.SourcePath)
		name, src := stringValue(nv), stringValue(sv)
		if name == "" || src == "" {
			continue
		}
		if cfg.SourceTemplate != "" {
			// Plain replacement: @source may follow a letter (/GV@source).
			src = strings.ReplaceAll(cfg.SourceTemplate, "@source", encodeComponent(src))
		}
		items = append(items, video.Item{ID: src, Title: name})
	}
	return items, nil
}

func (p *Provider) parseAPIChapters(body []byte, source string) ([]video.Line, error) {
	r := p.Rule
	cfg := r.ChapterAPI
	doc, err := decodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	vars := map[string]any{"source": source}
	for k, path := range cfg.Variables {
		v, err := readFirst(doc, path)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
		}
		if v == nil {
			return nil, fmt.Errorf("kazumi: %s: chapter variable %s matched nothing (%s)", r.Name, k, path)
		}
		vars[k] = v
	}
	if cfg.Format == "delimited" {
		return p.parseDelimited(doc, vars)
	}
	return p.parseNested(doc, vars)
}

func (p *Provider) parseNested(doc any, vars map[string]any) ([]video.Line, error) {
	r := p.Rule
	cfg := r.ChapterAPI
	hasRoads := strings.TrimSpace(cfg.RoadsPath) != ""
	roads := []any{doc}
	if hasRoads {
		var err error
		if roads, err = readAll(doc, cfg.RoadsPath); err != nil {
			return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
		}
	}
	var lines []video.Line
	for ri, road := range roads {
		roadName := ""
		if hasRoads && cfg.RoadNamePath != "" {
			v, _ := readFirst(road, cfg.RoadNamePath)
			roadName = stringValue(v)
		}
		eps, err := readAll(road, cfg.EpisodesPath)
		if err != nil {
			return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
		}
		line := video.Line{}
		for ei, ep := range eps {
			nv, _ := readFirst(ep, cfg.EpisodeNamePath)
			raw := ""
			if cfg.EpisodeURLPath != "" {
				uv, _ := readFirst(ep, cfg.EpisodeURLPath)
				raw = stringValue(uv)
			}
			u, err := p.episodeURL(vars, raw, ri, ei)
			if err != nil {
				return nil, err
			}
			if u == "" {
				continue
			}
			title := stringValue(nv)
			if title == "" {
				title = "第" + strconv.Itoa(ei+1) + "集"
			}
			line.Episodes = append(line.Episodes, video.Episode{ID: u, Title: title})
		}
		if len(line.Episodes) == 0 {
			continue
		}
		if roadName == "" {
			roadName = "播放线路" + strconv.Itoa(len(lines)+1)
		}
		line.Name, line.ID = roadName, "road"+strconv.Itoa(len(lines))
		lines = append(lines, line)
	}
	return lines, nil
}

func (p *Provider) parseDelimited(doc any, vars map[string]any) ([]video.Line, error) {
	r := p.Rule
	cfg := r.ChapterAPI
	nv, err := readFirst(doc, cfg.RoadNamesPath)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	ev, err := readFirst(doc, cfg.RoadEpisodesPath)
	if err != nil {
		return nil, fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	names, episodes := stringValue(nv), stringValue(ev)
	if episodes == "" {
		return nil, nil
	}
	roadNames := strings.Split(names, cfg.RoadSeparator)
	var lines []video.Line
	for ri, group := range strings.Split(episodes, cfg.RoadSeparator) {
		line := video.Line{}
		for ei, entry := range strings.Split(group, cfg.EpisodeSeparator) {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			i := strings.Index(entry, cfg.FieldSeparator)
			if i < 0 {
				continue
			}
			title := strings.TrimSpace(entry[:i])
			raw := strings.TrimSpace(entry[i+len(cfg.FieldSeparator):])
			u, err := p.episodeURL(vars, raw, ri, ei)
			if err != nil {
				return nil, err
			}
			if u == "" {
				continue
			}
			if title == "" {
				title = "第" + strconv.Itoa(ei+1) + "集"
			}
			line.Episodes = append(line.Episodes, video.Episode{ID: u, Title: title})
		}
		if len(line.Episodes) == 0 {
			continue
		}
		name := ""
		if ri < len(roadNames) {
			name = strings.TrimSpace(roadNames[ri])
		}
		if name == "" {
			name = "播放线路" + strconv.Itoa(len(lines)+1)
		}
		line.Name, line.ID = name, "road"+strconv.Itoa(len(lines))
		lines = append(lines, line)
	}
	return lines, nil
}

func (p *Provider) episodeURL(root map[string]any, raw string, road, ep int) (string, error) {
	r := p.Rule
	page := r.ChapterAPI.EpisodePage
	if page == nil {
		return NormalizeEpisodeURL(r.BaseURL, raw), nil
	}
	vars := make(map[string]any, len(root)+5)
	for k, v := range root {
		vars[k] = v
	}
	vars["episodeUrl"] = raw
	vars["roadIndex"] = json.Number(strconv.Itoa(road))
	vars["roadNumber"] = json.Number(strconv.Itoa(road + 1))
	vars["episodeIndex"] = json.Number(strconv.Itoa(ep))
	vars["episodeNumber"] = json.Number(strconv.Itoa(ep + 1))
	path, err := render(page.URL, vars, true)
	if err != nil {
		return "", fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	u, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("kazumi: %s: invalid episode page URL %q", r.Name, path)
	}
	if len(page.Query) > 0 {
		q := u.Query()
		for _, k := range sortedKeys(page.Query) {
			rv, err := renderValue(page.Query[k], vars)
			if err != nil {
				return "", fmt.Errorf("kazumi: %s: %w", r.Name, err)
			}
			q.Set(k, valueString(rv))
		}
		u.RawQuery = q.Encode()
	}
	return NormalizeEpisodeURL(r.BaseURL, u.String()), nil
}

// --- URLs ---------------------------------------------------------------------

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

// NormalizeEpisodeURL resolves raw against base and canonicalises it so the
// same episode always gets the same identity: the host is lower-cased,
// same-host links take the base's scheme, trailing slashes of non-root paths
// and an empty query are removed. A raw value that cannot be made absolute
// is returned trimmed.
func NormalizeEpisodeURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	b, berr := url.Parse(strings.TrimSpace(base))
	validBase := berr == nil && b.Scheme != "" && b.Host != ""
	var res *url.URL
	switch {
	case ref.Scheme != "" && ref.Host != "":
		res = ref
	case validBase:
		res = b.ResolveReference(ref)
	default:
		return raw
	}
	res.Host = strings.ToLower(res.Host)
	if validBase && isHTTP(b.Scheme) && isHTTP(res.Scheme) && res.Scheme != b.Scheme && strings.EqualFold(res.Host, b.Host) {
		res.Scheme = b.Scheme
	}
	for len(res.Path) > 1 && strings.HasSuffix(res.Path, "/") {
		res.Path = strings.TrimSuffix(res.Path, "/")
		res.RawPath = strings.TrimSuffix(res.RawPath, "/")
	}
	if res.RawQuery == "" {
		res.ForceQuery = false
	}
	return res.String()
}

func isHTTP(s string) bool { return s == "http" || s == "https" }

var _ video.Provider = (*Provider)(nil)

// IsCaptcha reports whether err means the rule's verification page.
func IsCaptcha(err error) bool {
	var e *ErrCaptchaRequired
	return errors.As(err, &e)
}
