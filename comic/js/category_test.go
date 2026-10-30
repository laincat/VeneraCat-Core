package js

import (
	"context"
	"testing"
)

const categorySource = `class Cat extends ComicSource {
  name = 'Cat'; key = 'cat'; version = '1'
  category = {
    title: '漫画柜',
    parts: [
      {name: '类型', type: 'fixed', itemType: 'category', categories: ['全部', '热血'], categoryParams: ['', 'rexue']},
      {name: '地区', type: 'fixed', itemType: 'category', categories: ['全部', '日本']},
    ],
  }
  categoryComics = {
    optionList: [{label: '排序', type: 'select', options: ['-默认', 'new-最新']}],
    load: async (category, param, options, page) => ({comics: [], maxPage: 1}),
  }
}`

func TestCategoryReadsPartsAndOptions(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), categorySource); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "漫画柜" || len(got.Parts) != 2 {
		t.Fatalf("category = %+v", got)
	}
	first := got.Parts[0]
	if first.Name != "类型" || first.Type != "fixed" || first.ItemType != "category" {
		t.Errorf("first part = %+v", first)
	}
	if len(first.Categories) != 2 || first.Categories[1] != "热血" {
		t.Errorf("categories = %v", first.Categories)
	}
	// The parameter list lines up with the values, including an empty one.
	if len(first.Params) != 2 || first.Params[1] != "rexue" || first.Params[0] != "" {
		t.Errorf("params = %v", first.Params)
	}
	// A part without categoryParams has no params, and the interface then passes
	// the chosen value itself.
	if len(got.Parts[1].Params) != 0 {
		t.Errorf("second part params = %v, want none", got.Parts[1].Params)
	}
	if len(got.Options) != 1 || got.Options[0].Label != "排序" {
		t.Fatalf("options = %+v", got.Options)
	}
	if len(got.Options[0].Values) != 2 || got.Options[0].Values[0] != "" {
		t.Errorf("option values = %v, the first is the empty default", got.Options[0].Values)
	}
}

func TestCategoryWithoutOneIsEmpty(t *testing.T) {
	r := newRT(t, Options{}, nil)
	if _, err := r.LoadSource(context.Background(), `class N extends ComicSource { name='N'; key='n'; version='1' }`); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "" || len(got.Parts) != 0 {
		t.Errorf("a source without a category page answered %+v", got)
	}
}

// A dynamic part has no values until its loader runs, and the loader may be
// asynchronous. manga_dex builds its Tags part that way from a tag map.
func TestCategoryLoadsDynamicParts(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class Dyn extends ComicSource {
  name = 'Dyn'; key = 'dyn'; version = '1'
  tags = {romance: '111', action: '222'}
  category = {
    title: 'Dynamic',
    parts: [
      {name: 'Tags', type: 'dynamic', loader: async () => Object.keys(this.tags).map(k => ({
        label: k, target: {action: 'category', keyword: k, param: this.tags[k]},
      }))},
      {name: 'Sort', type: 'fixed', categories: ['最新'], categoryParams: ['new']},
    ],
  }
  categoryComics = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "dyn")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Parts) != 2 {
		t.Fatalf("parts = %+v", got.Parts)
	}
	dyn := got.Parts[0]
	if len(dyn.Categories) != 2 || dyn.Categories[0] != "romance" {
		t.Errorf("dynamic categories = %v", dyn.Categories)
	}
	// The parameter comes from the entry's target, which is what the loader
	// needs; a plain string entry has no parameter and falls back to its label.
	if len(dyn.Params) != 2 || dyn.Params[1] != "222" {
		t.Errorf("dynamic params = %v", dyn.Params)
	}
	if got.Parts[1].Categories[0] != "最新" {
		t.Errorf("fixed part lost: %+v", got.Parts[1])
	}
}

// A "random" part is not loaded through categoryComics: the client picks that
// many values and searches for them. nhentai declares its Tags that way.
func TestCategoryKeepsARandomPart(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class Rnd extends ComicSource {
  name = 'Rnd'; key = 'rnd'; version = '1'
  category = {
    title: 'Random',
    parts: [{name: 'Tags', type: 'random', randomNumber: 20, itemType: 'search',
             categories: ['a', 'b', 'c']}],
  }
  categoryComics = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "rnd")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Parts) != 1 {
		t.Fatalf("parts = %+v", got.Parts)
	}
	part := got.Parts[0]
	if part.Type != "random" || part.RandomNumber != 20 || part.ItemType != "search" {
		t.Errorf("part = %+v", part)
	}
	if len(part.Categories) != 3 {
		t.Errorf("categories = %v", part.Categories)
	}
}

// nhentai's Language part declares groupParam "language": the loader wants that
// name as its parameter, not the chosen value.
func TestCategoryKeepsAGroupParam(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class G extends ComicSource {
  name='G'; key='g'; version='1'
  category = {title: 'G', parts: [
    {name: 'Language', type: 'fixed', categories: ['Chinese', 'English'], itemType: 'category', groupParam: 'language'},
  ]}
  categoryComics = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Parts) != 1 || got.Parts[0].GroupParam != "language" {
		t.Fatalf("parts = %+v", got.Parts)
	}
	// No per-value parameters: the group parameter is what the loader gets.
	if len(got.Parts[0].Params) != 0 {
		t.Errorf("params = %v", got.Parts[0].Params)
	}
}

// A value may be an object carrying its own label and parameter: hcomic
// declares its 热门TAG part that way, and stringifying it gave "[object Object]".
func TestCategoryReadsObjectValues(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class Obj extends ComicSource {
  name='Obj'; key='obj'; version='1'
  category = {title: 'Obj', parts: [
    {name: '热门TAG', type: 'fixed', itemType: 'category',
     categories: [{label: '恋爱', param: 'love'}, {name: '战斗', value: 'fight'}]},
  ]}
  categoryComics = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "obj")
	if err != nil {
		t.Fatal(err)
	}
	part := got.Parts[0]
	if len(part.Categories) != 2 || part.Categories[0] != "恋爱" || part.Categories[1] != "战斗" {
		t.Fatalf("categories = %v", part.Categories)
	}
	if len(part.Params) != 2 || part.Params[0] != "love" || part.Params[1] != "fight" {
		t.Errorf("params = %v", part.Params)
	}
}

// An option group without a declared default starts at its first value, as a
// search's options do: komiic answered "Variable orderBy has invalid value ."
// to the empty string.
func TestCategoryOptionsFallBackToTheFirstValue(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class O extends ComicSource {
  name='O'; key='o'; version='1'
  category = {title: 'O', parts: [{name: 'P', type: 'fixed', categories: ['a']}]}
  categoryComics = {
    optionList: [{label: '排序', type: 'select', options: ['updated-最近更新', 'hot-热门']}],
    load: async () => ({comics: [], maxPage: 1}),
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "o")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Options) != 1 || got.Options[0].Default != "updated" {
		t.Fatalf("options = %+v", got.Options)
	}
}

// A ranking page is a flat option list plus its own loader: nhentai declares
// five sort options and loads them with categoryComics.ranking.load. Ignoring
// the block left that whole surface unreachable.
func TestCategoryReportsARankingPage(t *testing.T) {
	r := newRT(t, Options{}, nil)
	src := `class R extends ComicSource {
  name='R'; key='r'; version='1'
  category = {title: 'R', parts: [{name: 'P', type: 'fixed', categories: ['a']}]}
  categoryComics = {
    ranking: {
      options: ['date-Recent', 'today-Popular Today'],
      load: async (option, page) => ({comics: [], maxPage: 3}),
    },
    load: async () => ({comics: [], maxPage: 1}),
  }
}`
	if _, err := r.LoadSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	got, err := r.Category(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ranking == nil || len(got.Ranking.Options) != 2 {
		t.Fatalf("ranking = %+v", got.Ranking)
	}
	if got.Ranking.Options[0] != (RankingOption{Value: "date", Label: "Recent"}) {
		t.Errorf("option 0 = %+v", got.Ranking.Options[0])
	}
	if got.Ranking.Options[1] != (RankingOption{Value: "today", Label: "Popular Today"}) {
		t.Errorf("option 1 = %+v", got.Ranking.Options[1])
	}

	// A source without one reports nothing rather than an empty page.
	src2 := `class N extends ComicSource {
  name='N'; key='n'; version='1'
  category = {title: 'N', parts: [{name: 'P', type: 'fixed', categories: ['a']}]}
  categoryComics = {load: async () => ({comics: [], maxPage: 1})}
}`
	if _, err := r.LoadSource(context.Background(), src2); err != nil {
		t.Fatal(err)
	}
	plain, err := r.Category(context.Background(), "n")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Ranking != nil {
		t.Errorf("ranking = %+v, want none", plain.Ranking)
	}
}
