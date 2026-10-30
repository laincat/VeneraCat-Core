package html

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	nethtml "golang.org/x/net/html"
)

const fixture = `<!DOCTYPE html>
<html><head><title>T</title><script>var a = 1 < 2;</script></head>
<body>
<div id="root" class="a  b	c a" data-x="1&amp;2">
  Hello <b>bold</b> &amp; &lt;world&gt;&nbsp;!
  <!-- note -->
  <ul id="list"><li class="item first">one</li><li class="item">two</li><li class="item last">three</li></ul>
  <p>tail</p>
</div>
<span id="after">x</span>
</body></html>`

func mustDo(t *testing.T, s *Store, m Message) any {
	t.Helper()
	v, err := s.Do(m)
	if err != nil {
		t.Fatalf("%s: %v", m.Function, err)
	}
	return v
}

func newDoc(t *testing.T, key int64, data string) *Store {
	t.Helper()
	s := NewStore()
	mustDo(t, s, Message{Function: "parse", Key: key, Data: data})
	return s
}

func el(t *testing.T, s *Store, doc int64, q string) int64 {
	t.Helper()
	v := mustDo(t, s, Message{Function: "querySelector", Key: doc, Query: q})
	h, ok := v.(int)
	if !ok {
		t.Fatalf("querySelector(%q) = %#v, want handle", q, v)
	}
	return int64(h)
}

func call(t *testing.T, s *Store, fn string, doc, key int64) any {
	t.Helper()
	return mustDo(t, s, Message{Function: fn, Doc: doc, Key: key})
}

func TestQueryAndText(t *testing.T) {
	s := newDoc(t, 1, fixture)
	root := el(t, s, 1, "#root")
	if got := call(t, s, "getId", 1, root); got != "root" {
		t.Errorf("getId = %#v", got)
	}
	if got := call(t, s, "getLocalName", 1, root); got != "div" {
		t.Errorf("getLocalName = %#v", got)
	}
	text := call(t, s, "getText", 1, root).(string)
	if !strings.Contains(text, "Hello bold & <world>\u00a0!") || strings.Contains(text, "note") {
		t.Errorf("getText = %q", text)
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "tail") {
		t.Errorf("getText tail = %q", text)
	}
	attrs := call(t, s, "getAttributes", 1, root).(map[string]string)
	want := map[string]string{"id": "root", "class": "a  b\tc a", "data-x": "1&2"}
	if !reflect.DeepEqual(attrs, want) {
		t.Errorf("getAttributes = %v", attrs)
	}
	classes := call(t, s, "getClassNames", 1, root)
	if !reflect.DeepEqual(classes, []string{"a", "b", "c"}) {
		t.Errorf("getClassNames = %#v", classes)
	}
	span := el(t, s, 1, "span")
	if got := call(t, s, "getClassNames", 1, span); !reflect.DeepEqual(got, []string{}) {
		t.Errorf("no class = %#v", got)
	}
	nope := mustDo(t, s, Message{Function: "querySelector", Key: 1, Query: ".missing"})
	if nope != nil {
		t.Errorf("missing = %#v", nope)
	}
	all := mustDo(t, s, Message{Function: "querySelectorAll", Key: 1, Query: ".missing"})
	if !reflect.DeepEqual(all, []int{}) {
		t.Errorf("missing all = %#v", all)
	}
	if got := mustDo(t, s, Message{Function: "getElementById", Key: 1, ID: "after"}); got == nil {
		t.Error("getElementById after = nil")
	}
	if got := mustDo(t, s, Message{Function: "getElementById", Key: 1, ID: "zzz"}); got != nil {
		t.Errorf("getElementById zzz = %#v", got)
	}
}

func TestScriptTextIncluded(t *testing.T) {
	s := newDoc(t, 1, fixture)
	sc := el(t, s, 1, "script")
	if got := call(t, s, "getText", 1, sc); got != "var a = 1 < 2;" {
		t.Errorf("script text = %#v", got)
	}
	head := el(t, s, 1, "head")
	if got := call(t, s, "getText", 1, head).(string); !strings.Contains(got, "var a = 1 < 2;") {
		t.Errorf("head text = %q", got)
	}
	if got := call(t, s, "getInnerHTML", 1, head).(string); !strings.Contains(got, "<script>var a = 1 < 2;</script>") {
		t.Errorf("head innerHTML = %q", got)
	}
}

func TestDomQuerySearchesDescendantsOnly(t *testing.T) {
	s := newDoc(t, 1, fixture)
	list := el(t, s, 1, "#list")
	got := mustDo(t, s, Message{Function: "dom_querySelector", Doc: 1, Key: list, Query: "ul"})
	if got != nil {
		t.Errorf("self matched: %#v", got)
	}
	got = mustDo(t, s, Message{Function: "dom_querySelectorAll", Doc: 1, Key: list, Query: "li.item"})
	if l, ok := got.([]int); !ok || len(l) != 3 {
		t.Fatalf("li.item = %#v", got)
	}
	h := mustDo(t, s, Message{Function: "dom_querySelector", Doc: 1, Key: list, Query: "li.last"}).(int)
	if got := call(t, s, "getText", 1, int64(h)); got != "three" {
		t.Errorf("text = %#v", got)
	}
	// Selector with ancestor outside the scope still matches (like Dart).
	if got := mustDo(t, s, Message{Function: "dom_querySelector", Doc: 1, Key: list, Query: "div li"}); got == nil {
		t.Error("ancestor combinator lost")
	}
}

func TestInnerHTML(t *testing.T) {
	s := newDoc(t, 1, `<div id="d"><p class="x" title='a"b'>1 &lt; 2 &amp; 3</p><br><img src="a.png"><!--c--></div>`)
	d := el(t, s, 1, "#d")
	want := `<p class="x" title="a&quot;b">1 &lt; 2 &amp; 3</p><br><img src="a.png"><!--c-->`
	if got := call(t, s, "getInnerHTML", 1, d); got != want {
		t.Errorf("innerHTML = %q, want %q", got, want)
	}
}

func TestChildrenNodesAndTypes(t *testing.T) {
	s := newDoc(t, 1, fixture)
	root := el(t, s, 1, "#root")
	kids := call(t, s, "getChildren", 1, root).([]int)
	// b, ul, p
	if len(kids) != 3 {
		t.Fatalf("children = %v", kids)
	}
	names := []string{}
	for _, k := range kids {
		names = append(names, call(t, s, "getLocalName", 1, int64(k)).(string))
	}
	if !reflect.DeepEqual(names, []string{"b", "ul", "p"}) {
		t.Errorf("child names = %v", names)
	}

	nodes := call(t, s, "getNodes", 1, root).([]int)
	var types []string
	var texts []string
	for _, n := range nodes {
		types = append(types, call(t, s, "node_type", 1, int64(n)).(string))
		texts = append(texts, mustDo(t, s, Message{Function: "node_text", Doc: 1, Key: int64(n)}).(string))
	}
	wantTypes := []string{"text", "element", "text", "comment", "text", "element", "text", "element", "text"}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Errorf("types = %v", types)
	}
	if texts[3] != " note " {
		t.Errorf("comment text = %q", texts[3])
	}
	if texts[1] != "bold" {
		t.Errorf("element node_text = %q", texts[1])
	}
	// node_to_element: element -> handle, text -> nil, both spellings.
	for _, fn := range []string{"node_to_element", "node_toElement"} {
		if got := call(t, s, fn, 1, int64(nodes[1])); got == nil {
			t.Errorf("%s(element) = nil", fn)
		}
		if got := call(t, s, fn, 1, int64(nodes[0])); got != nil {
			t.Errorf("%s(text) = %#v", fn, got)
		}
	}
	h := call(t, s, "node_toElement", 1, int64(nodes[1])).(int)
	if got := call(t, s, "getLocalName", 1, int64(h)); got != "b" {
		t.Errorf("toElement localName = %#v", got)
	}
}

func TestDocumentNodeAndUnknownNodeType(t *testing.T) {
	s := newDoc(t, 1, "<!DOCTYPE html><p>x")
	d1 := s.docs[1]
	// register the document node and the doctype node by hand.
	hs, err := d1.addNodes([]*nethtml.Node{d1.root, d1.root.FirstChild})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(t, s, "node_type", 1, int64(hs[0])); got != "document" {
		t.Errorf("document type = %#v", got)
	}
	if got := call(t, s, "node_type", 1, int64(hs[1])); got != "unknown" {
		t.Errorf("doctype type = %#v", got)
	}
	if got := mustDo(t, s, Message{Function: "node_text", Doc: 1, Key: int64(hs[0])}); got != nil {
		t.Errorf("document node_text = %#v", got)
	}
}

func TestSiblingNavigationSkipsText(t *testing.T) {
	s := newDoc(t, 1, fixture)
	list := el(t, s, 1, "#list")
	prev := call(t, s, "getPreviousSibling", 1, list).(int)
	if got := call(t, s, "getLocalName", 1, int64(prev)); got != "b" {
		t.Errorf("prev = %#v", got)
	}
	next := call(t, s, "getNextSibling", 1, list).(int)
	if got := call(t, s, "getLocalName", 1, int64(next)); got != "p" {
		t.Errorf("next = %#v", got)
	}
	b := el(t, s, 1, "#root > b")
	if got := call(t, s, "getPreviousSibling", 1, b); got != nil {
		t.Errorf("first element prev = %#v", got)
	}
	p := el(t, s, 1, "#root > p")
	if got := call(t, s, "getNextSibling", 1, p); got != nil {
		t.Errorf("last element next = %#v", got)
	}
	parent := call(t, s, "getParent", 1, list).(int)
	if got := call(t, s, "getId", 1, int64(parent)); got != "root" {
		t.Errorf("parent id = %#v", got)
	}
	html := el(t, s, 1, "html")
	if got := call(t, s, "getParent", 1, html); got != nil {
		t.Errorf("html parent = %#v", got)
	}
}

func TestGetIdEmpty(t *testing.T) {
	s := newDoc(t, 1, `<p>x</p>`)
	p := el(t, s, 1, "p")
	if got := call(t, s, "getId", 1, p); got != "" {
		t.Errorf("getId = %#v", got)
	}
}

func TestSeparateHandleSpaces(t *testing.T) {
	s := newDoc(t, 1, `<div><i>a</i>t<b>c</b></div>`)
	// first element handle and first node handle are both 0.
	div := el(t, s, 1, "div")
	if div != 0 {
		t.Fatalf("first element handle = %d", div)
	}
	nodes := call(t, s, "getNodes", 1, div).([]int)
	if !reflect.DeepEqual(nodes, []int{0, 1, 2}) {
		t.Fatalf("nodes = %v", nodes)
	}
	// Handles are never reused or deduplicated.
	again := el(t, s, 1, "div")
	if again == div {
		t.Error("handle reused")
	}
	if got := call(t, s, "getText", 1, again); got != "atc" {
		t.Errorf("text = %#v", got)
	}
	more := call(t, s, "getNodes", 1, div).([]int)
	if !reflect.DeepEqual(more, []int{3, 4, 5}) {
		t.Errorf("second getNodes = %v", more)
	}
	// Node handle 1 (text) is not an element handle: node funcs work on it
	// while element handle 1 is the duplicate div.
	if got := call(t, s, "node_type", 1, 1); got != "text" {
		t.Errorf("node 1 = %#v", got)
	}
	if got := call(t, s, "getLocalName", 1, again); got != "div" {
		t.Errorf("element %d = %#v", again, got)
	}
}

func TestEviction(t *testing.T) {
	s := NewStore()
	for k := int64(1); k <= 10; k++ {
		mustDo(t, s, Message{Function: "parse", Key: k, Data: "<p id=x>" + string(rune('a'+k)) + "</p>"})
	}
	if _, err := s.Do(Message{Function: "querySelector", Key: 1, Query: "p"}); err == nil {
		t.Error("evicted document still accessible")
	}
	for k := int64(2); k <= 10; k++ {
		if v := mustDo(t, s, Message{Function: "querySelector", Key: k, Query: "p"}); v == nil {
			t.Errorf("doc %d missing", k)
		}
	}
	// Reparsing a live key refreshes its age without evicting another document.
	mustDo(t, s, Message{Function: "parse", Key: 2, Data: "<p>new</p>"})
	if len(s.docs) != 9 {
		t.Errorf("live docs = %d", len(s.docs))
	}
	mustDo(t, s, Message{Function: "parse", Key: 11, Data: "<p>n</p>"})
	if _, err := s.Do(Message{Function: "querySelector", Key: 3, Query: "p"}); err == nil {
		t.Error("doc 3 should be evicted")
	}
	if _, err := s.Do(Message{Function: "querySelector", Key: 2, Query: "p"}); err != nil {
		t.Errorf("doc 2 evicted too early: %v", err)
	}
}

func TestDisposeAndMissing(t *testing.T) {
	s := newDoc(t, 1, "<p>x</p>")
	p := el(t, s, 1, "p")
	mustDo(t, s, Message{Function: "dispose", Key: 1})
	mustDo(t, s, Message{Function: "dispose", Key: 1}) // idempotent
	for _, m := range []Message{
		{Function: "querySelector", Key: 1, Query: "p"},
		{Function: "getElementById", Key: 1, ID: "a"},
		{Function: "getText", Doc: 1, Key: p},
		{Function: "node_type", Doc: 1, Key: 0},
		{Function: "getText", Doc: 99, Key: 0},
	} {
		if _, err := s.Do(m); err == nil {
			t.Errorf("%s on missing document: no error", m.Function)
		}
	}
	s2 := newDoc(t, 1, "<p>x</p>")
	if _, err := s2.Do(Message{Function: "getText", Doc: 1, Key: 5}); err == nil {
		t.Error("invalid element handle: no error")
	}
	if _, err := s2.Do(Message{Function: "node_text", Doc: 1, Key: -1}); err == nil {
		t.Error("invalid node handle: no error")
	}
}

func TestInvalidSelector(t *testing.T) {
	s := newDoc(t, 1, "<p>x</p>")
	p := el(t, s, 1, "p")
	for _, m := range []Message{
		{Function: "querySelector", Key: 1, Query: "p["},
		{Function: "querySelectorAll", Key: 1, Query: ">>"},
		{Function: "dom_querySelector", Doc: 1, Key: p, Query: ":::"},
		{Function: "dom_querySelectorAll", Doc: 1, Key: p, Query: ""},
	} {
		if _, err := s.Do(m); err == nil {
			t.Errorf("%s(%q): no error", m.Function, m.Query)
		}
	}
}

func TestMalformedHTML(t *testing.T) {
	s := newDoc(t, 1, `<div id=a><p>one<p>two<b>bold<div class=z>x`)
	ps := mustDo(t, s, Message{Function: "querySelectorAll", Key: 1, Query: "p"}).([]int)
	if len(ps) != 2 {
		t.Fatalf("p count = %d", len(ps))
	}
	if got := call(t, s, "getText", 1, int64(ps[0])); got != "one" {
		t.Errorf("p0 = %#v", got)
	}
	if v := mustDo(t, s, Message{Function: "querySelector", Key: 1, Query: ".z"}); v == nil {
		t.Error("unclosed .z missing")
	}
	e := newDoc(t, 2, "")
	if v := mustDo(t, e, Message{Function: "querySelector", Key: 2, Query: "body"}); v == nil {
		t.Error("empty input has no body")
	}
}

func TestUnknownFunction(t *testing.T) {
	s := NewStore()
	if v, err := s.Do(Message{Function: "bogus"}); v != nil || err != nil {
		t.Errorf("lenient = %#v, %v", v, err)
	}
	s.Strict = true
	if _, err := s.Do(Message{Function: "bogus"}); err == nil {
		t.Error("strict: no error")
	}
}

func TestLimits(t *testing.T) {
	s := NewStore()
	big := strings.Repeat("a", MaxDocumentBytes+1)
	if _, err := s.Do(Message{Function: "parse", Key: 1, Data: big}); err == nil {
		t.Error("oversize document accepted")
	}
	if len(s.docs) != 0 {
		t.Error("oversize document stored")
	}
	// Parse failure must not drop an existing document under the same key.
	mustDo(t, s, Message{Function: "parse", Key: 1, Data: "<p>x</p>"})
	if _, err := s.Do(Message{Function: "parse", Key: 1, Data: big}); err == nil {
		t.Error("oversize document accepted")
	}
	if v := mustDo(t, s, Message{Function: "querySelector", Key: 1, Query: "p"}); v == nil {
		t.Error("existing document lost after rejected parse")
	}

	d := &document{}
	d.elems = make([]*nethtml.Node, MaxHandles-1)
	if _, err := d.addElems(make([]*nethtml.Node, 2)); err == nil {
		t.Error("element cap not enforced")
	}
	if len(d.elems) != MaxHandles-1 {
		t.Error("partial registration on cap error")
	}
	if _, err := d.addElems(make([]*nethtml.Node, 1)); err != nil {
		t.Errorf("filling to the cap failed: %v", err)
	}
	if h, err := d.elemHandle(&nethtml.Node{}); err == nil {
		t.Errorf("elemHandle beyond cap = %v", h)
	}
	d.nodes = make([]*nethtml.Node, MaxHandles)
	if _, err := d.addNodes(make([]*nethtml.Node, 1)); err == nil {
		t.Error("node cap not enforced")
	}
}

func TestDeepNesting(t *testing.T) {
	s := newDoc(t, 1, strings.Repeat("<span>", 400)+"x")
	v := mustDo(t, s, Message{Function: "querySelectorAll", Key: 1, Query: "span"}).([]int)
	if len(v) != 400 {
		t.Fatalf("spans = %d", len(v))
	}
	if got := call(t, s, "getText", 1, int64(v[0])); got != "x" {
		t.Errorf("deep text = %#v", got)
	}
	// golang.org/x/net/html refuses more than 512 open elements; the error
	// must surface instead of panicking or storing a document.
	if _, err := s.Do(Message{Function: "parse", Key: 2, Data: strings.Repeat("<span>", 5000)}); err == nil {
		t.Error("over-deep document accepted")
	}
	if _, err := s.Do(Message{Function: "querySelector", Key: 2, Query: "span"}); err == nil {
		t.Error("failed parse stored a document")
	}
}

func TestConcurrent(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := int64(g*1000 + i)
				if _, err := s.Do(Message{Function: "parse", Key: key, Data: fixture}); err != nil {
					t.Error(err)
					return
				}
				// The document may already be evicted by other goroutines;
				// only data races and panics matter here.
				v, err := s.Do(Message{Function: "querySelectorAll", Key: key, Query: "li"})
				if err != nil {
					continue
				}
				for _, h := range v.([]int) {
					_, _ = s.Do(Message{Function: "getText", Doc: key, Key: int64(h)})
				}
				_, _ = s.Do(Message{Function: "dispose", Key: key})
			}
		}(g)
	}
	wg.Wait()
}
