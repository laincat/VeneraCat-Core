package mobile

import "encoding/json"

import "testing"

const tagSourceForMobile = `
class Tags extends ComicSource {
  name = 'Tags'; key = 'tags'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
  comic = {
    onClickTag: async (namespace, tag) => {
      if (tag === 'broken') throw new Error('no such tag')
      if (namespace === '类型') return {action: 'category', param: 'action'}
      if (tag === 'nothing') return {}
      return {action: 'search', keyword: '作者:' + tag}
    },
  }
}`

func tagsCore(t *testing.T) *Core {
	t.Helper()
	c := newCore(t, &fakeHost{data: map[string]string{}, routes: map[string]string{}})
	if _, err := c.Install(0, "comic", tagSourceForMobile, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	return c
}

// What a source means by a tap travels as the same shape both clients read.
func TestClickTag(t *testing.T) {
	c := tagsCore(t)
	cases := []struct {
		namespace, tag         string
		action, keyword, param string
	}{
		{"作者", "张三", "search", "作者:张三", ""},
		{"类型", "长篇", "category", "", "action"},
		{"作者", "nothing", "", "", ""},
	}
	for _, one := range cases {
		raw, err := c.ClickTag(0, "tags", one.namespace, one.tag)
		if err != nil {
			t.Fatalf("%s/%s: %v", one.namespace, one.tag, err)
		}
		var got TagAction
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if got.Action != one.action || got.Keyword != one.keyword || got.Param != one.param {
			t.Errorf("%s/%s = %+v", one.namespace, one.tag, got)
		}
	}
}

// A source that throws on a tag is reported with its own words: it is the only
// honest answer to "why did nothing happen".
func TestClickTagError(t *testing.T) {
	c := tagsCore(t)
	if _, err := c.ClickTag(0, "tags", "作者", "broken"); err == nil {
		t.Fatal("a throwing onClickTag was swallowed")
	}
	raw, err := c.LogLines()
	if err != nil {
		t.Fatal(err)
	}
	var lines []LogEntry
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range lines {
		if line.Key == "tags" && line.Content != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("the failure left no log line: %s", raw)
	}
}

// A source with no onClickTag has no opinion rather than an error.
func TestClickTagWithoutHandler(t *testing.T) {
	c := newCore(t, &fakeHost{data: map[string]string{}, routes: map[string]string{}})
	if _, err := c.Install(0, "comic", `class Quiet extends ComicSource {
  name = 'Quiet'; key = 'quiet'; version = '1'
  search = {load: async () => ({comics: [], maxPage: 1})}
}`, "file", "", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := c.ClickTag(0, "quiet", "作者", "张三")
	if err != nil {
		t.Fatalf("click: %v", err)
	}
	if raw != `{"action":"","keyword":"","param":""}` {
		t.Errorf("raw = %s", raw)
	}
}
