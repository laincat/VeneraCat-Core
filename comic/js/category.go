package js

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// CategoryPart is one dimension of a source's category page: 类型, 地区, 状态.
//
// A source declares the values a part offers and, for each of them, the
// parameter its own loader expects. Params may be empty, in which case the
// value is passed to the loader as the parameter itself.
type CategoryPart struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`     // "fixed" or "mixed"
	ItemType   string   `json:"itemType"` // "category" or "author"
	Categories []string `json:"categories"`
	// Params maps Categories by index; a shorter or empty list means the value
	// itself is the parameter.
	Params []string `json:"params,omitempty"`
	// GroupParam is the name a source's loader expects as its parameter when the
	// part's values are searched rather than looked up by id; nhentai needs
	// "language" here, not the chosen value.
	GroupParam string `json:"groupParam,omitempty"`
	// RandomNumber is set on a "random" part: the client picks that many values
	// at random and searches for them together. Such a part is not loaded
	// through categoryComics.
	RandomNumber int `json:"randomNumber,omitempty"`
}

// RankingOption is one entry of a ranking page, e.g. "最近更新".
type RankingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Ranking is a source's ranking page: a flat list of options and a loader that
// takes one of them plus a page number. Nine official sources declare one, so
// ignoring it left a whole surface of theirs unreachable.
type Ranking struct {
	Options []RankingOption `json:"options"`
}

// Category is a source's category page: a title, the parts to choose from, the
// options comic.categoryComics.load takes, and the ranking page when the source
// declares one.
type Category struct {
	Title   string              `json:"title"`
	Parts   []CategoryPart      `json:"parts"`
	Options []SearchOptionGroup `json:"options,omitempty"`
	Ranking *Ranking            `json:"ranking,omitempty"`
}

// Category reads <Base>.sources[key].category. A source without one returns an
// empty Category, which the interface shows as "no category page".
//
// The values are data, not functions, so they cannot be reached through Call;
// the script reads the object and hands back only what JSON can carry.
func (r *Runtime) Category(ctx context.Context, key string) (Category, error) {
	e, err := r.engine(key)
	if err != nil {
		return Category{}, err
	}
	src := "(function(o){" +
		"var c=o&&o.category;if(!c)return null;" +
		// A fixed part lists its values; a dynamic one asks its loader, which
		// may be asynchronous, so every part becomes a promise.
		"var parts=(c.parts||[]).map(function(p){" +
		"function norm(cats,params){return {name:p.name===undefined?'':String(p.name)," +
		"type:p.type===undefined?'':String(p.type),itemType:p.itemType===undefined?'':String(p.itemType)," +
		"categories:cats,params:params,groupParam:p.groupParam===undefined?'':String(p.groupParam)," +
		"randomNumber:Number(p.randomNumber)||0};}" +
		"if((p.type==='dynamic')&&typeof p.loader==='function'){" +
		"return Promise.resolve(p.loader()).then(function(list){" +
		"var cats=[],params=[];" +
		"(list||[]).forEach(function(e){" +
		"if(e===null||e===undefined)return;" +
		"if(typeof e==='string'){cats.push(e);return;}" +
		"cats.push(e.label===undefined?String(e):String(e.label));" +
		"var t=e.target||{};" +
		"params.push(t.param===undefined||t.param===null?'':String(t.param));});" +
		"return norm(cats,params);});}" +
		"var cats=[],params=[];" +
		// A value may be an object of its own: hcomic declares 热门TAG that way,
		// carrying the label and the parameter the loader needs.
		"(p.categories||[]).forEach(function(v){" +
		"if(v!==null&&typeof v==='object'){" +
		"var label=v.label!==undefined?v.label:(v.name!==undefined?v.name:(v.text!==undefined?v.text:v.value));" +
		"cats.push(label===undefined?'':String(label));" +
		"var prm=v.param!==undefined?v.param:(v.value!==undefined?v.value:(v.target&&v.target.param));" +
		"params.push(prm===undefined||prm===null?'':String(prm));" +
		"return;}" +
		"cats.push(String(v))});" +
		"(p.categoryParams||[]).forEach(function(v){params.push(String(v))});" +
		"return norm(cats,params);});" +
		"var opts=[],ol=o.categoryComics&&o.categoryComics.optionList;" +
		"(ol||[]).forEach(function(g){var keys=[],labels=[];" +
		"(g.options||[]).forEach(function(opt){if(typeof opt!=='string'||!opt)return;" +
		"var i=opt.indexOf('-');if(i<0){keys.push(opt);labels.push(opt);return;}" +
		"keys.push(opt.slice(0,i));labels.push(opt.slice(i+1));});" +
		"var dv=[];if(Array.isArray(g['default'])){g['default'].forEach(function(v){dv.push(String(v))});}" +
		// A group without a declared default starts at its first value, the way
		// SearchOptions already behaves; sending an empty string made komiic
		// answer "Variable orderBy has invalid value .".
		"var def=g['default']==null||Array.isArray(g['default'])?'':String(g['default']);" +
		"if(def===''&&keys.length>0&&g.type!=='multi-select'){def=keys[0];}" +
		"opts.push({label:g.label===undefined?'':String(g.label),type:g.type===undefined?'':String(g.type)," +
		"values:keys,labels:labels,\"default\":def,defaultValues:dv});});" +
		// A ranking page is a flat option list plus categoryComics.ranking.load:
		// "date-Recent" is the value and its label, the same "-" the option
		// groups use.
		"var rank=[];var rl=o.categoryComics&&o.categoryComics.ranking;" +
		"((rl&&rl.options)||[]).forEach(function(opt){" +
		"if(typeof opt!=='string'||!opt)return;" +
		"var i=opt.indexOf('-');" +
		"if(i<0){rank.push({value:opt,label:opt});return;}" +
		"rank.push({value:opt.slice(0,i),label:opt.slice(i+1)});});" +
		"return Promise.all(parts).then(function(resolved){" +
		"return {title:c.title===undefined?'':String(c.title),parts:resolved,options:opts," +
		"ranking:rank.length?{options:rank}:undefined};});" +
		"})(" + e.kind.registry() + "[" + strconv.Quote(key) + "])"
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return Category{}, err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return Category{}, nil
	}
	var out Category
	if err := json.Unmarshal(raw, &out); err != nil {
		return Category{}, fmt.Errorf("js: category: %w", err)
	}
	return out, nil
}
