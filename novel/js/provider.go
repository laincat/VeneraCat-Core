package js

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	comicjs "github.com/laincat/veneracat-core/comic/js"
	"github.com/laincat/veneracat-core/novel"
)

// CallError reports where a NovelSource method call failed.
type CallError struct {
	Key    string
	Method string
	Stack  string
	Err    error
}

func (e *CallError) Error() string {
	loc := firstStackFrame(e.Stack)
	if loc != "" {
		return fmt.Sprintf("novel-js: %s.%s: %v (%s)", e.Key, e.Method, e.Err, loc)
	}
	return fmt.Sprintf("novel-js: %s.%s: %v", e.Key, e.Method, e.Err)
}

func (e *CallError) Unwrap() error { return e.Err }

func firstStackFrame(stack string) string {
	for _, line := range strings.Split(stack, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "at ") {
			return trimmed
		}
	}
	return ""
}

// Provider adapts a loaded NovelSource plugin to novel.Provider.
type Provider struct {
	rt   *comicjs.Runtime
	meta comicjs.Meta
}

// NewProvider returns a Provider for a loaded NovelSource plugin on rt.
func NewProvider(rt *comicjs.Runtime, key string) (*Provider, error) {
	if rt == nil {
		return nil, errors.New("novel-js: nil runtime")
	}
	meta, ok := rt.Source(key)
	if !ok {
		return nil, fmt.Errorf("novel-js: no source %q", key)
	}
	if meta.Kind != comicjs.KindNovel {
		return nil, fmt.Errorf("novel-js: %q is a %s source, not a novel plugin", key, meta.Kind)
	}
	return &Provider{rt: rt, meta: meta}, nil
}

// Info implements novel.Provider.
func (p *Provider) Info() novel.Info {
	return novel.Info{
		Key:         p.meta.Key,
		Name:        p.meta.Name,
		Version:     p.meta.Version,
		Kind:        "js",
		BaseURL:     p.meta.URL,
		Attribution: p.meta.Attribution,
	}
}

type jsNovelItem struct {
	ID     any    `json:"id"`
	Title  string `json:"title"`
	Cover  string `json:"cover"`
	Author string `json:"author"`
}

type jsNovelPage struct {
	Items   []jsNovelItem `json:"items"`
	MaxPage *json.Number  `json:"maxPage"`
}

// Popular implements novel.Provider.
func (p *Provider) Popular(ctx context.Context, page int) (*novel.Page, error) {
	if page < 1 {
		page = 1
	}
	var out jsNovelPage
	if err := p.call(ctx, &out, "popular", page); err != nil {
		return nil, err
	}
	return toPage(out)
}

// Search implements novel.Provider.
func (p *Provider) Search(ctx context.Context, keyword string, page int) (*novel.Page, error) {
	if page < 1 {
		page = 1
	}
	var out jsNovelPage
	if err := p.call(ctx, &out, "search", keyword, page); err != nil {
		return nil, err
	}
	return toPage(out)
}

func toPage(in jsNovelPage) (*novel.Page, error) {
	out := &novel.Page{Items: []novel.Item{}}
	for _, item := range in.Items {
		id := idText(item.ID)
		if id == "" || item.Title == "" {
			continue
		}
		out.Items = append(out.Items, novel.Item{
			ID:     id,
			Title:  item.Title,
			Cover:  item.Cover,
			Author: item.Author,
		})
	}
	if in.MaxPage != nil {
		maxPage, err := in.MaxPage.Int64()
		if err != nil {
			return nil, fmt.Errorf("novel-js: invalid maxPage %q", in.MaxPage.String())
		}
		if maxPage > 0 {
			out.MaxPage = int(maxPage)
		}
	}
	return out, nil
}

// Detail implements novel.Provider.
func (p *Provider) Detail(ctx context.Context, id string) (*novel.Detail, error) {
	var out struct {
		ID       any      `json:"id"`
		Title    string   `json:"title"`
		Cover    string   `json:"cover"`
		Author   string   `json:"author"`
		Summary  string   `json:"summary"`
		Status   string   `json:"status"`
		Tags     []string `json:"tags"`
		Chapters []struct {
			ID    any    `json:"id"`
			Title string `json:"title"`
			Time  string `json:"time"`
		} `json:"chapters"`
	}
	if err := p.call(ctx, &out, "detail", id); err != nil {
		return nil, err
	}
	resolvedID := idText(out.ID)
	if resolvedID == "" {
		resolvedID = id
	}
	detail := &novel.Detail{
		ID:       resolvedID,
		Title:    out.Title,
		Cover:    out.Cover,
		Author:   out.Author,
		Summary:  out.Summary,
		Status:   out.Status,
		Tags:     out.Tags,
		Chapters: []novel.Chapter{},
	}
	for _, ch := range out.Chapters {
		cid := idText(ch.ID)
		if cid == "" {
			continue
		}
		detail.Chapters = append(detail.Chapters, novel.Chapter{
			ID:    cid,
			Title: ch.Title,
			Time:  ch.Time,
		})
	}
	return detail, nil
}

// Chapter implements novel.Provider.
func (p *Provider) Chapter(ctx context.Context, novelID, chapterID string) (*novel.ChapterContent, error) {
	var out struct {
		HTML *string `json:"html"`
	}
	if err := p.call(ctx, &out, "chapter", novelID, chapterID); err != nil {
		return nil, err
	}
	if out.HTML == nil {
		return nil, &CallError{
			Key:    p.meta.Key,
			Method: "chapter",
			Err:    errors.New("result must include html string"),
		}
	}
	return &novel.ChapterContent{HTML: *out.HTML}, nil
}

func (p *Provider) call(ctx context.Context, dst any, method string, args ...any) error {
	raw, err := p.rt.Call(ctx, p.meta.Key, method, args...)
	if err != nil {
		var se *comicjs.ScriptError
		stack := ""
		if errors.As(err, &se) {
			stack = se.Stack
		}
		return &CallError{Key: p.meta.Key, Method: method, Stack: stack, Err: err}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return &CallError{
			Key:    p.meta.Key,
			Method: method,
			Err:    errors.New("result is null or undefined"),
		}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return &CallError{
			Key:    p.meta.Key,
			Method: method,
			Err:    fmt.Errorf("invalid result: %w", err),
		}
	}
	return nil
}

func idText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}
