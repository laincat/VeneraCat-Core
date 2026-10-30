package js

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/laincat/veneracat-core/video"
)

// VideoProvider adapts a loaded VideoSource plugin to video.Provider.
type VideoProvider struct {
	rt   *Runtime
	meta Meta
}

// VideoProvider returns the provider of a loaded video plugin.
func (r *Runtime) VideoProvider(key string) (*VideoProvider, error) {
	r.smu.Lock()
	m, ok := r.sources[key]
	r.smu.Unlock()
	if !ok {
		return nil, fmt.Errorf("js: no source %q", key)
	}
	if m.Kind != KindVideo {
		return nil, fmt.Errorf("js: %q is a %s source, not a video plugin", key, m.Kind)
	}
	return &VideoProvider{rt: r, meta: m}, nil
}

// Info implements video.Provider.
func (p *VideoProvider) Info() video.Info {
	return video.Info{Key: p.meta.Key, Name: p.meta.Name, Version: p.meta.Version, Kind: "js"}
}

type jsVideo struct {
	ID       any               `json:"id"`
	Title    string            `json:"title"`
	Cover    string            `json:"cover"`
	Subtitle string            `json:"subtitle"`
	Tags     []string          `json:"tags"`
	Extra    map[string]string `json:"extra"`
}

type jsPage struct {
	Videos  []jsVideo    `json:"videos"`
	MaxPage *json.Number `json:"maxPage"`
	Next    any          `json:"next"`
}

// Search implements video.Provider. A plugin with search.load pages by
// number (the cursor is the page number, starting at 1); one with
// search.loadNext passes its own opaque cursor through.
func (p *VideoProvider) Search(ctx context.Context, keyword, cursor string) (*video.Page, error) {
	if ok, _ := p.rt.Has(ctx, p.meta.Key, "search.load"); ok {
		page := 1
		if cursor != "" {
			n, err := strconv.Atoi(cursor)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("js: invalid page cursor %q", cursor)
			}
			page = n
		}
		var out jsPage
		if err := p.call(ctx, &out, "search.load", keyword, []any{}, page); err != nil {
			return nil, err
		}
		res := toPage(out)
		if out.MaxPage != nil {
			if max, err := out.MaxPage.Int64(); err == nil && int64(page) < max {
				res.Next = strconv.Itoa(page + 1)
			}
		}
		return res, nil
	}
	var next any
	if cursor != "" {
		next = cursor
	}
	var out jsPage
	if err := p.call(ctx, &out, "search.loadNext", keyword, []any{}, next); err != nil {
		return nil, err
	}
	res := toPage(out)
	if s := idText(out.Next); s != "" {
		res.Next = s
	}
	return res, nil
}

func toPage(in jsPage) *video.Page {
	out := &video.Page{Items: []video.Item{}}
	for _, v := range in.Videos {
		id := idText(v.ID)
		if id == "" || v.Title == "" {
			continue
		}
		out.Items = append(out.Items, video.Item{ID: id, Title: v.Title, Cover: v.Cover, Subtitle: v.Subtitle, Tags: v.Tags, Extra: v.Extra})
	}
	return out
}

// Details implements video.Provider.
func (p *VideoProvider) Details(ctx context.Context, id string) (*video.Details, error) {
	var out struct {
		Title       string   `json:"title"`
		Cover       string   `json:"cover"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Lines       []struct {
			ID       any    `json:"id"`
			Name     string `json:"name"`
			Episodes []struct {
				ID    any    `json:"id"`
				Title string `json:"title"`
			} `json:"episodes"`
		} `json:"lines"`
	}
	if err := p.call(ctx, &out, "video.loadInfo", id); err != nil {
		return nil, err
	}
	d := &video.Details{ID: id, Title: out.Title, Cover: out.Cover, Description: out.Description, Tags: out.Tags, Lines: []video.Line{}}
	for i, l := range out.Lines {
		line := video.Line{ID: idText(l.ID), Name: l.Name}
		if line.ID == "" {
			line.ID = "line" + strconv.Itoa(i)
		}
		if line.Name == "" {
			line.Name = "线路" + strconv.Itoa(i+1)
		}
		for j, e := range l.Episodes {
			eid := idText(e.ID)
			if eid == "" {
				continue
			}
			title := e.Title
			if title == "" {
				title = "第" + strconv.Itoa(j+1) + "集"
			}
			line.Episodes = append(line.Episodes, video.Episode{ID: eid, Title: title})
		}
		if len(line.Episodes) > 0 {
			d.Lines = append(d.Lines, line)
		}
	}
	return d, nil
}

// Play implements video.Provider.
func (p *VideoProvider) Play(ctx context.Context, itemID, lineID, episodeID string) (*video.Play, error) {
	var out struct {
		URL       string            `json:"url"`
		Format    string            `json:"format"`
		Headers   map[string]string `json:"headers"`
		Variants  []video.Variant   `json:"variants"`
		HLS       *video.HLSOptions `json:"hls"`
		Sniff     *video.Sniff      `json:"sniff"`
		Encrypted bool              `json:"encrypted"`
	}
	if err := p.call(ctx, &out, "video.loadPlay", itemID, lineID, episodeID); err != nil {
		return nil, err
	}
	play := &video.Play{URL: out.URL, Variants: out.Variants, HLS: out.HLS, Sniff: out.Sniff, Encrypted: out.Encrypted}
	switch video.Format(out.Format) {
	case video.FormatHLS, video.FormatMP4, video.FormatDASH:
		play.Format = video.Format(out.Format)
	default:
		play.Format = video.FormatAuto
	}
	if len(out.Headers) > 0 {
		play.Header = http.Header{}
		for k, v := range out.Headers {
			play.Header.Set(k, v)
		}
	}
	if err := play.Validate(); err != nil {
		return nil, fmt.Errorf("js: %s returned an invalid play result: %w", p.meta.Key, err)
	}
	return play, nil
}

func (p *VideoProvider) call(ctx context.Context, out any, path string, args ...any) error {
	raw, err := p.rt.Call(ctx, p.meta.Key, path, args...)
	if err != nil {
		if errors.Is(err, ErrNoFunction) {
			return fmt.Errorf("js: %s does not implement %s", p.meta.Key, path)
		}
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("js: %s.%s returned unexpected data: %w", p.meta.Key, path, err)
	}
	return nil
}

// idText renders a script ID. Numbers arrive as json.Number (the decoder
// uses UseNumber), so integer IDs above 2^53 keep their digits.
func idText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

var _ video.Provider = (*VideoProvider)(nil)
