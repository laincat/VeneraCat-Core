package adapt

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// DefaultMaxChunkRunes is the default character budget per chapter chunk.
const DefaultMaxChunkRunes = 2400

var skippedTags = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"svg":      true,
	"iframe":   true,
}

var blockTags = map[string]bool{
	"p":          true,
	"div":        true,
	"section":    true,
	"article":    true,
	"blockquote": true,
	"li":         true,
	"tr":         true,
	"hr":         true,
	"h1":         true,
	"h2":         true,
	"h3":         true,
	"h4":         true,
	"h5":         true,
	"h6":         true,
}

// ExtractText converts raw chapter HTML into clean plain-text paragraphs
// separated by blank lines. Scripts, styles, and embedded frames are omitted.
func ExtractText(rawHTML string) string {
	if strings.TrimSpace(rawHTML) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return normalizeParagraphs(rawHTML)
	}

	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if skippedTags[tag] {
				return
			}
			if tag == "br" {
				b.WriteString("\n\n")
				return
			}
			if blockTags[tag] {
				b.WriteString("\n\n")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[strings.ToLower(n.Data)] {
			b.WriteString("\n\n")
		}
	}
	walk(doc)
	return normalizeParagraphs(b.String())
}

func normalizeParagraphs(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u3000", " ")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		cleaned := collapseSpaces(line)
		if cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return strings.Join(out, "\n\n")
}

func collapseSpaces(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// ChunkText splits plain text on paragraph boundaries so each chunk stays
// within maxRunes runes. A single paragraph longer than maxRunes is split at
// sentence boundaries when possible, then by rune count.
func ChunkText(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxChunkRunes
	}
	trimmed := normalizeParagraphs(text)
	if trimmed == "" {
		return nil
	}
	paras := strings.Split(trimmed, "\n\n")
	var chunks []string
	var cur strings.Builder
	curRunes := 0

	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
			curRunes = 0
		}
	}

	for _, p := range paras {
		pRunes := len([]rune(p))
		if pRunes > maxRunes {
			flush()
			chunks = append(chunks, splitLongParagraph(p, maxRunes)...)
			continue
		}
		sepRunes := 0
		if curRunes > 0 {
			sepRunes = 2
		}
		if curRunes > 0 && curRunes+sepRunes+pRunes > maxRunes {
			flush()
		}
		if curRunes > 0 {
			cur.WriteString("\n\n")
			curRunes += 2
		}
		cur.WriteString(p)
		curRunes += pRunes
	}
	flush()
	return chunks
}

func splitLongParagraph(p string, maxRunes int) []string {
	runes := []rune(p)
	var out []string
	for len(runes) > maxRunes {
		cut := maxRunes
		for i := maxRunes - 1; i >= maxRunes/2; i-- {
			switch runes[i] {
			case '。', '！', '？', '!', '?', '.', '；', ';':
				cut = i + 1
				i = 0
			}
		}
		part := strings.TrimSpace(string(runes[:cut]))
		if part != "" {
			out = append(out, part)
		}
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		out = append(out, tail)
	}
	return out
}
