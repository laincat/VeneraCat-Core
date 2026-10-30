// Package html implements the DOM-handle protocol of the legacy ComicSource
// "html" sendMessage method (docs/SENDMESSAGE-SPEC.md, section 4) without any
// JavaScript engine dependency.
//
// Documents are identified by an integer key chosen by the caller. Inside a
// document, elements and nodes are addressed by integer handles. Elements and
// nodes use two separate index spaces. Handles are allocated sequentially from
// 0, are never reused and are not deduplicated: querying the same element twice
// yields two handles, as in the official host whose lists only grow. All
// handles of a document are released together by dispose (or by eviction).
//
// # Known divergences from the Dart "html" package
//
// Parsing uses golang.org/x/net/html (HTML5 tree builder). Dart's package
// implements the same specification family, but equivalence is not claimed;
// expect differences on malformed input, <template>, foreign content
// (SVG/MathML), and encoding handling (input here is already a Go string).
//
// Other known differences:
//   - CSS: cascadia supports a different selector set than Dart's package and
//     reports invalid selectors as errors where Dart throws.
//   - getInnerHTML uses an HTML5-style serializer written for this package
//     (void elements without a slash, raw text inside script/style and
//     similar). It is modelled on Dart's output, not verified byte for byte.
//   - getClassNames de-duplicates names, keeping first-occurrence order,
//     because Dart's Element.classes is a set.
//   - getParent returns nil when the parent is the document node, because
//     only element handles exist for elements.
//   - Namespaced attributes (e.g. xlink:href) are reported as "prefix:name".
//   - getLocalName returns the parser's element name unchanged: lower case for
//     HTML, case-adjusted for SVG (e.g. "foreignObject").
//   - node_text returns nil for document and doctype nodes (Dart returns null).
package html

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/andybalholm/cascadia"
	nethtml "golang.org/x/net/html"
)

const (
	// MaxLiveDocuments is the number of documents kept alive at once. Parsing
	// one more evicts the oldest.
	MaxLiveDocuments = 9
	// MaxDocumentBytes is the largest accepted HTML source.
	MaxDocumentBytes = 16 << 20
	// MaxHandles caps the element handles and, separately, the node handles
	// of one document.
	MaxHandles = 1_000_000
)

// Message is one html sendMessage call. Key is the document key for
// parse, dispose, querySelector, querySelectorAll and getElementById, and the
// element or node handle for all other functions, which name their document
// in Doc.
type Message struct {
	Function string
	Key, Doc int64
	Query    string
	ID       string
	Data     string
}

type document struct {
	root  *nethtml.Node
	elems []*nethtml.Node
	nodes []*nethtml.Node
}

// Store holds parsed documents. It is safe for concurrent use.
type Store struct {
	// Strict makes unknown function names return an error. By default they
	// return (nil, nil), like the official host. Set it before sharing the
	// Store between goroutines.
	Strict bool

	mu    sync.Mutex
	docs  map[int64]*document
	order []int64 // keys of live documents, oldest first
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{docs: make(map[int64]*document)}
}

// Do executes m. Handles are returned as int, handle lists as []int, text as
// string, attributes as map[string]string, class names as []string. An absent
// result is nil.
func (s *Store) Do(m Message) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch m.Function {
	case "parse":
		return nil, s.parse(m.Key, m.Data)
	case "dispose":
		s.dispose(m.Key)
		return nil, nil
	case "querySelector", "querySelectorAll", "getElementById":
		d, err := s.doc(m.Key)
		if err != nil {
			return nil, err
		}
		switch m.Function {
		case "querySelector":
			return d.query(d.root, m.Query, false)
		case "querySelectorAll":
			return d.query(d.root, m.Query, true)
		default:
			return d.byID(m.ID)
		}
	case "dom_querySelector", "dom_querySelectorAll", "getText", "getAttributes",
		"getInnerHTML", "getChildren", "getNodes", "getParent",
		"getPreviousSibling", "getNextSibling", "getClassNames", "getId",
		"getLocalName":
		d, err := s.doc(m.Doc)
		if err != nil {
			return nil, err
		}
		e, err := d.elem(m.Key)
		if err != nil {
			return nil, err
		}
		return d.elementFunc(m, e)
	case "node_text", "node_type", "node_to_element", "node_toElement":
		d, err := s.doc(m.Doc)
		if err != nil {
			return nil, err
		}
		n, err := d.node(m.Key)
		if err != nil {
			return nil, err
		}
		return d.nodeFunc(m.Function, n)
	}
	if s.Strict {
		return nil, fmt.Errorf("html: unknown function %q", m.Function)
	}
	return nil, nil
}

func (s *Store) parse(key int64, data string) error {
	if len(data) > MaxDocumentBytes {
		return fmt.Errorf("html: document is %d bytes, limit is %d", len(data), MaxDocumentBytes)
	}
	root, err := nethtml.Parse(strings.NewReader(data))
	if err != nil {
		return fmt.Errorf("html: parse: %w", err)
	}
	s.dispose(key)
	s.docs[key] = &document{root: root}
	s.order = append(s.order, key)
	if len(s.order) > MaxLiveDocuments {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.docs, oldest)
	}
	return nil
}

func (s *Store) dispose(key int64) {
	if _, ok := s.docs[key]; !ok {
		return
	}
	delete(s.docs, key)
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *Store) doc(key int64) (*document, error) {
	d, ok := s.docs[key]
	if !ok {
		return nil, fmt.Errorf("html: no document with key %d (never parsed, disposed or evicted)", key)
	}
	return d, nil
}

func (d *document) elem(h int64) (*nethtml.Node, error) {
	if h < 0 || h >= int64(len(d.elems)) {
		return nil, fmt.Errorf("html: invalid element handle %d", h)
	}
	return d.elems[h], nil
}

func (d *document) node(h int64) (*nethtml.Node, error) {
	if h < 0 || h >= int64(len(d.nodes)) {
		return nil, fmt.Errorf("html: invalid node handle %d", h)
	}
	return d.nodes[h], nil
}

var errHandleLimit = errors.New("html: handle limit exceeded for document")

// addElems registers ns as element handles and returns them. Nothing is
// registered if the cap would be exceeded.
func (d *document) addElems(ns []*nethtml.Node) ([]int, error) {
	if len(d.elems)+len(ns) > MaxHandles {
		return nil, errHandleLimit
	}
	out := make([]int, len(ns))
	for i, n := range ns {
		out[i] = len(d.elems)
		d.elems = append(d.elems, n)
	}
	return out, nil
}

// elemHandle registers n (nil allowed) and returns its handle or nil.
func (d *document) elemHandle(n *nethtml.Node) (any, error) {
	if n == nil {
		return nil, nil
	}
	hs, err := d.addElems([]*nethtml.Node{n})
	if err != nil {
		return nil, err
	}
	return hs[0], nil
}

func (d *document) addNodes(ns []*nethtml.Node) ([]int, error) {
	if len(d.nodes)+len(ns) > MaxHandles {
		return nil, errHandleLimit
	}
	out := make([]int, len(ns))
	for i, n := range ns {
		out[i] = len(d.nodes)
		d.nodes = append(d.nodes, n)
	}
	return out, nil
}

func (d *document) elementFunc(m Message, e *nethtml.Node) (any, error) {
	switch m.Function {
	case "dom_querySelector":
		return d.query(e, m.Query, false)
	case "dom_querySelectorAll":
		return d.query(e, m.Query, true)
	case "getText":
		return textOf(e), nil
	case "getAttributes":
		return attributes(e), nil
	case "getInnerHTML":
		var b strings.Builder
		for c := e.FirstChild; c != nil; c = c.NextSibling {
			serialize(&b, c)
		}
		return b.String(), nil
	case "getChildren":
		var kids []*nethtml.Node
		for c := e.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == nethtml.ElementNode {
				kids = append(kids, c)
			}
		}
		return d.addElems(kids)
	case "getNodes":
		var kids []*nethtml.Node
		for c := e.FirstChild; c != nil; c = c.NextSibling {
			kids = append(kids, c)
		}
		return d.addNodes(kids)
	case "getParent":
		if p := e.Parent; p != nil && p.Type == nethtml.ElementNode {
			return d.elemHandle(p)
		}
		return nil, nil
	case "getPreviousSibling":
		for p := e.PrevSibling; p != nil; p = p.PrevSibling {
			if p.Type == nethtml.ElementNode {
				return d.elemHandle(p)
			}
		}
		return nil, nil
	case "getNextSibling":
		for p := e.NextSibling; p != nil; p = p.NextSibling {
			if p.Type == nethtml.ElementNode {
				return d.elemHandle(p)
			}
		}
		return nil, nil
	case "getClassNames":
		return classNames(attr(e, "class")), nil
	case "getId":
		return attr(e, "id"), nil
	default: // getLocalName
		return e.Data, nil
	}
}

func (d *document) nodeFunc(fn string, n *nethtml.Node) (any, error) {
	switch fn {
	case "node_text":
		switch n.Type {
		case nethtml.TextNode, nethtml.CommentNode:
			return n.Data, nil
		case nethtml.ElementNode:
			return textOf(n), nil
		}
		return nil, nil
	case "node_type":
		switch n.Type {
		case nethtml.ElementNode:
			return "element", nil
		case nethtml.TextNode:
			return "text", nil
		case nethtml.CommentNode:
			return "comment", nil
		case nethtml.DocumentNode:
			return "document", nil
		}
		return "unknown", nil
	default: // node_to_element, node_toElement
		if n.Type != nethtml.ElementNode {
			return nil, nil
		}
		return d.elemHandle(n)
	}
}

// query finds elements matching the selector among the descendants of from
// (never from itself). With all=false it returns one handle or nil; with
// all=true it returns []int, empty when nothing matches.
func (d *document) query(from *nethtml.Node, sel string, all bool) (any, error) {
	g, err := cascadia.ParseGroup(sel)
	if err != nil {
		return nil, fmt.Errorf("html: invalid selector %q: %w", sel, err)
	}
	var found []*nethtml.Node
	walk(from, func(n *nethtml.Node) bool {
		if n.Type == nethtml.ElementNode && g.Match(n) {
			found = append(found, n)
			return all
		}
		return true
	})
	if all {
		return d.addElems(found)
	}
	if len(found) == 0 {
		return nil, nil
	}
	return d.elemHandle(found[0])
}

func (d *document) byID(id string) (any, error) {
	if id == "" {
		return nil, nil
	}
	var hit *nethtml.Node
	walk(d.root, func(n *nethtml.Node) bool {
		if n.Type == nethtml.ElementNode && attr(n, "id") == id {
			hit = n
			return false
		}
		return true
	})
	return d.elemHandle(hit)
}

// walk visits the descendants of root in document order without recursion.
// It stops when visit returns false.
func walk(root *nethtml.Node, visit func(*nethtml.Node) bool) {
	n := root.FirstChild
	for n != nil {
		if !visit(n) {
			return
		}
		if n.FirstChild != nil {
			n = n.FirstChild
			continue
		}
		for n != root && n.NextSibling == nil {
			n = n.Parent
		}
		if n == root {
			return
		}
		n = n.NextSibling
	}
}

// textOf concatenates all descendant text nodes (script and style content
// included, comments excluded), like Dart's Element.text.
func textOf(e *nethtml.Node) string {
	var b strings.Builder
	walk(e, func(n *nethtml.Node) bool {
		if n.Type == nethtml.TextNode {
			b.WriteString(n.Data)
		}
		return true
	})
	return b.String()
}

func attr(e *nethtml.Node, name string) string {
	for _, a := range e.Attr {
		if a.Namespace == "" && a.Key == name {
			return a.Val
		}
	}
	return ""
}

func attrName(a nethtml.Attribute) string {
	if a.Namespace != "" {
		return a.Namespace + ":" + a.Key
	}
	return a.Key
}

func attributes(e *nethtml.Node) map[string]string {
	out := make(map[string]string, len(e.Attr))
	for _, a := range e.Attr {
		k := attrName(a)
		if _, dup := out[k]; !dup { // first occurrence wins, as in the HTML spec
			out[k] = a.Val
		}
	}
	return out
}

func classNames(v string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range strings.Fields(v) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}
