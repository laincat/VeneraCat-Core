package js

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A source with one browse of each declared shape and one cursor browse.
const exploreSource = `
class Pages extends ComicSource {
    name = "Pages"
    key = "pages"
    version = "1.0.0"
    explore = [
        { title: "titled map", type: "singlePageWithMultiPart", load: async () => ({
            "热门": [{id: "a1", title: "A"}, {id: "a2", title: "B"}],
            "新作": [{id: "b1", title: "C"}],
            "空": []
        })},
        { title: "numbered", type: "multiPageComicList", load: async (page) => ({
            comics: [{id: "n" + page, title: "N"}], maxPage: 7
        })},
        { title: "parts", type: "multiPartPage", load: async () => ([
            {title: "一组", comics: [{id: "p1", title: "P"}]},
            [{id: "p2", title: "Q"}]
        ])},
        { title: "mixed", type: "mixed", load: async (page) => ({
            data: [{title: "混合", comics: [{id: "m1", title: "M"}]}], maxPage: 3
        })},
        { title: "cursor", type: "multiPageComicList", loadNext: async (next) => ({
            comics: [{id: "c-" + (next ?? "start"), title: "C"}], next: next ? null : "tok-2"
        })}
    ]
    search = {load: async () => ({comics: [], maxPage: 1})}
}`

func exploreRT(t *testing.T) *Runtime {
	t.Helper()
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), exploreSource); err != nil {
		t.Fatal(err)
	}
	return r
}

func firstIDs(res ExploreResult) []string {
	var out []string
	for _, s := range res.Sections {
		for _, c := range s.Comics {
			var comic struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(c, &comic) == nil {
				out = append(out, comic.ID)
			}
		}
	}
	return out
}

func TestExploreShapes(t *testing.T) {
	r := exploreRT(t)
	ctx := context.Background()
	cases := []struct {
		index  int
		want   []string
		titles []string
		pages  int
	}{
		{0, []string{"a1", "a2", "b1"}, []string{"热门", "新作"}, 0},
		{1, []string{"n1"}, nil, 7},
		{2, []string{"p1", "p2"}, []string{"一组", ""}, 0},
		{3, []string{"m1"}, []string{"混合"}, 3},
	}
	for _, c := range cases {
		res, err := r.Explore(ctx, "pages", c.index, 1, "")
		if err != nil {
			t.Fatalf("browse %d: %v", c.index, err)
		}
		if got := firstIDs(res); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("browse %d comics = %v, want %v", c.index, got, c.want)
		}
		if c.titles != nil {
			var titles []string
			for _, s := range res.Sections {
				titles = append(titles, s.Title)
			}
			if strings.Join(titles, ",") != strings.Join(c.titles, ",") {
				t.Errorf("browse %d titles = %v, want %v", c.index, titles, c.titles)
			}
		}
		if res.MaxPage != c.pages {
			t.Errorf("browse %d maxPage = %d, want %d", c.index, res.MaxPage, c.pages)
		}
	}
}

// A browse that only declares loadNext must still load: ehentai declares nothing
// else, and calling load there is what reported "source does not define this
// function".
func TestExploreFallsBackToLoadNext(t *testing.T) {
	r := exploreRT(t)
	ctx := context.Background()
	first, err := r.Explore(ctx, "pages", 4, 1, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := firstIDs(first); len(got) != 1 || got[0] != "c-start" {
		t.Fatalf("first page = %v", got)
	}
	if first.Next != "tok-2" {
		t.Fatalf("next = %q, want tok-2", first.Next)
	}
	second, err := r.Explore(ctx, "pages", 4, 2, first.Next)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := firstIDs(second); len(got) != 1 || got[0] != "c-tok-2" {
		t.Fatalf("second page = %v", got)
	}
	if second.Next != "" {
		t.Errorf("next after the end = %q", second.Next)
	}
}

// The browse index has to be one the source declared.
func TestExploreUnknownIndex(t *testing.T) {
	r := exploreRT(t)
	if _, err := r.Explore(context.Background(), "pages", 9, 1, ""); err == nil {
		t.Fatal("an index past the end was accepted")
	}
}
