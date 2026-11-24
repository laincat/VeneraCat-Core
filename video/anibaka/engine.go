package anibaka

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/video"
	"github.com/laincat/veneracat-core/video/hls"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// Provider executes one validated anx-rule/2 Rule as a video.Provider.
type Provider struct {
	Rule      *Rule
	Transport host.Transport
	Sniffer   host.Sniffer
}

// New returns an AniBaka provider using t for all HTTP traffic.
func New(rule *Rule, t host.Transport) *Provider {
	return &Provider{Rule: rule, Transport: t}
}

// NewWithSniffer returns an AniBaka provider with an optional host.Sniffer for
// rules that opt into inline sniffing.
func NewWithSniffer(rule *Rule, t host.Transport, s host.Sniffer) *Provider {
	return &Provider{Rule: rule, Transport: t, Sniffer: s}
}

// Info implements video.Provider.
func (p *Provider) Info() video.Info {
	return video.Info{
		Key:         p.Rule.ResolvedKey(),
		Name:        p.Rule.Name,
		Version:     p.Rule.Version,
		Kind:        Kind,
		BaseURL:     p.Rule.BaseURL,
		Attribution: p.Rule.Attribution,
	}
}

// Partition isolates cookies and clearance for this provider.
func (p *Provider) Partition() string { return "video:" + p.Info().Key }

// RewriteHLS applies the rule's HLS rewriting and ad-filtering configuration
// using video/hls.
func (p *Provider) RewriteHLS(playlist []byte, baseURL string, override *HLSConfig) (hls.RewriteResult, error) {
	cfg := p.Rule.HLS
	if override != nil {
		cfg = override
	}
	opts := hls.RewriteOptions{
		BaseURL: baseURL,
	}
	if cfg != nil {
		opts.DropAds = cfg.FilterAds
		opts.StripPrefix = cfg.StripPrefix
		opts.SelectVariant = chooserFromConfig(cfg)
	}
	return hls.RewriteDetailed(playlist, opts)
}

func chooserFromConfig(cfg *HLSConfig) hls.VariantChooser {
	if cfg == nil {
		return hls.VariantChooser{Mode: hls.VariantAll}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "highest":
		return hls.VariantChooser{Mode: hls.VariantHighestBandwidth}
	case "maxheight":
		return hls.VariantChooser{Mode: hls.VariantMaxHeight, MaxHeight: cfg.MaxHeight}
	default:
		if cfg.MaxHeight > 0 {
			return hls.VariantChooser{Mode: hls.VariantMaxHeight, MaxHeight: cfg.MaxHeight}
		}
		return hls.VariantChooser{Mode: hls.VariantAll}
	}
}

// Search implements video.Provider.
func (p *Provider) Search(ctx context.Context, keyword, cursor string) (*video.Page, error) {
	st, cancel, err := p.runStage(ctx, &p.Rule.Search, map[string]any{
		"keyword": keyword,
		"cursor":  cursor,
	})
	if cancel != nil {
		defer cancel()
	}
	if err != nil {
		return nil, err
	}
	rawItems := st.resolveOutputValue(p.Rule.Search.Output.Items, "items", true)
	items := toVideoItems(rawItems, p.Rule.BaseURL)
	nextVal := ""
	if p.Rule.Search.Output.Next != "" {
		nextVal = st.renderTemplate(p.Rule.Search.Output.Next)
	} else if v, ok := st.vars["next"]; ok {
		nextVal = toString(v)
	}
	return &video.Page{Items: items, Next: strings.TrimSpace(nextVal)}, nil
}

// Details implements video.Provider.
func (p *Provider) Details(ctx context.Context, id string) (*video.Details, error) {
	st, cancel, err := p.runStage(ctx, &p.Rule.Detail, map[string]any{
		"id": id,
	})
	if cancel != nil {
		defer cancel()
	}
	if err != nil {
		return nil, err
	}
	out := p.Rule.Detail.Output
	title := st.resolveOutputString(out.Title, "title")
	cover := resolveMaybeRelative(p.Rule.BaseURL, st.resolveOutputString(out.Cover, "cover"))
	desc := st.resolveOutputString(out.Description, "description")
	tags := toStringSlice(st.resolveOutputValue(out.Tags, "tags", false))
	lines := toVideoLines(st.resolveOutputValue(out.Lines, "lines", true), p.Rule.BaseURL)
	if len(lines) == 0 {
		return nil, fmt.Errorf("anibaka: %s: no playable episodes", p.Rule.Name)
	}
	return &video.Details{
		ID:          id,
		Title:       title,
		Cover:       cover,
		Description: desc,
		Tags:        tags,
		Lines:       lines,
	}, nil
}

// Play implements video.Provider.
func (p *Provider) Play(ctx context.Context, itemID, lineID, episodeID string) (*video.Play, error) {
	st, cancel, err := p.runStage(ctx, &p.Rule.Play, map[string]any{
		"itemId":    itemID,
		"id":        itemID,
		"lineId":    lineID,
		"episodeId": episodeID,
	})
	if cancel != nil {
		defer cancel()
	}
	if err != nil {
		return nil, err
	}
	out := p.Rule.Play.Output

	play := &video.Play{}
	// Headers: rule defaults + stage output headers + runtime playHeaders var.
	hdr := p.defaultPlayHeaders()
	for k, v := range out.Headers {
		val := st.renderTemplate(v)
		if val != "" {
			hdr.Set(k, val)
		}
	}
	if v, ok := st.vars["headers"]; ok {
		if m, ok := v.(map[string]any); ok {
			for k, val := range m {
				if s := toString(val); s != "" {
					hdr.Set(k, s)
				}
			}
		}
	}
	if len(hdr) > 0 {
		play.Header = hdr
	}

	// HLS options from rule or stage output.
	filterAds := p.Rule.HLS != nil && p.Rule.HLS.FilterAds
	if out.FilterAds != nil {
		filterAds = *out.FilterAds
	}
	if filterAds {
		play.HLS = &video.HLSOptions{FilterAds: true}
	}

	if st.sniffSignal != nil {
		play.Sniff = st.sniffSignal
		if err := play.Validate(); err != nil {
			return nil, fmt.Errorf("anibaka: %s: %w", p.Rule.Name, err)
		}
		return play, nil
	}

	rawURL := strings.TrimSpace(st.resolveOutputString(out.URL, "videoUrl"))
	if rawURL == "" {
		rawURL = strings.TrimSpace(st.resolveOutputString("", "url"))
	}
	if rawURL == "" {
		if s, ok := st.pipe.(string); ok {
			rawURL = strings.TrimSpace(s)
		}
	}
	rawURL = resolveMaybeRelative(p.Rule.BaseURL, rawURL)

	// Stage-level sniff fallback when no direct URL was obtained.
	if rawURL == "" && out.Sniff != nil {
		sniff, resolved, err := p.handleSniff(st.ctx, st, out.Sniff.Page, out.Sniff.Match, out.Sniff.FollowIframes, out.Sniff.TimeoutMs, out.Sniff.Inline)
		if err != nil {
			return nil, err
		}
		if resolved != nil {
			rawURL = resolved.URL
			for k, vals := range resolved.Header {
				if play.Header == nil {
					play.Header = http.Header{}
				}
				for _, val := range vals {
					play.Header.Set(k, val)
				}
			}
		} else if sniff != nil {
			play.Sniff = sniff
			if err := play.Validate(); err != nil {
				return nil, fmt.Errorf("anibaka: %s: %w", p.Rule.Name, err)
			}
			return play, nil
		}
	}

	play.URL = rawURL
	fmtStr := strings.ToLower(strings.TrimSpace(st.resolveOutputString(out.Format, "format")))
	switch fmtStr {
	case "hls", "m3u8":
		play.Format = video.FormatHLS
	case "mp4":
		play.Format = video.FormatMP4
	case "dash", "mpd":
		play.Format = video.FormatDASH
	default:
		play.Format = inferFormat(rawURL)
	}

	if rawVars := st.resolveOutputValue(out.Variants, "variants", false); rawVars != nil {
		play.Variants = toVideoVariants(rawVars, p.Rule.BaseURL)
	}

	if err := play.Validate(); err != nil {
		return nil, fmt.Errorf("anibaka: %s: %w", p.Rule.Name, err)
	}
	return play, nil
}

func (p *Provider) defaultPlayHeaders() http.Header {
	h := http.Header{}
	if p.Rule.Referer != "" {
		h.Set("Referer", p.Rule.Referer)
	} else if p.Rule.BaseURL != "" {
		h.Set("Referer", strings.TrimRight(p.Rule.BaseURL, "/")+"/")
	}
	if p.Rule.UserAgent != "" {
		h.Set("User-Agent", p.Rule.UserAgent)
	}
	for k, v := range p.Rule.Headers {
		h.Set(k, v)
	}
	return h
}

// --- pipeline runtime -------------------------------------------------------

type execState struct {
	provider    *Provider
	ctx         context.Context
	vars        map[string]any
	pipe        any
	lastURL     string
	stepsUsed   int
	maxSteps    int
	maxLoop     int
	sniffSignal *video.Sniff
}

func (p *Provider) runStage(ctx context.Context, stage *Stage, initial map[string]any) (*execState, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if stage.TimeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(stage.TimeoutMs)*time.Millisecond)
	}
	maxSteps := p.Rule.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	maxLoop := p.Rule.MaxLoop
	if maxLoop <= 0 {
		maxLoop = DefaultMaxLoopItems
	}
	vars := map[string]any{
		"baseUrl": p.Rule.BaseURL,
	}
	for k, v := range initial {
		vars[k] = v
	}
	st := &execState{
		provider: p,
		ctx:      ctx,
		vars:     vars,
		lastURL:  p.Rule.BaseURL,
		maxSteps: maxSteps,
		maxLoop:  maxLoop,
	}
	if err := st.execSteps(stage.Steps, 1); err != nil {
		return st, cancel, err
	}
	return st, cancel, nil
}

func (s *execState) consumeStep() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.stepsUsed++
	if s.stepsUsed > s.maxSteps {
		return fmt.Errorf("%w: exceeded %d steps", ErrStepLimitExceeded, s.maxSteps)
	}
	return nil
}

func (s *execState) execSteps(steps []Step, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("%w: depth %d", ErrDepthExceeded, depth)
	}
	for i := range steps {
		if err := s.consumeStep(); err != nil {
			return err
		}
		if err := s.execStep(&steps[i], depth); err != nil {
			return err
		}
		if s.sniffSignal != nil {
			return nil
		}
	}
	return nil
}

func (s *execState) inputOf(st *Step) any {
	if st.From != "" {
		return s.vars[st.From]
	}
	return s.pipe
}

func (s *execState) store(st *Step, val any) {
	if st.To != "" {
		s.vars[st.To] = val
	}
	if !st.KeepPipe {
		s.pipe = val
	}
}

func (s *execState) execStep(st *Step, depth int) error {
	op := strings.ToLower(strings.TrimSpace(st.Op))
	in := s.inputOf(st)

	switch op {
	case "const", "set":
		val := st.Value
		if val == nil && st.Template != "" {
			val = s.renderTemplate(st.Template)
		} else {
			val = s.renderAny(val, in)
		}
		s.store(st, val)
		return nil

	case "get":
		s.store(st, in)
		return nil

	case "default", "pick":
		if isEmptyValue(in) {
			val := st.Value
			if str, ok := val.(string); ok {
				val = s.renderTemplate(str)
			} else if val == nil && st.Template != "" {
				val = s.renderTemplate(st.Template)
			}
			s.store(st, val)
		} else {
			s.store(st, in)
		}
		return nil

	case "template":
		tpl := st.Template
		if tpl == "" {
			tpl = toString(st.Value)
		}
		s.store(st, s.renderTemplateWithPipe(tpl, in))
		return nil

	case "trim":
		s.store(st, strings.TrimSpace(toString(in)))
		return nil
	case "lower":
		s.store(st, strings.ToLower(toString(in)))
		return nil
	case "upper":
		s.store(st, strings.ToUpper(toString(in)))
		return nil
	case "replace":
		s.store(st, strings.Replace(toString(in), s.renderTemplate(st.Old), s.renderTemplate(st.New), 1))
		return nil
	case "replaceall":
		s.store(st, strings.ReplaceAll(toString(in), s.renderTemplate(st.Old), s.renderTemplate(st.New)))
		return nil
	case "prefix":
		s.store(st, s.renderTemplate(st.Prefix)+toString(in))
		return nil
	case "suffix":
		s.store(st, toString(in)+s.renderTemplate(st.Suffix))
		return nil
	case "substring":
		runes := []rune(toString(in))
		start := st.Start
		if start < 0 {
			start = max(0, len(runes)+start)
		}
		if start > len(runes) {
			start = len(runes)
		}
		end := len(runes)
		if st.HasEnd || st.End != 0 {
			end = st.End
			if end < 0 {
				end = max(0, len(runes)+end)
			}
			if end > len(runes) {
				end = len(runes)
			}
		}
		if end < start {
			end = start
		}
		s.store(st, string(runes[start:end]))
		return nil
	case "split":
		sep := st.Separator
		parts := strings.Split(toString(in), sep)
		out := make([]any, len(parts))
		for i, part := range parts {
			out[i] = part
		}
		s.store(st, out)
		return nil
	case "join":
		list := toAnySlice(in)
		parts := make([]string, len(list))
		for i, it := range list {
			parts[i] = toString(it)
		}
		s.store(st, strings.Join(parts, st.Separator))
		return nil
	case "first":
		list := toAnySlice(in)
		if len(list) == 0 {
			s.store(st, "")
		} else {
			s.store(st, list[0])
		}
		return nil
	case "last":
		list := toAnySlice(in)
		if len(list) == 0 {
			s.store(st, "")
		} else {
			s.store(st, list[len(list)-1])
		}
		return nil
	case "index":
		list := toAnySlice(in)
		idx := st.Index
		if idx < 0 {
			idx = len(list) + idx
		}
		if idx < 0 || idx >= len(list) {
			s.store(st, "")
		} else {
			s.store(st, list[idx])
		}
		return nil
	case "slice":
		list := toAnySlice(in)
		start := st.Start
		if start < 0 {
			start = max(0, len(list)+start)
		}
		if start > len(list) {
			start = len(list)
		}
		end := len(list)
		if st.HasEnd || st.End != 0 {
			end = st.End
			if end < 0 {
				end = max(0, len(list)+end)
			}
			if end > len(list) {
				end = len(list)
			}
		}
		if end < start {
			end = start
		}
		cp := make([]any, end-start)
		copy(cp, list[start:end])
		s.store(st, cp)
		return nil
	case "flatten":
		list := toAnySlice(in)
		var out []any
		for _, item := range list {
			if sub, ok := item.([]any); ok {
				out = append(out, sub...)
			} else {
				out = append(out, item)
			}
		}
		s.store(st, out)
		return nil
	case "dedupe":
		list := toAnySlice(in)
		seen := map[string]bool{}
		out := make([]any, 0, len(list))
		for _, item := range list {
			k := toString(item)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, item)
		}
		s.store(st, out)
		return nil
	case "reverse":
		list := toAnySlice(in)
		out := make([]any, len(list))
		for i := range list {
			out[len(list)-1-i] = list[i]
		}
		s.store(st, out)
		return nil
	case "urlresolve":
		base := s.renderTemplate(st.Base)
		if base == "" {
			base = s.lastURL
		}
		s.store(st, resolveMaybeRelative(base, toString(in)))
		return nil
	case "urlencode":
		s.store(st, url.QueryEscape(toString(in)))
		return nil
	case "urldecode":
		u, err := url.QueryUnescape(toString(in))
		if err != nil {
			return fmt.Errorf("anibaka: urldecode: %w", err)
		}
		s.store(st, u)
		return nil
	case "htmlunescape":
		s.store(st, stdhtml.UnescapeString(toString(in)))
		return nil
	case "jsonstringify":
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("anibaka: jsonstringify: %w", err)
		}
		s.store(st, string(b))
		return nil

	case "fetch":
		return s.execFetch(st, in)

	case "select", "css", "xpath":
		mode := strings.ToLower(strings.TrimSpace(st.Mode))
		if op == "css" {
			mode = "css"
		} else if op == "xpath" {
			mode = "xpath"
		}
		if mode == "" {
			mode = "css"
		}
		out, err := evalSelect(toString(in), mode, st.Selector, st.Attr, st.All, st.Fields)
		if err != nil {
			return err
		}
		s.store(st, out)
		return nil

	case "regex", "regexall", "regexreplace":
		re, err := regexp.Compile(st.Pattern)
		if err != nil {
			return fmt.Errorf("anibaka: regex: %w", err)
		}
		src := toString(in)
		if op == "regexreplace" || st.Replace != nil {
			repl := ""
			if st.Replace != nil {
				repl = s.renderTemplate(*st.Replace)
			}
			s.store(st, re.ReplaceAllString(src, repl))
			return nil
		}
		if op == "regexall" || st.All {
			matches := re.FindAllStringSubmatch(src, -1)
			out := make([]any, 0, len(matches))
			for _, m := range matches {
				if st.Group < len(m) {
					out = append(out, m[st.Group])
				}
			}
			s.store(st, out)
			return nil
		}
		m := re.FindStringSubmatch(src)
		if st.Group < len(m) {
			s.store(st, m[st.Group])
		} else {
			s.store(st, "")
		}
		return nil

	case "json", "jsonpath":
		doc, err := decodeJSONValue(in)
		if err != nil {
			return err
		}
		var matched []any
		if st.Path == "" {
			matched = []any{doc}
		} else {
			matched, err = evalJSONPath(doc, st.Path)
			if err != nil {
				return err
			}
		}
		if len(st.JSONFields) > 0 {
			extractObj := func(item any) (map[string]any, error) {
				obj := make(map[string]any, len(st.JSONFields))
				for fname, fpath := range st.JSONFields {
					vals, err := evalJSONPath(item, fpath)
					if err != nil {
						return nil, err
					}
					if len(vals) == 0 {
						obj[fname] = ""
					} else if len(vals) == 1 {
						obj[fname] = vals[0]
					} else {
						obj[fname] = vals
					}
				}
				return obj, nil
			}
			if st.All {
				out := make([]any, 0, len(matched))
				for _, item := range matched {
					obj, err := extractObj(item)
					if err != nil {
						return err
					}
					out = append(out, obj)
				}
				s.store(st, out)
				return nil
			}
			if len(matched) == 0 {
				s.store(st, map[string]any{})
				return nil
			}
			obj, err := extractObj(matched[0])
			if err != nil {
				return err
			}
			s.store(st, obj)
			return nil
		}
		if st.All {
			s.store(st, matched)
			return nil
		}
		if len(matched) == 0 {
			s.store(st, "")
		} else {
			s.store(st, matched[0])
		}
		return nil

	case "crypto":
		res, err := runCrypto(
			st.Algorithm,
			toString(in),
			s.renderTemplate(st.Key),
			s.renderTemplate(st.IV),
			st.InputEncoding,
			st.KeyEncoding,
			st.IVEncoding,
			st.OutputEncoding,
			st.Padding,
		)
		if err != nil {
			return err
		}
		s.store(st, res)
		return nil

	case "branch", "if":
		ok, err := evalCondition(st.Condition, in, s.renderTemplate(st.Target))
		if err != nil {
			return err
		}
		if ok {
			return s.execSteps(st.Then, depth+1)
		}
		return s.execSteps(st.Else, depth+1)

	case "map", "foreach":
		items := toAnySlice(in)
		if len(items) > s.maxLoop {
			return fmt.Errorf("%w: %d items exceeds limit %d", ErrLoopLimitExceeded, len(items), s.maxLoop)
		}
		itemVar := st.ItemVar
		if itemVar == "" {
			itemVar = "item"
		}
		indexVar := st.IndexVar
		if indexVar == "" {
			indexVar = "index"
		}
		prevItem, hadItem := s.vars[itemVar]
		prevIndex, hadIndex := s.vars[indexVar]
		savedPipe := s.pipe
		out := make([]any, 0, len(items))
		for idx, it := range items {
			s.vars[itemVar] = it
			s.vars[indexVar] = json.Number(strconv.Itoa(idx))
			s.pipe = it
			if err := s.execSteps(st.Steps, depth+1); err != nil {
				return err
			}
			if s.sniffSignal != nil {
				return nil
			}
			out = append(out, s.pipe)
		}
		if hadItem {
			s.vars[itemVar] = prevItem
		} else {
			delete(s.vars, itemVar)
		}
		if hadIndex {
			s.vars[indexVar] = prevIndex
		} else {
			delete(s.vars, indexVar)
		}
		s.pipe = savedPipe
		s.store(st, out)
		return nil

	case "filter":
		items := toAnySlice(in)
		if len(items) > s.maxLoop {
			return fmt.Errorf("%w: %d items exceeds limit %d", ErrLoopLimitExceeded, len(items), s.maxLoop)
		}
		target := s.renderTemplate(st.Target)
		out := make([]any, 0, len(items))
		for _, it := range items {
			if err := s.consumeStep(); err != nil {
				return err
			}
			ok, err := evalCondition(st.Condition, it, target)
			if err != nil {
				return err
			}
			if ok {
				out = append(out, it)
			}
		}
		s.store(st, out)
		return nil

	case "sniff":
		if st.WhenEmpty && !isEmptyValue(in) {
			return nil
		}
		sniff, resolved, err := s.provider.handleSniff(s.ctx, s, st.Page, st.Match, st.FollowIframes, st.TimeoutMs, st.Inline)
		if err != nil {
			return err
		}
		if resolved != nil {
			s.store(st, resolved.URL)
			if len(resolved.Header) > 0 {
				hdrMap := map[string]any{}
				for k, vals := range resolved.Header {
					if len(vals) > 0 {
						hdrMap[k] = vals[0]
					}
				}
				s.vars["headers"] = hdrMap
			}
			return nil
		}
		s.sniffSignal = sniff
		return nil

	case "hls", "hlsrewrite":
		base := s.renderTemplate(st.BaseURL)
		if base == "" {
			base = s.lastURL
		}
		chooser := hls.VariantChooser{Mode: hls.VariantAll}
		switch strings.ToLower(strings.TrimSpace(st.SelectVariant)) {
		case "highest":
			chooser.Mode = hls.VariantHighestBandwidth
		case "maxheight":
			chooser.Mode = hls.VariantMaxHeight
			chooser.MaxHeight = st.MaxHeight
		}
		if strings.EqualFold(strings.TrimSpace(st.OutputMode), "url") {
			if chooser.Mode == hls.VariantAll {
				chooser.Mode = hls.VariantHighestBandwidth
			}
			chooser.Output = hls.OutputReference
		}
		rw, err := hls.RewriteDetailed([]byte(toString(in)), hls.RewriteOptions{
			BaseURL:       base,
			DropAds:       st.DropAds,
			StripPrefix:   st.StripPrefix,
			SelectVariant: chooser,
		})
		if err != nil {
			return fmt.Errorf("anibaka: hls rewrite: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(st.OutputMode), "url") {
			u := rw.SelectedURL
			if u == "" {
				u = strings.TrimSpace(string(rw.Body))
			}
			s.store(st, u)
		} else {
			s.store(st, string(rw.Body))
		}
		return nil
	}
	return fmt.Errorf("anibaka: unknown op %q", st.Op)
}

func (s *execState) execFetch(st *Step, in any) error {
	if s.provider.Transport == nil {
		return errors.New("anibaka: host transport is required")
	}
	rawURL := s.renderTemplateWithPipe(st.URL, in)
	if rawURL == "" {
		rawURL = toString(in)
	}
	rawURL = resolveMaybeRelative(s.lastURL, rawURL)
	if err := host.ValidateURL(rawURL); err != nil {
		return fmt.Errorf("anibaka: fetch invalid URL %q: %w", rawURL, err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("anibaka: fetch URL: %w", err)
	}
	if len(st.Query) > 0 {
		q := u.Query()
		for k, v := range st.Query {
			q.Set(k, s.renderTemplateWithPipe(v, in))
		}
		u.RawQuery = q.Encode()
		rawURL = u.String()
	}

	method := strings.ToUpper(strings.TrimSpace(st.Method))
	if method == "" {
		method = http.MethodGet
	}

	hdr := s.provider.defaultPlayHeaders()
	for k, v := range st.Headers {
		val := s.renderTemplateWithPipe(v, in)
		if err := host.ValidHeader(k, val); err != nil {
			return fmt.Errorf("anibaka: fetch header: %w", err)
		}
		hdr.Set(k, val)
	}

	var bodyBytes []byte
	bodyStr := s.renderTemplateWithPipe(st.Body, in)
	switch strings.ToLower(strings.TrimSpace(st.BodyType)) {
	case "json":
		bodyBytes = []byte(bodyStr)
		if hdr.Get("Content-Type") == "" {
			hdr.Set("Content-Type", "application/json")
		}
	case "form":
		bodyBytes = []byte(bodyStr)
		if hdr.Get("Content-Type") == "" {
			hdr.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case "raw":
		bodyBytes = []byte(bodyStr)
	default:
		if bodyStr != "" {
			bodyBytes = []byte(bodyStr)
		}
	}

	cookies := true
	if s.provider.Rule.Cookies != nil {
		cookies = *s.provider.Rule.Cookies
	}
	if st.Cookies != nil {
		cookies = *st.Cookies
	}

	resp, err := s.provider.Transport.Do(s.ctx, &host.Request{
		Method:    method,
		URL:       rawURL,
		Header:    hdr,
		Body:      bodyBytes,
		Cookies:   cookies,
		Partition: s.provider.Partition(),
	})
	if err != nil {
		return fmt.Errorf("anibaka: fetch %q: %w", rawURL, err)
	}
	if len(resp.Body) > MaxBodyBytes {
		return fmt.Errorf("anibaka: response body exceeds %d bytes", MaxBodyBytes)
	}
	if resp.FinalURL != "" && host.ValidateURL(resp.FinalURL) == nil {
		s.lastURL = resp.FinalURL
	} else {
		s.lastURL = rawURL
	}
	if st.SaveStatus != "" {
		s.vars[st.SaveStatus] = json.Number(strconv.Itoa(resp.Status))
	}
	if st.SaveFinalURL != "" {
		s.vars[st.SaveFinalURL] = s.lastURL
	}
	if st.SaveHeaders != "" {
		hm := make(map[string]any, len(resp.Header))
		for k, vals := range resp.Header {
			if len(vals) > 0 {
				hm[k] = vals[0]
			}
		}
		s.vars[st.SaveHeaders] = hm
	}
	decoded, err := decodeCharset(resp.Body, st.Charset)
	if err != nil {
		return err
	}
	s.store(st, decoded)
	return nil
}

func (p *Provider) handleSniff(ctx context.Context, s *execState, pageTpl, match string, followIframes bool, timeoutMs int, inline bool) (*video.Sniff, *host.SniffResult, error) {
	page := strings.TrimSpace(s.renderTemplate(pageTpl))
	if page == "" {
		page = strings.TrimSpace(toString(s.vars["episodeId"]))
	}
	page = resolveMaybeRelative(p.Rule.BaseURL, page)
	if err := host.ValidateURL(page); err != nil {
		return nil, nil, fmt.Errorf("anibaka: sniff page URL %q: %w", page, err)
	}
	if timeoutMs <= 0 {
		timeoutMs = 20000
	}
	if inline && p.Sniffer != nil {
		res, err := p.Sniffer.Sniff(ctx, &host.SniffRequest{
			PageURL:       page,
			Referer:       p.Rule.Referer,
			Partition:     p.Partition(),
			Match:         match,
			FollowIframes: followIframes,
			TimeoutMillis: timeoutMs,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("anibaka: sniff: %w", err)
		}
		if res != nil && strings.TrimSpace(res.URL) != "" {
			return nil, res, nil
		}
	}
	return &video.Sniff{
		Page:          page,
		Match:         match,
		FollowIframes: followIframes,
		TimeoutMillis: timeoutMs,
	}, nil, nil
}

// --- template & condition helpers -------------------------------------------

var templateTokenRe = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

func (s *execState) renderAny(v, pipe any) any {
	switch x := v.(type) {
	case string:
		return s.renderTemplateWithPipe(x, pipe)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = s.renderAny(item, pipe)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = s.renderAny(val, pipe)
		}
		return out
	default:
		return v
	}
}

func (s *execState) renderTemplate(tpl string) string {
	return s.renderTemplateWithPipe(tpl, s.pipe)
}

func (s *execState) renderTemplateWithPipe(tpl string, pipe any) string {
	if !strings.Contains(tpl, "{{") {
		return tpl
	}
	return templateTokenRe.ReplaceAllStringFunc(tpl, func(match string) string {
		inner := strings.TrimSpace(match[2 : len(match)-2])
		parts := strings.Split(inner, "|")
		expr := strings.TrimSpace(parts[0])
		val := s.lookupExpr(expr, pipe)
		str := toString(val)
		for _, filt := range parts[1:] {
			switch strings.ToLower(strings.TrimSpace(filt)) {
			case "urlencode":
				str = url.QueryEscape(str)
			case "urldecode":
				if u, err := url.QueryUnescape(str); err == nil {
					str = u
				}
			case "trim":
				str = strings.TrimSpace(str)
			case "lower":
				str = strings.ToLower(str)
			case "upper":
				str = strings.ToUpper(str)
			case "base64":
				str = base64.StdEncoding.EncodeToString([]byte(str))
			case "json":
				if b, err := json.Marshal(val); err == nil {
					str = string(b)
				}
			}
		}
		return str
	})
}

func (s *execState) lookupExpr(expr string, pipe any) any {
	if expr == "value" || expr == "pipe" || expr == "." {
		return pipe
	}
	if v, ok := s.vars[expr]; ok {
		return v
	}
	// Dotted lookup e.g. "item.title" or "item.id".
	if dot := strings.IndexByte(expr, '.'); dot > 0 {
		rootName := expr[:dot]
		rest := expr[dot+1:]
		var root any
		if rootName == "value" || rootName == "pipe" {
			root = pipe
		} else {
			root = s.vars[rootName]
		}
		if vals, err := evalJSONPath(root, "$."+rest); err == nil && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

func (s *execState) resolveOutputValue(spec, defaultVar string, fallbackPipe bool) any {
	if spec != "" {
		if v, ok := s.vars[spec]; ok {
			return v
		}
		if strings.Contains(spec, "{{") {
			return s.renderTemplate(spec)
		}
		if strings.HasPrefix(spec, "$") {
			if vals, err := evalJSONPath(s.pipe, spec); err == nil && len(vals) > 0 {
				if len(vals) == 1 {
					return vals[0]
				}
				return vals
			}
		}
	}
	if defaultVar != "" {
		if v, ok := s.vars[defaultVar]; ok {
			return v
		}
	}
	if fallbackPipe {
		return s.pipe
	}
	return nil
}

func (s *execState) resolveOutputString(spec, defaultVar string) string {
	if spec != "" {
		if strings.Contains(spec, "{{") {
			return s.renderTemplate(spec)
		}
		if v, ok := s.vars[spec]; ok {
			return toString(v)
		}
		return spec
	}
	if defaultVar != "" {
		if v, ok := s.vars[defaultVar]; ok {
			return toString(v)
		}
	}
	return ""
}

func evalCondition(cond string, val any, target string) (bool, error) {
	str := toString(val)
	switch strings.ToLower(strings.TrimSpace(cond)) {
	case "empty":
		return isEmptyValue(val), nil
	case "notempty":
		return !isEmptyValue(val), nil
	case "contains":
		return strings.Contains(str, target), nil
	case "notcontains":
		return !strings.Contains(str, target), nil
	case "equals":
		return str == target, nil
	case "notequals":
		return str != target, nil
	case "startswith":
		return strings.HasPrefix(str, target), nil
	case "endswith":
		return strings.HasSuffix(str, target), nil
	case "regex":
		re, err := regexp.Compile(target)
		if err != nil {
			return false, err
		}
		return re.MatchString(str), nil
	default:
		return false, fmt.Errorf("anibaka: unsupported condition %q", cond)
	}
}

// --- conversion helpers -----------------------------------------------------

func toVideoItems(raw any, baseURL string) []video.Item {
	list := toAnySlice(raw)
	out := make([]video.Item, 0, len(list))
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(toString(firstMapValue(m, "id", "url", "href")))
		title := strings.TrimSpace(toString(firstMapValue(m, "title", "name")))
		if id == "" && title == "" {
			continue
		}
		item := video.Item{
			ID:       id,
			Title:    title,
			Cover:    resolveMaybeRelative(baseURL, strings.TrimSpace(toString(firstMapValue(m, "cover", "image", "poster")))),
			Subtitle: strings.TrimSpace(toString(firstMapValue(m, "subtitle", "remarks", "status"))),
			Tags:     toStringSlice(m["tags"]),
		}
		out = append(out, item)
	}
	return out
}

func toVideoLines(raw any, baseURL string) []video.Line {
	list := toAnySlice(raw)
	if len(list) == 0 {
		return nil
	}
	// Detect if list is already lines ([{name, episodes}]) or flat episodes ([{id, title}]).
	isLines := false
	if first, ok := list[0].(map[string]any); ok {
		if _, hasEps := first["episodes"]; hasEps {
			isLines = true
		}
	}
	if !isLines {
		eps := toVideoEpisodes(list, baseURL)
		if len(eps) == 0 {
			return nil
		}
		return []video.Line{{ID: "line0", Name: "Line 1", Episodes: eps}}
	}
	lines := make([]video.Line, 0, len(list))
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		eps := toVideoEpisodes(toAnySlice(m["episodes"]), baseURL)
		if len(eps) == 0 {
			continue
		}
		lid := strings.TrimSpace(toString(m["id"]))
		if lid == "" {
			lid = fmt.Sprintf("line%d", len(lines))
		}
		name := strings.TrimSpace(toString(firstMapValue(m, "name", "title")))
		if name == "" {
			name = fmt.Sprintf("Line %d", i+1)
		}
		lines = append(lines, video.Line{ID: lid, Name: name, Episodes: eps})
	}
	return lines
}

func toVideoEpisodes(list []any, baseURL string) []video.Episode {
	eps := make([]video.Episode, 0, len(list))
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(toString(firstMapValue(m, "id", "url", "href")))
		if id == "" {
			continue
		}
		if strings.HasPrefix(id, "/") {
			id = resolveMaybeRelative(baseURL, id)
		}
		title := strings.TrimSpace(toString(firstMapValue(m, "title", "name")))
		if title == "" {
			title = fmt.Sprintf("Episode %d", i+1)
		}
		eps = append(eps, video.Episode{ID: id, Title: title})
	}
	return eps
}

func toVideoVariants(raw any, baseURL string) []video.Variant {
	list := toAnySlice(raw)
	out := make([]video.Variant, 0, len(list))
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		u := resolveMaybeRelative(baseURL, strings.TrimSpace(toString(m["url"])))
		if u == "" {
			continue
		}
		q, _ := strconv.Atoi(strings.TrimSpace(toString(m["quality"])))
		out = append(out, video.Variant{
			Name:    strings.TrimSpace(toString(firstMapValue(m, "name", "label"))),
			URL:     u,
			Quality: q,
		})
	}
	return out
}

func firstMapValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && !isEmptyValue(v) {
			return v
		}
	}
	return nil
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case []byte:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func toAnySlice(v any) []any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	default:
		return []any{v}
	}
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	list := toAnySlice(v)
	out := make([]string, 0, len(list))
	for _, it := range list {
		if s := strings.TrimSpace(toString(it)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func resolveMaybeRelative(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		return host.NormalizeURL(raw, base)
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if ref.IsAbs() {
		return raw
	}
	b, err := url.Parse(strings.TrimSpace(base))
	if err != nil || !b.IsAbs() {
		return raw
	}
	return b.ResolveReference(ref).String()
}

func inferFormat(rawURL string) video.Format {
	u, err := url.Parse(rawURL)
	if err != nil {
		return video.FormatAuto
	}
	lower := strings.ToLower(u.Path)
	switch {
	case strings.HasSuffix(lower, ".m3u8"):
		return video.FormatHLS
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".m4v"):
		return video.FormatMP4
	case strings.HasSuffix(lower, ".mpd"):
		return video.FormatDASH
	default:
		return video.FormatAuto
	}
}

func decodeCharset(b []byte, charset string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf8", "utf-8":
		return string(b), nil
	case "gbk", "gb2312":
		out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
		if err != nil {
			return "", fmt.Errorf("anibaka: decode GBK: %w", err)
		}
		return string(out), nil
	case "gb18030":
		out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
		if err != nil {
			return "", fmt.Errorf("anibaka: decode GB18030: %w", err)
		}
		return string(out), nil
	case "big5":
		out, err := traditionalchinese.Big5.NewDecoder().Bytes(b)
		if err != nil {
			return "", fmt.Errorf("anibaka: decode Big5: %w", err)
		}
		return string(out), nil
	default:
		return "", fmt.Errorf("anibaka: unsupported charset %q", charset)
	}
}

var _ video.Provider = (*Provider)(nil)
