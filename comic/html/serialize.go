package html

import (
	"strings"

	nethtml "golang.org/x/net/html"
)

var voidElements = map[string]bool{
	"area": true, "base": true, "basefont": true, "bgsound": true, "br": true,
	"col": true, "command": true, "embed": true, "frame": true, "hr": true,
	"img": true, "input": true, "keygen": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

var rawTextParents = map[string]bool{
	"style": true, "script": true, "xmp": true, "iframe": true,
	"noembed": true, "noframes": true, "plaintext": true,
}

// serialize writes n and its subtree using the HTML fragment serialization
// rules: void elements get no end tag, text under raw-text elements is not
// escaped.
func serialize(b *strings.Builder, n *nethtml.Node) {
	switch n.Type {
	case nethtml.TextNode:
		if p := n.Parent; p != nil && p.Type == nethtml.ElementNode && rawTextParents[p.Data] {
			b.WriteString(n.Data)
		} else {
			escape(b, n.Data, false)
		}
	case nethtml.CommentNode:
		b.WriteString("<!--")
		b.WriteString(n.Data)
		b.WriteString("-->")
	case nethtml.DoctypeNode:
		b.WriteString("<!DOCTYPE ")
		b.WriteString(n.Data)
		b.WriteString(">")
	case nethtml.ElementNode:
		b.WriteByte('<')
		b.WriteString(n.Data)
		for _, a := range n.Attr {
			b.WriteByte(' ')
			b.WriteString(attrName(a))
			b.WriteString(`="`)
			escape(b, a.Val, true)
			b.WriteByte('"')
		}
		b.WriteByte('>')
		if voidElements[n.Data] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			serialize(b, c)
		}
		b.WriteString("</")
		b.WriteString(n.Data)
		b.WriteByte('>')
	default: // document
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			serialize(b, c)
		}
	}
}

func escape(b *strings.Builder, s string, attribute bool) {
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '\u00a0':
			b.WriteString("&nbsp;")
		case r == '"' && attribute:
			b.WriteString("&quot;")
		case r == '<' && !attribute:
			b.WriteString("&lt;")
		case r == '>' && !attribute:
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
}
