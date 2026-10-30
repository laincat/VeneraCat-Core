package kazumi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"golang.org/x/net/html"
)

// --- restricted JSONPath --------------------------------------------------
//
// Kazumi API rules accept a small JSONPath subset: "$" followed by ".name",
// "[index]", "[*]" or "['name']" / ["name"] segments. Anything else (filters,
// recursive descent, slices) is rejected at import time.

type segment struct {
	key      string
	index    int
	wildcard bool
	isIndex  bool
}

type path []segment

func compilePath(expr string) (path, error) {
	if !strings.HasPrefix(expr, "$") {
		return nil, fmt.Errorf("JSONPath must start with $: %q", expr)
	}
	var out path
	i := 1
	for i < len(expr) {
		switch expr[i] {
		case '.':
			i++
			start := i
			for i < len(expr) && isNameByte(expr[i]) {
				i++
			}
			if i == start {
				return nil, fmt.Errorf("unsupported JSONPath: %q", expr)
			}
			out = append(out, segment{key: expr[start:i]})
		case '[':
			end, err := bracketEnd(expr, i)
			if err != nil {
				return nil, err
			}
			content := strings.TrimSpace(expr[i+1 : end])
			switch {
			case content == "*":
				out = append(out, segment{wildcard: true})
			case isDigits(content):
				n, _ := strconv.Atoi(content)
				out = append(out, segment{index: n, isIndex: true})
			case len(content) >= 2 && (content[0] == '\'' && content[len(content)-1] == '\'' ||
				content[0] == '"' && content[len(content)-1] == '"'):
				out = append(out, segment{key: unescapeQuoted(content[1 : len(content)-1])})
			default:
				return nil, fmt.Errorf("unsupported JSONPath segment [%s] in %q", content, expr)
			}
			i = end + 1
		default:
			return nil, fmt.Errorf("unsupported JSONPath: %q", expr)
		}
	}
	return out, nil
}

func isNameByte(b byte) bool {
	return b == '_' || b == '$' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func unescapeQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func bracketEnd(expr string, start int) (int, error) {
	var quote byte
	escaped := false
	for i := start + 1; i < len(expr); i++ {
		c := expr[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ']':
			return i, nil
		}
	}
	return 0, fmt.Errorf("JSONPath missing ]: %q", expr)
}

// read returns every value the path selects. JSON numbers stay json.Number
// so large IDs are not rounded through float64.
func (p path) read(doc any) []any {
	cur := []any{doc}
	for _, s := range p {
		var next []any
		for _, v := range cur {
			switch x := v.(type) {
			case map[string]any:
				if s.wildcard {
					// JSON objects are unordered and Go randomises map
					// iteration, so visit keys in sorted order to make the
					// result reproducible.
					keys := make([]string, 0, len(x))
					for k := range x {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						next = append(next, x[k])
					}
				} else if !s.isIndex {
					if item, ok := x[s.key]; ok {
						next = append(next, item)
					}
				}
			case []any:
				switch {
				case s.wildcard:
					next = append(next, x...)
				case s.isIndex:
					if s.index < len(x) {
						next = append(next, x[s.index])
					}
				}
			}
		}
		cur = next
	}
	return cur
}

func readFirst(doc any, expr string) (any, error) {
	p, err := compilePath(expr)
	if err != nil {
		return nil, err
	}
	vs := p.read(doc)
	if len(vs) == 0 {
		return nil, nil
	}
	return vs[0], nil
}

func readAll(doc any, expr string) ([]any, error) {
	p, err := compilePath(expr)
	if err != nil {
		return nil, err
	}
	return p.read(doc), nil
}

func decodeJSON(body []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("API response is not valid JSON: %w", err)
	}
	return v, nil
}

// stringValue renders a JSON scalar as Kazumi does: strings are trimmed,
// numbers and booleans use their text, null and containers become "" and
// the JSON text respectively.
func stringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// --- XPath ----------------------------------------------------------------

func checkXPath(field, expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if _, err := compileXPath(expr); err != nil {
		return fmt.Errorf("%s: invalid XPath %q: %w", field, expr, err)
	}
	return nil
}

// attrOp matches Kazumi's CSS-like attribute operators, which are not XPath:
// @a*='v' (contains), @a^='v' (starts with), @a$='v' (ends with) and
// @a~='v' (space-separated word). The left side may also be text().
var attrOp = regexp.MustCompile(`(@[A-Za-z_][\w:.-]*|text\(\s*\))\s*([*^$~])=\s*('[^']*'|"[^"]*")`)

// compileXPath compiles a rule expression. It first rewrites the dialect
// Kazumi's selector library accepts into standard XPath 1.0: the attribute
// operators above, and "/" or "//" alone (which Kazumi evaluates to the
// context node itself).
func compileXPath(expr string) (*xpath.Expr, error) {
	return xpath.Compile(rewriteXPath(expr))
}

func rewriteXPath(expr string) string {
	if t := strings.TrimSpace(expr); t == "/" || t == "//" {
		return "."
	}
	if !strings.ContainsAny(expr, "*^$~") {
		return expr
	}
	quoted := quotedSpans(expr)
	var b strings.Builder
	last := 0
	for _, m := range attrOp.FindAllStringSubmatchIndex(expr, -1) {
		if inSpan(quoted, m[0]) {
			continue
		}
		left, op, lit := expr[m[2]:m[3]], expr[m[4]:m[5]], expr[m[6]:m[7]]
		var repl string
		switch op {
		case "*":
			repl = "contains(" + left + "," + lit + ")"
		case "^":
			repl = "starts-with(" + left + "," + lit + ")"
		case "$":
			repl = "ends-with(" + left + "," + lit + ")"
		default: // "~": the value is one of the space-separated words
			word := lit[1 : len(lit)-1]
			if strings.ContainsAny(word, " \t\r\n") {
				repl = "false()"
			} else {
				repl = "contains(concat(' ', normalize-space(" + left + "), ' '), " + lit[:1] + " " + word + " " + lit[:1] + ")"
			}
		}
		b.WriteString(expr[last:m[0]])
		b.WriteString(repl)
		last = m[1]
	}
	b.WriteString(expr[last:])
	return b.String()
}

// quotedSpans returns the [start,end) byte ranges of quoted literals.
func quotedSpans(s string) [][2]int {
	var spans [][2]int
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				spans = append(spans, [2]int{start, i + 1})
				quote = 0
			}
		case c == '\'' || c == '"':
			quote, start = c, i
		}
	}
	return spans
}

func inSpan(spans [][2]int, pos int) bool {
	for _, sp := range spans {
		if pos >= sp[0] && pos < sp[1] {
			return true
		}
	}
	return false
}

func parseHTML(body []byte) (*html.Node, error) {
	doc, err := htmlquery.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("HTML response cannot be parsed: %w", err)
	}
	return doc, nil
}

// queryAll evaluates expr against n. Kazumi's selector library treats a
// leading "//" as relative to the context node when called on a child
// element, so "//h3/a" inside a search item finds that item's link, not the
// first link of the page. Plain XPath semantics would search the whole
// document. We rewrite a leading "//" (and "/") to ".//" for non-document
// contexts to keep published rules working.
func queryAll(n *html.Node, expr string) ([]*html.Node, error) {
	q, err := compileXPath(relativeTo(n, expr))
	if err != nil {
		return nil, err
	}
	return htmlquery.QuerySelectorAll(n, q), nil
}

func queryOne(n *html.Node, expr string) (*html.Node, error) {
	nodes, err := queryAll(n, expr)
	if err != nil || len(nodes) == 0 {
		return nil, err
	}
	return nodes[0], nil
}

func relativeTo(n *html.Node, expr string) string {
	if n == nil || n.Type == html.DocumentNode {
		return expr
	}
	t := strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(t, "//"):
		return "." + t
	case strings.HasPrefix(t, "/"):
		return "." + t
	}
	return t
}

func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	return htmlquery.InnerText(n)
}

func attr(n *html.Node, name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}
