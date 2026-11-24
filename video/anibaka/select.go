package anibaka

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"
)

func evalSelect(htmlText, mode, selector, attr string, all bool, fields map[string]SelectField) (any, error) {
	doc, err := html.Parse(strings.NewReader(htmlText))
	if err != nil {
		return nil, fmt.Errorf("anibaka: parse HTML: %w", err)
	}
	nodes, err := queryNodes(doc, mode, selector)
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		if all {
			out := make([]any, 0, len(nodes))
			for _, n := range nodes {
				obj, err := extractFields(n, mode, fields)
				if err != nil {
					return nil, err
				}
				out = append(out, obj)
			}
			return out, nil
		}
		if len(nodes) == 0 {
			return map[string]any{}, nil
		}
		return extractFields(nodes[0], mode, fields)
	}
	if all {
		out := make([]any, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, readNodeValue(n, attr))
		}
		return out, nil
	}
	if len(nodes) == 0 {
		return "", nil
	}
	return readNodeValue(nodes[0], attr), nil
}

func extractFields(root *html.Node, defaultMode string, fields map[string]SelectField) (map[string]any, error) {
	obj := make(map[string]any, len(fields))
	for name, sf := range fields {
		fmode := strings.ToLower(strings.TrimSpace(sf.Mode))
		if fmode == "" {
			fmode = defaultMode
		}
		if strings.TrimSpace(sf.Selector) == "" || sf.Selector == "." {
			v := readNodeValue(root, sf.Attr)
			if v == "" && sf.Default != "" {
				v = sf.Default
			}
			obj[name] = v
			continue
		}
		matched, err := queryNodes(root, fmode, sf.Selector)
		if err != nil {
			return nil, fmt.Errorf("anibaka: field %q: %w", name, err)
		}
		if sf.All {
			vals := make([]any, 0, len(matched))
			for _, m := range matched {
				vals = append(vals, readNodeValue(m, sf.Attr))
			}
			obj[name] = vals
			continue
		}
		if len(matched) == 0 {
			obj[name] = sf.Default
			continue
		}
		v := readNodeValue(matched[0], sf.Attr)
		if v == "" && sf.Default != "" {
			v = sf.Default
		}
		obj[name] = v
	}
	return obj, nil
}

func queryNodes(root *html.Node, mode, selector string) ([]*html.Node, error) {
	if mode == "xpath" {
		nodes, err := htmlquery.QueryAll(root, selector)
		if err != nil {
			return nil, fmt.Errorf("anibaka: xpath %q: %w", selector, err)
		}
		return nodes, nil
	}
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return nil, fmt.Errorf("anibaka: css %q: %w", selector, err)
	}
	return sel.MatchAll(root), nil
}

func readNodeValue(n *html.Node, attr string) string {
	if n == nil {
		return ""
	}
	a := strings.TrimSpace(attr)
	switch strings.ToLower(a) {
	case "", "text":
		return strings.TrimSpace(htmlquery.InnerText(n))
	case "html", "innerhtml":
		return renderInnerHTML(n)
	case "outerhtml":
		return renderOuterHTML(n)
	}
	if strings.HasPrefix(strings.ToLower(a), "attr:") {
		a = a[5:]
	}
	a = strings.TrimPrefix(a, "@")
	if n.Type == html.ElementNode {
		for _, at := range n.Attr {
			if strings.EqualFold(at.Key, a) {
				return strings.TrimSpace(at.Val)
			}
		}
	}
	return ""
}

func renderInnerHTML(n *html.Node) string {
	var b bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&b, c)
	}
	return b.String()
}

func renderOuterHTML(n *html.Node) string {
	var b bytes.Buffer
	_ = html.Render(&b, n)
	return b.String()
}
