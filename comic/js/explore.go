package js

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// Loading one browse page.
//
// A source declares its browses in `explore`, and each entry pages in one of two
// ways: `load(page)` with a page number, or `loadNext(next)` with a cursor the
// source hands back. ehentai declares only the second kind, so a client that
// always calls `load` reports "source does not define this function" for a
// browse that exists.
//
// The four declared shapes are normalised here, once, so that both clients read
// a browse the same way:
//
//	singlePageWithMultiPart  {partTitle: [comic, ...]}
//	multiPartPage            [{title, comics}, ...] or [[comic, ...], ...]
//	multiPageComicList       {comics, maxPage} or {comics, next}
//	mixed                    {data: [{title, comics}, ...], maxPage}

// ExploreSection is one titled group of comics in a browse.
type ExploreSection struct {
	Title string
	// Comics stay as the source wrote them; the client knows its own comic shape
	// and parsing here would mean serialising every comic twice.
	Comics []json.RawMessage
}

// ExploreResult is what one browse page returned.
type ExploreResult struct {
	Sections []ExploreSection
	// MaxPage is the page count of a numbered browse; 0 when unknown.
	MaxPage int
	// Next is the cursor of a cursor browse; empty means there is no more.
	Next string
}

// Explore loads one browse page. cursor is the token the previous page returned
// ("" for the first); page is 1-based and only used by numbered browses.
func (r *Runtime) Explore(ctx context.Context, key string, index, page int, cursor string) (ExploreResult, error) {
	if page < 1 {
		page = 1
	}
	pages, err := r.ExplorePages(ctx, key)
	if err != nil {
		return ExploreResult{}, err
	}
	if index < 0 || index >= len(pages) {
		return ExploreResult{}, fmt.Errorf("js: no browse %d (the source has %d)", index, len(pages))
	}
	kind := pages[index].Type
	load := fmt.Sprintf("explore[%d].load", index)
	next := fmt.Sprintf("explore[%d].loadNext", index)

	if ok, err := r.Has(ctx, key, load); err != nil {
		return ExploreResult{}, err
	} else if !ok {
		return r.exploreCursor(ctx, key, next, kind, cursor)
	}

	switch kind {
	case "multiPageComicList":
		raw, err := r.Call(ctx, key, load, page)
		if err != nil {
			return ExploreResult{}, err
		}
		return exploreList(raw)
	case "multiPartPage":
		raw, err := r.Call(ctx, key, load)
		if err != nil {
			return ExploreResult{}, err
		}
		return exploreParts(raw, "")
	case "mixed":
		raw, err := r.Call(ctx, key, load, page)
		if err != nil {
			return ExploreResult{}, err
		}
		res, err := exploreParts(raw, "data")
		if err != nil {
			return res, err
		}
		var env struct {
			MaxPage json.Number `json:"maxPage"`
		}
		if json.Unmarshal(raw, &env) == nil {
			if n, err := env.MaxPage.Int64(); err == nil {
				res.MaxPage = int(n)
			}
		}
		return res, nil
	default: // singlePageWithMultiPart
		raw, err := r.Call(ctx, key, load)
		if err != nil {
			return ExploreResult{}, err
		}
		return exploreTitledMap(raw)
	}
}

// exploreCursor calls a browse that pages by token. The source receives null on
// the first page, which is what its own `next ?? default` expects.
func (r *Runtime) exploreCursor(ctx context.Context, key, path, kind, cursor string) (ExploreResult, error) {
	ok, err := r.Has(ctx, key, path)
	if err != nil {
		return ExploreResult{}, err
	}
	if !ok {
		return ExploreResult{}, fmt.Errorf("js: source defines neither load nor loadNext for this browse")
	}
	var arg any
	if cursor != "" {
		arg = cursor
	}
	raw, err := r.Call(ctx, key, path, arg)
	if err != nil {
		return ExploreResult{}, err
	}
	// A cursor browse answers like a page: {comics, next}. A multi-part one may
	// answer with parts instead, which is normalised the same way as the rest.
	if kind == "multiPartPage" {
		if res, err := exploreParts(raw, ""); err == nil && len(res.Sections) > 0 {
			return res, nil
		}
	}
	return exploreList(raw)
}

// exploreList reads {comics, maxPage, next}.
func exploreList(raw json.RawMessage) (ExploreResult, error) {
	var env struct {
		Comics  []json.RawMessage `json:"comics"`
		MaxPage json.Number       `json:"maxPage"`
		Next    json.RawMessage   `json:"next"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ExploreResult{}, fmt.Errorf("js: browse: %w", err)
	}
	out := ExploreResult{Next: cursorOf(env.Next)}
	if n, err := env.MaxPage.Int64(); err == nil {
		out.MaxPage = int(n)
	}
	if len(env.Comics) > 0 {
		out.Sections = []ExploreSection{{Comics: env.Comics}}
	}
	return out, nil
}

// cursorOf reads a next token, which sources write as a string, a number or null.
func cursorOf(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// exploreTitledMap reads {partTitle: [comic, ...]}.
func exploreTitledMap(raw json.RawMessage) (ExploreResult, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return ExploreResult{}, err
	}
	var out ExploreResult
	for _, f := range fields {
		var comics []json.RawMessage
		if json.Unmarshal(f.raw, &comics) != nil || len(comics) == 0 {
			continue
		}
		out.Sections = append(out.Sections, ExploreSection{Title: f.name, Comics: comics})
	}
	return out, nil
}

// exploreParts reads [{title, comics}, ...] or [[comic, ...], ...], optionally
// under a field name.
func exploreParts(raw json.RawMessage, field string) (ExploreResult, error) {
	body := raw
	if field != "" {
		fields, err := objectFields(raw)
		if err != nil {
			return ExploreResult{}, err
		}
		body = nil
		for _, f := range fields {
			if f.name == field {
				body = f.raw
				break
			}
		}
		if body == nil {
			return ExploreResult{}, nil
		}
	}
	var list []json.RawMessage
	if err := json.Unmarshal(body, &list); err != nil {
		return ExploreResult{}, nil
	}
	var out ExploreResult
	for _, item := range list {
		var part struct {
			Title  json.RawMessage   `json:"title"`
			Comics []json.RawMessage `json:"comics"`
		}
		if json.Unmarshal(item, &part) == nil && len(part.Comics) > 0 {
			out.Sections = append(out.Sections, ExploreSection{
				Title:  stringOf(part.Title),
				Comics: part.Comics,
			})
			continue
		}
		// A bare list of comics: a part without a title.
		var comics []json.RawMessage
		if json.Unmarshal(item, &comics) == nil && len(comics) > 0 {
			out.Sections = append(out.Sections, ExploreSection{Comics: comics})
		}
	}
	return out, nil
}

// namedField is one field of a JSON object, in the order the source wrote it.
type namedField struct {
	name string
	raw  json.RawMessage
}

// objectFields keeps the source's field order: a browse's parts are drawn in it.
func objectFields(raw json.RawMessage) ([]namedField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil
	}
	var out []namedField
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, _ := key.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, namedField{name: name, raw: value})
	}
	return out, nil
}

// stringOf reads a title that a source may write as a string or a number.
func stringOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
