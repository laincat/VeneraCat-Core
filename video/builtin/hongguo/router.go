package hongguo

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var routerStart = regexp.MustCompile(`_ROUTER_DATA\s*=\s*`)

// parseRouterData decodes the window._ROUTER_DATA object embedded in a page.
// Numbers stay json.Number so 64-bit IDs survive.
func parseRouterData(body []byte) map[string]any {
	loc := routerStart.FindIndex(body)
	if loc == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body[loc[1]:]))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		return nil
	}
	return data
}

// loaderPage returns the first non-empty loaderData entry whose key equals
// one of names, then the first (in key order) whose key starts with one.
func loaderPage(data map[string]any, names ...string) map[string]any {
	loader := asMap(data["loaderData"])
	for _, name := range names {
		if m := asMap(loader[name]); len(m) > 0 {
			return m
		}
	}
	keys := make([]string, 0, len(loader))
	for k := range loader {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, name := range names {
			if strings.HasPrefix(k, name) {
				if m := asMap(loader[k]); len(m) > 0 {
					return m
				}
			}
		}
	}
	return nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// asList accepts a JSON array or an object wrapping one under list, items
// or data.
func asList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		for _, k := range []string{"list", "items", "data"} {
			if out := asList(x[k]); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

// str renders a JSON string or number. Other types give "".
func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	}
	return ""
}

// pick returns the first non-empty value of keys, trying each map in order.
func pick(maps []map[string]any, keys ...string) string {
	for _, m := range maps {
		for _, k := range keys {
			if s := str(m[k]); s != "" {
				return s
			}
		}
	}
	return ""
}

func tagsOf(m map[string]any) []string {
	var tags []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(tags, s) {
			tags = append(tags, s)
		}
	}
	switch t := m["tags"].(type) {
	case string:
		for _, s := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == '，' }) {
			add(s)
		}
	default:
		for _, v := range asList(t) {
			add(str(v))
		}
	}
	for _, v := range asList(m["category_list"]) {
		add(str(asMap(v)["name"]))
	}
	if schema, ok := m["category_schema"].(string); ok {
		var cats []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(schema), &cats) == nil {
			for _, c := range cats {
				add(c.Name)
			}
		}
	}
	return tags
}
