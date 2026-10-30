package js

import (
	"context"
	"testing"
)

// "-<none>" means an empty value with the text "<none>": the option format is
// "value-text" and the left side may be empty. Reading the whole string as the
// value sends a truthy "-<none>" to a source that checks whether a filter was
// chosen, which builds a broken address.
func TestAnOptionWithAnEmptyValue(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class M extends ComicSource {
  name = 'M'; key = 'm'; version = '1'
  search = {
    optionList: [
      {label: '星级', type: 'dropdown', options: ['-<none>', '4-4', '5-5']},
    ],
    load: async (k, o, p) => ({comics: []}),
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	groups, err := r.SearchOptions(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	g := groups[0]
	if len(g.Values) != 3 || g.Values[0] != "" {
		t.Errorf("values = %q, want an empty first value", g.Values)
	}
	if g.Labels[0] != "<none>" {
		t.Errorf("labels = %q, want <none> first", g.Labels)
	}
	// The empty value is the default, so a search that chooses nothing sends
	// nothing rather than a literal "-<none>".
	if g.Default != "" {
		t.Errorf("default = %q, want empty", g.Default)
	}
}

// A multi-select group's value is a JSON array of the chosen keys, so the
// group has to expose the list rather than one key.
func TestMultiSelectDefaultValues(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class M extends ComicSource {
  name = 'M'; key = 'm'; version = '1'
  search = {
    optionList: [
      {label: '分类', type: 'multi-select', options: ['0-杂谈', '1-同人', '2-漫画'],
       default: ['0', '2']},
      {label: '无默认', type: 'multi-select', options: ['a-A', 'b-B']},
    ],
    load: async (k, o, p) => ({comics: [], options: o}),
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	groups, err := r.SearchOptions(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if g := groups[0]; len(g.DefaultValues) != 2 || g.DefaultValues[0] != "0" || g.DefaultValues[1] != "2" {
		t.Errorf("group 0 default values = %q", g.DefaultValues)
	}
	// A multi-select group never reports one key as its default: the two
	// shapes are not interchangeable.
	if g := groups[0]; g.Default != "" {
		t.Errorf("group 0 default = %q, want empty for a multi-select", g.Default)
	}
	if g := groups[1]; len(g.DefaultValues) != 0 {
		t.Errorf("group 1 default values = %q, want none", g.DefaultValues)
	}
}
