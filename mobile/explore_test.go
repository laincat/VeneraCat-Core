package mobile

import (
	"encoding/json"
	"testing"
)

const exploreSource = `
class Pages extends ComicSource {
    name = "Pages"; key = "pages"; version = "1.0.0"
    explore = [
        { title: "numbered", type: "multiPageComicList", load: async (page) => ({
            comics: [{id: "n" + page, title: "N"}], maxPage: 7
        })},
        { title: "cursor", type: "multiPageComicList", loadNext: async (next) => ({
            comics: [{id: "c-" + (next ?? "start"), title: "C"}], next: next ? null : "tok-2"
        })}
    ]
    search = {load: async () => ({comics: [], maxPage: 1})}
}`

func exploreCore(t *testing.T) *Core {
	t.Helper()
	c := newCore(t, &fakeHost{data: map[string]string{}, routes: map[string]string{}})
	if _, err := c.Install(0, "comic", exploreSource, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	return c
}

func idsOf(t *testing.T, raw string) (ids []string, maxPage int, next string) {
	t.Helper()
	var out struct {
		Sections []struct {
			Title  string `json:"title"`
			Comics []struct {
				ID string `json:"id"`
			} `json:"comics"`
		} `json:"sections"`
		MaxPage int    `json:"maxPage"`
		Next    string `json:"next"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("browse is not JSON: %v (%s)", err, raw)
	}
	for _, s := range out.Sections {
		for _, c := range s.Comics {
			ids = append(ids, c.ID)
		}
	}
	return ids, out.MaxPage, out.Next
}

// The phone gets the same normalised browse the desktop does, from the same code.
func TestComicExplore(t *testing.T) {
	c := exploreCore(t)
	raw, err := c.ComicExplore(0, "pages", 0, 2, "")
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	ids, maxPage, next := idsOf(t, raw)
	if len(ids) != 1 || ids[0] != "n2" {
		t.Errorf("comics = %v", ids)
	}
	if maxPage != 7 || next != "" {
		t.Errorf("maxPage = %d next = %q", maxPage, next)
	}
}

// A browse that only declares loadNext is the case that reported "source does not
// define this function": ehentai declares nothing else.
func TestComicExploreCursor(t *testing.T) {
	c := exploreCore(t)
	raw, err := c.ComicExplore(0, "pages", 1, 1, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	ids, _, next := idsOf(t, raw)
	if len(ids) != 1 || ids[0] != "c-start" {
		t.Fatalf("first page = %v", ids)
	}
	if next != "tok-2" {
		t.Fatalf("next = %q", next)
	}
	raw, err = c.ComicExplore(0, "pages", 1, 2, next)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	ids, _, next = idsOf(t, raw)
	if len(ids) != 1 || ids[0] != "c-tok-2" {
		t.Errorf("second page = %v", ids)
	}
	if next != "" {
		t.Errorf("next after the end = %q", next)
	}
}
