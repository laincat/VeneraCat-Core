package gohost

import (
	"encoding/json"
	"testing"
)

// Sources return tags and chapters as JS objects whose key order is reading
// order, so the parser must not sort or shuffle them.
func TestParseDetailsKeepsOrder(t *testing.T) {
	raw := json.RawMessage(`{
		"title":"漫画","subtitle":"副标题","cover":"https://c/1.jpg","description":"说明",
		"tags":{"作者":["甲","乙"],"标签":["b","a"],"坏":"不是数组"},
		"chapters":{"10":"第十话","2":"第二话"}
	}`)
	d, err := parseDetails(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "漫画" || d.Subtitle != "副标题" || d.Cover != "https://c/1.jpg" {
		t.Fatalf("details = %+v", d)
	}
	var ids []string
	for _, c := range d.Chapters {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || ids[0] != "10" || ids[1] != "2" {
		t.Fatalf("chapter order = %v, want [10 2]", ids)
	}
	if len(d.Tags) != 2 || d.Tags[0].Name != "作者" || len(d.Tags[0].Values) != 2 || d.Tags[1].Name != "标签" {
		t.Fatalf("tags = %+v", d.Tags)
	}
}

func TestParseDetailsGroupedChapters(t *testing.T) {
	d, err := parseDetails(json.RawMessage(`{"title":"x","chapters":{"正篇":{"c2":"二","c1":"一"},"番外":{"s1":"外"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Chapter{{ID: "c2", Title: "二", Group: "正篇"}, {ID: "c1", Title: "一", Group: "正篇"}, {ID: "s1", Title: "外", Group: "番外"}}
	if len(d.Chapters) != len(want) {
		t.Fatalf("chapters = %+v", d.Chapters)
	}
	for i, c := range want {
		if d.Chapters[i] != c {
			t.Errorf("chapter %d = %+v, want %+v", i, d.Chapters[i], c)
		}
	}
	if d.Chapters[0].Title != "二" {
		t.Errorf("title = %q", d.Chapters[0].Title)
	}
}

func TestParsePage(t *testing.T) {
	p, err := parsePage(json.RawMessage(`{
		"comics":[
			{"id":1,"title":"甲","cover":"c1"},
			{"title":"没有 id"},
			{"id":"b","title":"乙","subTitle":"小标题","tags":["t"]},
			{"id":"b","title":"重复"}
		],
		"maxPage":3
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxPage != 3 {
		t.Errorf("maxPage = %d", p.MaxPage)
	}
	if len(p.Comics) != 2 {
		t.Fatalf("comics = %+v", p.Comics)
	}
	if p.Comics[0].ID != "1" || p.Comics[0].Title != "甲" {
		t.Errorf("first = %+v", p.Comics[0])
	}
	if p.Comics[1].Subtitle != "小标题" || len(p.Comics[1].Tags) != 1 {
		t.Errorf("second = %+v", p.Comics[1])
	}
}

func TestParsePageToleratesMissingComics(t *testing.T) {
	p, err := parsePage(json.RawMessage(`{"next":"cursor-2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Comics) != 0 || p.Next != "cursor-2" {
		t.Fatalf("page = %+v", p)
	}
}

func TestObjectFieldsHandlesBadInput(t *testing.T) {
	for _, in := range []string{"", "null", "[]", "{", `{"a"}`} {
		if got := objectFields(json.RawMessage(in)); got != nil && len(got) != 0 {
			t.Errorf("%q: %v", in, got)
		}
	}
}
