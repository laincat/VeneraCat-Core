// Package kazumi runs Kazumi video rules (github.com/Predidit/KazumiRules,
// MIT licensed) as VeneraCat video providers.
//
// A Kazumi rule is declarative: it names XPath selectors (or JSONPath for
// API rules) for the search page and the episode list. It does not execute
// JavaScript. The playable media URL is not in the rule: Kazumi finds it by
// loading the episode page in a WebView and intercepting media requests.
// This package therefore returns a Play with Sniff set, and the client's
// Sniffer does the WebView part.
//
// Field semantics follow the rule format as published in KazumiRules and as
// observed in Kazumi's behaviour; the implementation is independent Go code.
package kazumi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/laincat/veneracat-core/attribution"
)

// Rule modes.
const (
	ModeXPath = "xpath"
	ModeAPI   = "api"
)

// Rule is a Kazumi rule file. Unknown fields are ignored, matching Kazumi.
type Rule struct {
	API         string `json:"api"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	MultiSource bool   `json:"muliSources"` // sic: the published field name
	UseWebview  bool   `json:"useWebview"`
	// UseNativePlayer and UseLegacyParser change how Kazumi's WebView finds
	// the media URL. UseLegacyParser means: report iframe src attributes
	// instead of intercepting m3u8 responses.
	UseNativePlayer bool   `json:"useNativePlayer"`
	UsePost         bool   `json:"usePost"`
	UseLegacyParser bool   `json:"useLegacyParser"`
	AdBlocker       bool   `json:"adBlocker"`
	UserAgent       string `json:"userAgent"`
	BaseURL         string `json:"baseURL"`
	SearchURL       string `json:"searchURL"`
	SearchList      string `json:"searchList"`
	SearchName      string `json:"searchName"`
	SearchResult    string `json:"searchResult"`
	ChapterRoads    string `json:"chapterRoads"`
	ChapterResult   string `json:"chapterResult"`
	Referer         string `json:"referer"`
	SearchMode      string `json:"searchMode"`
	ChapterMode     string `json:"chapterMode"`

	SearchAPI   APISearch   `json:"searchApiConfig"`
	ChapterAPI  APIChapter  `json:"chapterApiConfig"`
	AntiCrawler AntiCrawler `json:"antiCrawlerConfig"`

	Deprecated bool `json:"deprecated"`

	// Author and Homepage are VeneraCat extensions; published Kazumi rules
	// carry neither, and Kazumi ignores unknown fields, so rules that add
	// them stay valid for Kazumi.
	Author      json.RawMessage  `json:"author,omitempty"`
	Homepage    json.RawMessage  `json:"homepage,omitempty"`
	Attribution attribution.Info `json:"-"`
}

// APIRequest is a templated HTTP request. @name placeholders are replaced by
// variables; a value that is exactly "@name" keeps the variable's JSON type.
type APIRequest struct {
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Headers  map[string]any  `json:"headers"`
	Query    map[string]any  `json:"query"`
	BodyType string          `json:"bodyType"` // none | json | form
	Body     json.RawMessage `json:"body"`
}

// APISearch configures search in API mode.
type APISearch struct {
	Request        APIRequest `json:"request"`
	ListPath       string     `json:"listPath"`
	NamePath       string     `json:"namePath"`
	SourcePath     string     `json:"sourcePath"`
	SourceTemplate string     `json:"sourceTemplate"`
}

// APIEpisodePage builds the playback page URL instead of reading it.
type APIEpisodePage struct {
	URL   string         `json:"url"`
	Query map[string]any `json:"query"`
}

// APIChapter configures the episode list in API mode.
type APIChapter struct {
	Request          APIRequest        `json:"request"`
	Format           string            `json:"format"` // nested | delimited
	RoadsPath        string            `json:"roadsPath"`
	RoadNamePath     string            `json:"roadNamePath"`
	EpisodesPath     string            `json:"episodesPath"`
	EpisodeNamePath  string            `json:"episodeNamePath"`
	EpisodeURLPath   string            `json:"episodeUrlPath"`
	RoadNamesPath    string            `json:"roadNamesPath"`
	RoadEpisodesPath string            `json:"roadEpisodesPath"`
	RoadSeparator    string            `json:"roadSeparator"`
	EpisodeSeparator string            `json:"episodeSeparator"`
	FieldSeparator   string            `json:"fieldSeparator"`
	Variables        map[string]string `json:"variables"`
	EpisodePage      *APIEpisodePage   `json:"episodePage"`
}

// Captcha kinds and detection modes of AntiCrawler.
const (
	CaptchaImage      = 1
	CaptchaAutoClick  = 2
	CaptchaJavaScript = 3

	DetectXPath = 1
	DetectText  = 2
	DetectRegex = 3
)

// AntiCrawler describes a site verification page.
type AntiCrawler struct {
	Enabled            bool   `json:"enabled"`
	CaptchaType        int    `json:"captchaType"`
	CaptchaImage       string `json:"captchaImage"`
	CaptchaInput       string `json:"captchaInput"`
	CaptchaButton      string `json:"captchaButton"`
	CaptchaDetectType  int    `json:"captchaDetectType"`
	CaptchaDetectValue string `json:"captchaDetectValue"`
	CaptchaScript      string `json:"captchaScript"`
}

// boolDefaults holds the fields whose published defaults differ from Go's
// zero values; it is decoded separately from Rule.
type boolDefaults struct {
	MultiSource     *bool `json:"muliSources"`
	UseWebview      *bool `json:"useWebview"`
	UseNativePlayer *bool `json:"useNativePlayer"`
}

// Parse decodes a rule and applies the format's defaults.
func Parse(data []byte) (*Rule, error) {
	var r Rule
	// UseNumber keeps numeric query, header and body values exact; float64
	// would round an ID such as 12345678901234567890.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("kazumi: invalid rule JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("kazumi: invalid rule JSON: trailing data")
	}
	var raw boolDefaults
	_ = json.Unmarshal(data, &raw)
	r.MultiSource = boolOr(raw.MultiSource, true)
	r.UseWebview = boolOr(raw.UseWebview, true)
	r.UseNativePlayer = boolOr(raw.UseNativePlayer, true)
	if r.API == "" {
		r.API = "1"
	}
	if r.Type == "" {
		r.Type = "anime"
	}
	r.Attribution, _ = attribution.ParseJSON(r.Author, r.Homepage)
	r.SearchMode = normalizeMode(r.SearchMode)
	r.ChapterMode = normalizeMode(r.ChapterMode)
	r.SearchAPI.applyDefaults()
	r.ChapterAPI.applyDefaults()
	if r.AntiCrawler.CaptchaDetectType == 0 {
		r.AntiCrawler.CaptchaDetectType = DetectXPath
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func normalizeMode(m string) string {
	if m == ModeAPI {
		return ModeAPI
	}
	return ModeXPath
}

func (s *APISearch) applyDefaults() {
	s.Request.applyDefaults()
	if s.ListPath == "" {
		s.ListPath = "$.data[*]"
	}
	if s.NamePath == "" {
		s.NamePath = "$.name"
	}
	if s.SourcePath == "" {
		s.SourcePath = "$.url"
	}
}

func (c *APIChapter) applyDefaults() {
	c.Request.applyDefaults()
	if c.Format != "delimited" {
		c.Format = "nested"
	}
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&c.RoadsPath, "$.data.roads[*]")
	def(&c.RoadNamePath, "$.name")
	def(&c.EpisodesPath, "$.episodes[*]")
	def(&c.EpisodeNamePath, "$.name")
	def(&c.EpisodeURLPath, "$.url")
	def(&c.RoadSeparator, "$$$")
	def(&c.EpisodeSeparator, "#")
	def(&c.FieldSeparator, "$")
}

func (q *APIRequest) applyDefaults() {
	q.Method = strings.ToUpper(strings.TrimSpace(q.Method))
	if q.Method == "" {
		q.Method = "GET"
	}
	switch q.BodyType {
	case "json", "form":
	default:
		q.BodyType = "none"
	}
}

var keySafe = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// Key derives a VeneraCat provider key from the rule name, e.g. "AGE" ->
// "kz_AGE". Keys share the comic-source alphabet.
func (r *Rule) Key() string {
	k := strings.Trim(keySafe.ReplaceAllString(r.Name, "_"), "_")
	if k == "" {
		k = "rule"
	}
	return "kz_" + k
}

// Validate checks that the selectors and paths the rule's mode needs exist
// and are well-formed. It compiles XPath expressions so a broken rule fails
// at import, not on the first search.
func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("kazumi: rule name is required")
	}
	if r.SearchMode == ModeXPath {
		if r.SearchURL == "" {
			return fmt.Errorf("kazumi: %s: searchURL is required", r.Name)
		}
		for _, f := range []struct{ field, expr string }{{"searchList", r.SearchList}, {"searchName", r.SearchName}, {"searchResult", r.SearchResult}} {
			if err := checkXPath(f.field, f.expr); err != nil {
				return fmt.Errorf("kazumi: %s: %w", r.Name, err)
			}
		}
	} else {
		if r.SearchAPI.Request.URL == "" {
			return fmt.Errorf("kazumi: %s: searchApiConfig.request.url is required", r.Name)
		}
		if r.SearchAPI.SourceTemplate != "" && !strings.Contains(r.SearchAPI.SourceTemplate, "@source") {
			return fmt.Errorf("kazumi: %s: sourceTemplate must reference @source", r.Name)
		}
		for _, p := range []string{r.SearchAPI.ListPath, r.SearchAPI.NamePath, r.SearchAPI.SourcePath} {
			if _, err := compilePath(p); err != nil {
				return fmt.Errorf("kazumi: %s: %w", r.Name, err)
			}
		}
	}
	if err := checkMethod(r.SearchAPI.Request, r.SearchMode == ModeAPI); err != nil {
		return fmt.Errorf("kazumi: %s: %w", r.Name, err)
	}
	if r.ChapterMode == ModeXPath {
		for _, f := range []struct{ field, expr string }{{"chapterRoads", r.ChapterRoads}, {"chapterResult", r.ChapterResult}} {
			if err := checkXPath(f.field, f.expr); err != nil {
				return fmt.Errorf("kazumi: %s: %w", r.Name, err)
			}
		}
		return nil
	}
	return r.ChapterAPI.validate(r.Name)
}

func checkMethod(q APIRequest, active bool) error {
	if !active {
		return nil
	}
	if q.Method != "GET" && q.Method != "POST" {
		return fmt.Errorf("only GET/POST are supported, got %s", q.Method)
	}
	return nil
}

func (c *APIChapter) validate(name string) error {
	if c.Request.URL == "" {
		return fmt.Errorf("kazumi: %s: chapterApiConfig.request.url is required", name)
	}
	if err := checkMethod(c.Request, true); err != nil {
		return fmt.Errorf("kazumi: %s: %w", name, err)
	}
	paths := []string{}
	for _, p := range c.Variables {
		paths = append(paths, p)
	}
	if c.Format == "delimited" {
		paths = append(paths, c.RoadNamesPath, c.RoadEpisodesPath)
	} else {
		paths = append(paths, c.RoadsPath, c.RoadNamePath, c.EpisodesPath, c.EpisodeNamePath)
		if c.EpisodePage == nil {
			paths = append(paths, c.EpisodeURLPath)
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := compilePath(p); err != nil {
			return fmt.Errorf("kazumi: %s: %w", name, err)
		}
	}
	if c.EpisodePage != nil && strings.TrimSpace(c.EpisodePage.URL) == "" {
		return fmt.Errorf("kazumi: %s: episodePage.url is empty", name)
	}
	return nil
}
