package js

import (
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
)

//go:embed init.js
var initJS string

type msg = map[string]any

// asyncReply makes the fake host answer with a Promise.
type asyncReply struct{ v any }

// fakeHost is a MoeJS runtime with a recording sendMessage.
type fakeHost struct {
	t      *testing.T
	rt     *moejs.Runtime
	loop   *eventloop.Loop
	msgs   []msg
	handle func(m msg) (any, error)
}

// defaultReply answers like a cooperative host.
func defaultReply(m msg) (any, error) {
	switch m["method"] {
	case "delay":
		return asyncReply{nil}, nil
	case "http":
		return asyncReply{msg{"status": 200, "headers": msg{"a": "b"}, "body": "", "error": nil}}, nil
	case "getClipboard":
		return asyncReply{"clip"}, nil
	case "getLocale":
		return "zh_CN", nil
	case "getPlatform":
		return "windows", nil
	}
	return nil, nil
}

func newHost(t *testing.T, handle func(m msg) (any, error)) *fakeHost {
	t.Helper()
	if handle == nil {
		handle = defaultReply
	}
	h := &fakeHost{t: t, handle: handle}
	h.rt = moejs.NewRuntime(moejs.Options{})
	loop, err := eventloop.New(h.rt, eventloop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.loop = loop
	if err := h.rt.SetGlobal("sendMessage", moejs.NativeFunc(h.send)); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.SetGlobal("appVersion", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	h.run(initJS)
	h.msgs = nil
	return h
}

func (h *fakeHost) send(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	raw, err := r.ToGoStrict(moejs.Arg(args, 0))
	if err != nil {
		return moejs.Undefined(), err
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return moejs.Undefined(), fmt.Errorf("message is %T, want object", raw)
	}
	h.msgs = append(h.msgs, m)
	out, err := h.handle(m)
	if err != nil {
		return moejs.Undefined(), err
	}
	if a, ok := out.(asyncReply); ok {
		p, settle := h.loop.NewPromise()
		go settle(func(rt *moejs.Runtime) (moejs.Value, error) { return rt.FromGo(a.v) })
		return p, nil
	}
	return r.FromGo(out)
}

// run executes src on the event loop and returns the normalized completion value.
func (h *fakeHost) run(src string) any {
	h.t.Helper()
	s, err := moejs.CompileScript("test.js", src)
	if err != nil {
		h.t.Fatalf("compile: %v\n%s", err, src)
	}
	var out any
	err = h.loop.Run(func(rt *moejs.Runtime) error {
		v, err := rt.RunScript(s)
		if err != nil {
			return err
		}
		out, err = rt.ToGo(v)
		return err
	})
	if err != nil {
		h.t.Fatalf("run: %v\n%s", err, src)
	}
	return norm(out)
}

// runErr is run for scripts expected to throw.
func (h *fakeHost) runErr(src string) error {
	h.t.Helper()
	s, err := moejs.CompileScript("test.js", src)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.loop.Run(func(rt *moejs.Runtime) error {
		_, err := rt.RunScript(s)
		return err
	})
}

func (h *fakeHost) take() []msg {
	out := h.msgs
	h.msgs = nil
	return out
}

// norm makes Go values from MoeJS and from test literals comparable.
func norm(v any) any {
	switch x := v.(type) {
	case nil, string, bool, float64:
		return v
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case []byte:
		return "bytes:" + hex.EncodeToString(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = norm(x[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = norm(e)
		}
		return out
	case *moejs.Object:
		return "<func>"
	}
	return fmt.Sprintf("<%T>", v)
}

func wantMsg(t *testing.T, got []msg, want msg) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("no message sent, want %v", want)
	}
	last := got[len(got)-1]
	if !reflect.DeepEqual(norm(last), norm(want)) {
		t.Fatalf("message mismatch\n got: %v\nwant: %v", norm(last), norm(want))
	}
}

// with returns base plus the extra fields.
func with(base msg, extra msg) msg {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func convertMsg(typ string, isEncode bool, extra msg) msg {
	return with(msg{"method": "convert", "type": typ, "isEncode": isEncode}, extra)
}

func htmlMsg(fn string, extra msg) msg  { return with(msg{"method": "html", "function": fn}, extra) }
func uiMsg(fn string, extra msg) msg    { return with(msg{"method": "UI", "function": fn}, extra) }
func imageMsg(fn string, extra msg) msg { return with(msg{"method": "image", "function": fn}, extra) }

func httpMsg(method string, extra msg) msg {
	return with(msg{"method": "http", "http_method": method, "url": "https://x/y", "bytes": false}, extra)
}

// (a) every global and member exists with the right type.

func TestSurfaceGlobals(t *testing.T) {
	h := newHost(t, nil)
	types := map[string]string{
		"Convert": "object", "createUuid": "function", "randomInt": "function",
		"randomDouble": "function", "setTimeout": "function", "setInterval": "function",
		"clearTimeout": "function", "clearInterval": "function", "Cookie": "function",
		"Network": "object", "fetch": "function", "HtmlDocument": "function",
		"HtmlElement": "function", "HtmlNode": "function", "log": "function",
		"console": "object", "Comic": "function", "ComicDetails": "function",
		"Comment": "function", "ImageLoadingConfig": "function", "ComicSource": "function",
		"Image": "function", "UI": "object", "APP": "object", "setClipboard": "function",
		"getClipboard": "function", "compute": "function",
	}
	for name, want := range types {
		if got := h.run("typeof " + name); got != want {
			t.Errorf("typeof %s = %v, want %s", name, got, want)
		}
	}
}

func TestSurfaceMembers(t *testing.T) {
	h := newHost(t, nil)
	funcs := map[string][]string{
		"Convert": {
			"encodeUtf8", "decodeUtf8", "encodeGbk", "decodeGbk", "encodeBase64", "decodeBase64",
			"md5", "sha1", "sha256", "sha512", "hmac", "hmacString",
			"encryptAesEcb", "decryptAesEcb", "encryptAesCbc", "decryptAesCbc",
			"encryptAesCfb", "decryptAesCfb", "encryptAesOfb", "decryptAesOfb",
			"decryptRsa", "hexEncode",
		},
		"Network": {
			"fetchBytes", "sendRequest", "get", "post", "put", "patch", "delete",
			"setCookies", "getCookies", "deleteCookies",
		},
		"UI": {
			"showMessage", "showDialog", "launchUrl", "showLoading", "cancelLoading",
			"showInputDialog", "showSelectDialog",
		},
		"HtmlDocument.prototype": {"dispose", "querySelector", "querySelectorAll", "getElementById"},
		"HtmlElement.prototype":  {"querySelector", "querySelectorAll"},
		"HtmlNode.prototype":     {"toElement"},
		"Image.prototype":        {"copyRange", "copyAndRotate90", "fillImageAt", "fillImageRangeAt"},
		"Image":                  {"empty"},
		"ComicSource.prototype":  {"loadData", "loadSetting", "saveData", "deleteData", "translate", "init"},
	}
	for owner, names := range funcs {
		for _, n := range names {
			if got := h.run(fmt.Sprintf("typeof %s[%q]", owner, n)); got != "function" {
				t.Errorf("typeof %s.%s = %v, want function", owner, n, got)
			}
		}
	}
	getters := map[string][]string{
		"HtmlElement.prototype": {
			"text", "attributes", "innerHTML", "children", "nodes", "parent",
			"previousElementSibling", "nextElementSibling", "classNames", "id", "localName",
		},
		"HtmlNode.prototype":    {"text", "type"},
		"Image.prototype":       {"width", "height"},
		"ComicSource.prototype": {"isLogged"},
		"APP":                   {"version", "locale", "platform"},
	}
	for owner, names := range getters {
		for _, n := range names {
			src := fmt.Sprintf("(function(){var d=Object.getOwnPropertyDescriptor(%s,%q);return !!(d&&typeof d.get==='function')})()", owner, n)
			if got := h.run(src); got != true {
				t.Errorf("%s.%s is not a getter", owner, n)
			}
		}
	}
	if got := h.run("Object.keys(Convert).length"); got != float64(22) {
		t.Errorf("Convert has %v members, want 22", got)
	}
	if got := h.run("typeof ComicSource.sources + ':' + Object.keys(ComicSource.sources).length"); got != "object:0" {
		t.Errorf("ComicSource.sources = %v", got)
	}
	if n := len(h.take()); n != 0 {
		t.Errorf("reflection sent %d messages", n)
	}
}

func TestInitDoesNotDefineHostGlobals(t *testing.T) {
	rt := moejs.NewRuntime(moejs.Options{})
	s, err := moejs.CompileScript("init.js", initJS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RunScript(s); err != nil {
		t.Fatalf("loading init.js without a host: %v", err)
	}
	probe, err := moejs.CompileScript("p.js", "typeof sendMessage + ',' + typeof appVersion")
	if err != nil {
		t.Fatal(err)
	}
	v, err := rt.RunScript(probe)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); got != "undefined,undefined" {
		t.Fatalf("init.js defined host globals: %s", got)
	}
}

func TestInitHeaderStatesOrigin(t *testing.T) {
	head := initJS
	if len(head) > 600 {
		head = head[:600]
	}
	if !strings.Contains(head, "original") || !strings.Contains(head, "SENDMESSAGE-SPEC") {
		t.Fatalf("header comment must say the file is original and written from the spec:\n%s", head)
	}
}

// (b) wrappers send exactly the spec's message fields.

func TestWrapperMessages(t *testing.T) {
	hdr := `{h:"1"}`
	src := `new (class extends ComicSource { key = "k1" })()`
	cases := []struct {
		name string
		js   string
		want msg
	}{
		// 1.1 - 1.4
		{"delay", `setTimeout(function(){}, 25)`, msg{"method": "delay", "time": 25}},
		{"uuid", `createUuid()`, msg{"method": "uuid"}},
		{"random int", `randomInt(2, 9)`, msg{"method": "random", "type": "int", "min": 2, "max": 9}},
		{"random double", `randomDouble(0.5, 1.5)`, msg{"method": "random", "type": "double", "min": 0.5, "max": 1.5}},
		{"random defaults", `randomInt()`, msg{"method": "random", "type": "int"}},
		{"log", `log("warning", "T", "C")`, msg{"method": "log", "level": "warning", "title": "T", "content": "C"}},
		{"console.log", `console.log("hi")`, msg{"method": "log", "level": "info", "title": "console", "content": "hi"}},
		{"console.warn", `console.warn("hi")`, msg{"method": "log", "level": "warning", "title": "console", "content": "hi"}},
		{"console.error", `console.error("hi")`, msg{"method": "log", "level": "error", "title": "console", "content": "hi"}},
		{"console joins extra args", `console.log("a", 2, {b:1})`, msg{"method": "log", "level": "info", "title": "console", "content": `a 2 {"b":1}`}},

		// 1.5 convert
		{"encodeUtf8", `Convert.encodeUtf8("v")`, convertMsg("utf8", true, msg{"value": "v"})},
		{"decodeUtf8", `Convert.decodeUtf8("v")`, convertMsg("utf8", false, msg{"value": "v"})},
		{"encodeGbk", `Convert.encodeGbk("v")`, convertMsg("gbk", true, msg{"value": "v"})},
		{"decodeGbk", `Convert.decodeGbk("v")`, convertMsg("gbk", false, msg{"value": "v"})},
		{"encodeBase64", `Convert.encodeBase64("v")`, convertMsg("base64", true, msg{"value": "v"})},
		{"decodeBase64", `Convert.decodeBase64("v")`, convertMsg("base64", false, msg{"value": "v"})},
		{"md5", `Convert.md5("v")`, convertMsg("md5", true, msg{"value": "v"})},
		{"sha1", `Convert.sha1("v")`, convertMsg("sha1", true, msg{"value": "v"})},
		{"sha256", `Convert.sha256("v")`, convertMsg("sha256", true, msg{"value": "v"})},
		{"sha512", `Convert.sha512("v")`, convertMsg("sha512", true, msg{"value": "v"})},
		{"hmac", `Convert.hmac("k", "v", "sha256")`, convertMsg("hmac", true, msg{"key": "k", "value": "v", "hash": "sha256", "isString": false})},
		{"hmacString", `Convert.hmacString("k", "v", "md5")`, convertMsg("hmac", true, msg{"key": "k", "value": "v", "hash": "md5", "isString": true})},
		{"encryptAesEcb", `Convert.encryptAesEcb("v", "k")`, convertMsg("aes-ecb", true, msg{"value": "v", "key": "k"})},
		{"decryptAesEcb", `Convert.decryptAesEcb("v", "k")`, convertMsg("aes-ecb", false, msg{"value": "v", "key": "k"})},
		{"encryptAesCbc", `Convert.encryptAesCbc("v", "k", "i")`, convertMsg("aes-cbc", true, msg{"value": "v", "key": "k", "iv": "i"})},
		{"decryptAesCbc", `Convert.decryptAesCbc("v", "k", "i")`, convertMsg("aes-cbc", false, msg{"value": "v", "key": "k", "iv": "i"})},
		{"encryptAesCfb", `Convert.encryptAesCfb("v", "k", "i", 8)`, convertMsg("aes-cfb", true, msg{"value": "v", "key": "k", "iv": "i", "blockSize": 8})},
		{"decryptAesCfb", `Convert.decryptAesCfb("v", "k", "i", 8)`, convertMsg("aes-cfb", false, msg{"value": "v", "key": "k", "iv": "i", "blockSize": 8})},
		{"encryptAesOfb without iv", `Convert.encryptAesOfb("v", "k", 8)`, convertMsg("aes-ofb", true, msg{"value": "v", "key": "k", "blockSize": 8})},
		{"decryptAesOfb without iv", `Convert.decryptAesOfb("v", "k", 8)`, convertMsg("aes-ofb", false, msg{"value": "v", "key": "k", "blockSize": 8})},
		{"encryptAesOfb with iv", `Convert.encryptAesOfb("v", "k", 8, "i")`, convertMsg("aes-ofb", true, msg{"value": "v", "key": "k", "blockSize": 8, "iv": "i"})},
		{"decryptAesOfb with iv", `Convert.decryptAesOfb("v", "k", 8, "i")`, convertMsg("aes-ofb", false, msg{"value": "v", "key": "k", "blockSize": 8, "iv": "i"})},
		{"decryptRsa", `Convert.decryptRsa("v", "k")`, convertMsg("rsa", false, msg{"value": "v", "key": "k"})},

		// 1.8 compute
		{"compute string", `compute("(a)=>a", 1, "x")`, msg{"method": "compute", "function": "(a)=>a", "args": []any{1, "x"}}},
		{"compute function", `compute(function (a, b) { return a + b; }, 1, 2)`, msg{"method": "compute", "function": "function (a, b) { return a + b; }", "args": []any{1, 2}}},

		// 2 storage
		{"load_data", src + `.loadData("d")`, msg{"method": "load_data", "key": "k1", "data_key": "d"}},
		{"save_data", src + `.saveData("d", {a: 1})`, msg{"method": "save_data", "key": "k1", "data_key": "d", "data": msg{"a": 1}}},
		{"delete_data", src + `.deleteData("d")`, msg{"method": "delete_data", "key": "k1", "data_key": "d"}},
		{"load_setting", src + `.loadSetting("s")`, msg{"method": "load_setting", "key": "k1", "setting_key": "s"}},
		{"isLogged", src + `.isLogged`, msg{"method": "isLogged", "key": "k1"}},

		// 3 network
		{"sendRequest", `Network.sendRequest("PUT", "https://x/y", ` + hdr + `, "body", {e: 1})`,
			httpMsg("PUT", msg{"headers": msg{"h": "1"}, "data": "body", "extra": msg{"e": 1}})},
		{"sendRequest minimal", `Network.sendRequest("GET", "https://x/y")`, httpMsg("GET", nil)},
		{"fetchBytes", `Network.fetchBytes("GET", "https://x/y", ` + hdr + `)`,
			httpMsg("GET", msg{"bytes": true, "headers": msg{"h": "1"}})},
		{"post", `Network.post("https://x/y", ` + hdr + `, "d", {e: 1})`,
			httpMsg("POST", msg{"headers": msg{"h": "1"}, "data": "d", "extra": msg{"e": 1}})},
		{"put", `Network.put("https://x/y", ` + hdr + `, "d")`,
			httpMsg("PUT", msg{"headers": msg{"h": "1"}, "data": "d"})},
		{"patch", `Network.patch("https://x/y", ` + hdr + `, "d")`,
			httpMsg("PATCH", msg{"headers": msg{"h": "1"}, "data": "d"})},
		{"get", `Network.get("https://x/y", ` + hdr + `)`,
			httpMsg("GET", msg{"headers": msg{"h": "1"}})},
		{"delete", `Network.delete("https://x/y", ` + hdr + `)`,
			httpMsg("DELETE", msg{"headers": msg{"h": "1"}})},
		{"setCookies", `Network.setCookies("https://x/", [new Cookie({name: "a", value: "b", domain: "x"})])`,
			msg{"method": "cookie", "function": "set", "url": "https://x/", "cookies": []any{msg{"name": "a", "value": "b", "domain": "x"}}}},
		{"getCookies", `Network.getCookies("https://x/")`, msg{"method": "cookie", "function": "get", "url": "https://x/"}},
		{"deleteCookies", `Network.deleteCookies("https://x/")`, msg{"method": "cookie", "function": "delete", "url": "https://x/"}},
		{"fetch", `fetch("https://x/y", {method: "POST", headers: ` + hdr + `, body: "b"})`,
			httpMsg("POST", msg{"bytes": true, "headers": msg{"h": "1"}, "data": "b"})},
		{"fetch default method", `fetch("https://x/y")`, httpMsg("GET", msg{"bytes": true})},

		// 4 html
		{"html parse", `new HtmlDocument("<p>")`, htmlMsg("parse", msg{"key": 0, "data": "<p>"})},
		{"html dispose", `new HtmlDocument("<p>").dispose()`, htmlMsg("dispose", msg{"key": 0})},
		{"html querySelector", `new HtmlDocument("<p>").querySelector("p")`, htmlMsg("querySelector", msg{"key": 0, "query": "p"})},
		{"html querySelectorAll", `new HtmlDocument("<p>").querySelectorAll("p")`, htmlMsg("querySelectorAll", msg{"key": 0, "query": "p"})},
		{"html getElementById", `new HtmlDocument("<p>").getElementById("i")`, htmlMsg("getElementById", msg{"key": 0, "id": "i"})},
		{"html dom_querySelector", `new HtmlElement(2, 5).querySelector("a")`, htmlMsg("dom_querySelector", msg{"doc": 2, "key": 5, "query": "a"})},
		{"html dom_querySelectorAll", `new HtmlElement(2, 5).querySelectorAll("a")`, htmlMsg("dom_querySelectorAll", msg{"doc": 2, "key": 5, "query": "a"})},
		{"html getText", `new HtmlElement(2, 5).text`, htmlMsg("getText", msg{"doc": 2, "key": 5})},
		{"html getAttributes", `new HtmlElement(2, 5).attributes`, htmlMsg("getAttributes", msg{"doc": 2, "key": 5})},
		{"html getInnerHTML", `new HtmlElement(2, 5).innerHTML`, htmlMsg("getInnerHTML", msg{"doc": 2, "key": 5})},
		{"html getChildren", `new HtmlElement(2, 5).children`, htmlMsg("getChildren", msg{"doc": 2, "key": 5})},
		{"html getNodes", `new HtmlElement(2, 5).nodes`, htmlMsg("getNodes", msg{"doc": 2, "key": 5})},
		{"html getParent", `new HtmlElement(2, 5).parent`, htmlMsg("getParent", msg{"doc": 2, "key": 5})},
		{"html getPreviousSibling", `new HtmlElement(2, 5).previousElementSibling`, htmlMsg("getPreviousSibling", msg{"doc": 2, "key": 5})},
		{"html getNextSibling", `new HtmlElement(2, 5).nextElementSibling`, htmlMsg("getNextSibling", msg{"doc": 2, "key": 5})},
		{"html getClassNames", `new HtmlElement(2, 5).classNames`, htmlMsg("getClassNames", msg{"doc": 2, "key": 5})},
		{"html getId", `new HtmlElement(2, 5).id`, htmlMsg("getId", msg{"doc": 2, "key": 5})},
		{"html getLocalName", `new HtmlElement(2, 5).localName`, htmlMsg("getLocalName", msg{"doc": 2, "key": 5})},
		{"html node_text", `new HtmlNode(2, 5).text`, htmlMsg("node_text", msg{"doc": 2, "key": 5})},
		{"html node_type", `new HtmlNode(2, 5).type`, htmlMsg("node_type", msg{"doc": 2, "key": 5})},
		{"html node_to_element (quirk 5)", `new HtmlNode(2, 5).toElement()`, htmlMsg("node_to_element", msg{"doc": 2, "key": 5})},

		// 5 image
		{"image copyRange", `new Image(5).copyRange(1, 2, 3, 4)`, imageMsg("copyRange", msg{"key": 5, "x": 1, "y": 2, "width": 3, "height": 4})},
		{"image copyAndRotate90", `new Image(5).copyAndRotate90()`, imageMsg("copyAndRotate90", msg{"key": 5})},
		{"image fillImageAt", `new Image(5).fillImageAt(1, 2, new Image(9))`, imageMsg("fillImageAt", msg{"key": 5, "x": 1, "y": 2, "image": 9})},
		{"image fillImageRangeAt", `new Image(5).fillImageRangeAt(1, 2, new Image(9), 3, 4, 5, 6)`,
			imageMsg("fillImageRangeAt", msg{"key": 5, "x": 1, "y": 2, "image": 9, "srcX": 3, "srcY": 4, "width": 5, "height": 6})},
		{"image getWidth", `new Image(5).width`, imageMsg("getWidth", msg{"key": 5})},
		{"image getHeight", `new Image(5).height`, imageMsg("getHeight", msg{"key": 5})},
		{"image emptyImage", `Image.empty(10, 20)`, imageMsg("emptyImage", msg{"width": 10, "height": 20})},

		// 6 environment and UI
		{"getLocale", `APP.locale`, msg{"method": "getLocale"}},
		{"getPlatform", `APP.platform`, msg{"method": "getPlatform"}},
		{"setClipboard", `setClipboard("t")`, msg{"method": "setClipboard", "text": "t"}},
		{"getClipboard", `getClipboard()`, msg{"method": "getClipboard"}},
		{"ui showMessage", `UI.showMessage("m")`, uiMsg("showMessage", msg{"message": "m"})},
		{"ui showDialog", `UI.showDialog("T", "C", [{text: "ok", callback: function () {}, style: "text"}])`,
			uiMsg("showDialog", msg{"title": "T", "content": "C", "actions": []any{msg{"text": "ok", "callback": "<func>", "style": "text"}}})},
		{"ui launchUrl", `UI.launchUrl("https://x/")`, uiMsg("launchUrl", msg{"url": "https://x/"})},
		{"ui showLoading", `UI.showLoading(function () {})`, uiMsg("showLoading", msg{"onCancel": "<func>"})},
		{"ui showLoading null", `UI.showLoading()`, uiMsg("showLoading", msg{"onCancel": nil})},
		{"ui cancelLoading", `UI.cancelLoading(4)`, uiMsg("cancelLoading", msg{"id": 4})},
		{"ui showInputDialog", `UI.showInputDialog("T", function () {}, "img")`, uiMsg("showInputDialog", msg{"title": "T", "validator": "<func>", "image": "img"})},
		{"ui showSelectDialog", `UI.showSelectDialog("T", ["a", "b"], 1)`, uiMsg("showSelectDialog", msg{"title": "T", "options": []any{"a", "b"}, "initialIndex": 1})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHost(t, nil)
			h.run(c.js)
			wantMsg(t, h.take(), c.want)
		})
	}
}

// Every method and function name of the spec tables is sent by some wrapper.
func TestWrapperCoversSpecMethods(t *testing.T) {
	h := newHost(t, nil)
	seen := map[string]bool{}
	for _, src := range []string{
		`setTimeout(function(){}, 1)`, `createUuid()`, `randomInt(1, 2)`, `log("info", "a", "b")`,
		`Convert.md5("x")`, `compute("(a)=>a")`,
		`var s = new (class extends ComicSource { key = "k" })(); s.loadData("a"); s.saveData("a", 1); s.deleteData("a"); s.loadSetting("a"); s.isLogged`,
		`Network.get("u")`, `Network.getCookies("u")`, `Network.setCookies("u", [])`, `Network.deleteCookies("u")`,
		`APP.locale`, `APP.platform`, `setClipboard("a")`, `getClipboard()`,
		`UI.showMessage("a")`, `UI.showDialog("a", "b", [])`, `UI.launchUrl("a")`, `UI.showLoading()`,
		`UI.cancelLoading(1)`, `UI.showInputDialog("a")`, `UI.showSelectDialog("a", [])`,
		`var d = new HtmlDocument("x"); d.querySelector("a"); d.querySelectorAll("a"); d.getElementById("a"); d.dispose()`,
		`var e = new HtmlElement(0, 0); e.querySelector("a"); e.querySelectorAll("a"); e.text; e.attributes; e.innerHTML; e.children; e.nodes; e.parent; e.previousElementSibling; e.nextElementSibling; e.classNames; e.id; e.localName`,
		`var n = new HtmlNode(0, 0); n.text; n.type; n.toElement()`,
		`var i = new Image(1); i.copyRange(0, 0, 1, 1); i.copyAndRotate90(); i.fillImageAt(0, 0, i); i.fillImageRangeAt(0, 0, i, 0, 0, 1, 1); i.width; i.height; Image.empty(1, 1)`,
	} {
		h.run(src)
		for _, m := range h.take() {
			key, _ := m["method"].(string)
			if f, ok := m["function"].(string); ok && key != "compute" {
				key += "/" + f
			}
			seen[key] = true
		}
	}
	want := []string{
		"delay", "uuid", "random", "log", "convert", "compute",
		"load_data", "save_data", "delete_data", "load_setting", "isLogged", "http",
		"cookie/get", "cookie/set", "cookie/delete", "getLocale", "getPlatform", "setClipboard", "getClipboard",
		"UI/showMessage", "UI/showDialog", "UI/launchUrl", "UI/showLoading", "UI/cancelLoading",
		"UI/showInputDialog", "UI/showSelectDialog",
		"html/parse", "html/dispose", "html/querySelector", "html/querySelectorAll", "html/getElementById",
		"html/dom_querySelector", "html/dom_querySelectorAll", "html/getText", "html/getAttributes",
		"html/getInnerHTML", "html/getChildren", "html/getNodes", "html/getParent", "html/getPreviousSibling",
		"html/getNextSibling", "html/getClassNames", "html/getId", "html/getLocalName",
		"html/node_text", "html/node_type", "html/node_to_element",
		"image/copyRange", "image/copyAndRotate90", "image/fillImageAt", "image/fillImageRangeAt",
		"image/getWidth", "image/getHeight", "image/emptyImage",
	}
	var missing []string
	for _, k := range want {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("spec methods never sent: %v", missing)
	}
}

// (c) HTML getters return host values, handles become wrapped objects.

func htmlHost(t *testing.T) *fakeHost {
	return newHost(t, func(m msg) (any, error) {
		if m["method"] != "html" {
			return defaultReply(m)
		}
		switch m["function"] {
		case "querySelector", "dom_querySelector", "getParent":
			return 7, nil
		case "querySelectorAll", "dom_querySelectorAll", "getChildren":
			return []any{0, 3}, nil
		case "getNodes":
			return []any{4, 5}, nil
		case "getElementById", "getNextSibling":
			return nil, nil
		case "getPreviousSibling":
			return 0, nil
		case "getText", "node_text":
			return "hello", nil
		case "getAttributes":
			return msg{"href": "/a"}, nil
		case "getInnerHTML":
			return "<b>x</b>", nil
		case "getClassNames":
			return []any{"c1", "c2"}, nil
		case "getId":
			return "ident", nil
		case "getLocalName":
			return "div", nil
		case "node_type":
			return "text", nil
		case "node_to_element":
			return 11, nil
		}
		return nil, nil
	})
}

func TestHtmlGettersWrapHandles(t *testing.T) {
	h := htmlHost(t)
	checks := []struct {
		js   string
		want any
	}{
		{`(function(){var d=new HtmlDocument("x");var e=d.querySelector("p");return (e instanceof HtmlElement)+"@"+(e.doc===d.key)+":"+e.key})()`, "true@true:7"},
		{`new HtmlDocument("x").getElementById("q")`, nil},
		{`(function(){var d=new HtmlDocument("x");return d.querySelectorAll("p").map(function(e){return (e instanceof HtmlElement)+"@"+(e.doc===d.key)+":"+e.key}).join()})()`, "true@true:0,true@true:3"},
		{`(function(){var e=new HtmlElement(4,1).querySelector("a");return (e instanceof HtmlElement)+"@"+e.doc+":"+e.key})()`, "true@4:7"},
		{`new HtmlElement(4,1).querySelectorAll("a").map(function(e){return e.doc+":"+e.key}).join()`, "4:0,4:3"},
		{`new HtmlElement(4,1).children.map(function(e){return (e instanceof HtmlElement)+"@"+e.doc+":"+e.key}).join()`, "true@4:0,true@4:3"},
		{`new HtmlElement(4,1).nodes.map(function(n){return (n instanceof HtmlNode)+"@"+n.doc+":"+n.key}).join()`, "true@4:4,true@4:5"},
		{`(function(){var p=new HtmlElement(4,1).parent;return (p instanceof HtmlElement)+"@"+p.doc+":"+p.key})()`, "true@4:7"},
		{`new HtmlElement(4,1).nextElementSibling`, nil},
		// Handle 0 is a valid handle and must not be treated as "none".
		{`(function(){var p=new HtmlElement(4,1).previousElementSibling;return p.doc+":"+p.key})()`, "4:0"},
		{`new HtmlElement(4,1).text`, "hello"},
		{`JSON.stringify(new HtmlElement(4,1).attributes)`, `{"href":"/a"}`},
		{`new HtmlElement(4,1).innerHTML`, "<b>x</b>"},
		{`new HtmlElement(4,1).classNames.join()`, "c1,c2"},
		{`new HtmlElement(4,1).id`, "ident"},
		{`new HtmlElement(4,1).localName`, "div"},
		{`new HtmlNode(4,1).text`, "hello"},
		{`new HtmlNode(4,1).type`, "text"},
		{`(function(){var e=new HtmlNode(4,1).toElement();return (e instanceof HtmlElement)+"@"+e.doc+":"+e.key})()`, "true@4:11"},
	}
	for _, c := range checks {
		if got := h.run(c.js); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s\n got %#v\nwant %#v", c.js, got, c.want)
		}
	}
}

func TestHtmlNodeToElementNull(t *testing.T) {
	h := newHost(t, func(m msg) (any, error) { return nil, nil })
	if got := h.run(`new HtmlNode(1, 2).toElement()`); got != nil {
		t.Fatalf("got %v, want null", got)
	}
}

// Quirk 8: document keys are a global counter and are never reused.
func TestHtmlDocumentKeysIncrease(t *testing.T) {
	h := newHost(t, nil)
	got := h.run(`(function(){var a=new HtmlDocument("a");a.dispose();var b=new HtmlDocument("b");var c=new HtmlDocument("c");return [a.key,b.key,c.key].join()})()`)
	if got != "0,1,2" {
		t.Fatalf("keys = %v", got)
	}
}

// (d) ComicSource subclass round trip.

func TestComicSourceRoundTrip(t *testing.T) {
	h := newHost(t, func(m msg) (any, error) {
		switch m["method"] {
		case "load_data":
			return msg{"user": "u"}, nil
		case "load_setting":
			return 42, nil
		case "isLogged":
			return true, nil
		}
		return defaultReply(m)
	})
	h.run(`
class Demo extends ComicSource {
  name = "Demo";
  key = "demo_1";
  version = "1.0.0";
  minAppVersion = "1.0.0";
  url = "https://example.com/demo.js";
  translation = { zh_CN: { Hello: "你好" }, en: { Hello: "Hi" } };
}
globalThis.s = new Demo();
ComicSource.sources[s.key] = s;
`)
	h.take()

	if got := h.run(`s instanceof ComicSource && ComicSource.sources.demo_1 === s`); got != true {
		t.Fatal("registry does not hold the instance")
	}
	if got := h.run(`[s.name, s.key, s.version, s.minAppVersion, s.url].join("|")`); got != "Demo|demo_1|1.0.0|1.0.0|https://example.com/demo.js" {
		t.Fatalf("fields = %v", got)
	}
	if got := h.run(`JSON.stringify(s.loadData("account"))`); got != `{"user":"u"}` {
		t.Fatalf("loadData = %v", got)
	}
	wantMsg(t, h.take(), msg{"method": "load_data", "key": "demo_1", "data_key": "account"})

	h.run(`s.saveData("k", [1, 2])`)
	wantMsg(t, h.take(), msg{"method": "save_data", "key": "demo_1", "data_key": "k", "data": []any{1, 2}})

	h.run(`s.deleteData("k")`)
	wantMsg(t, h.take(), msg{"method": "delete_data", "key": "demo_1", "data_key": "k"})

	if got := h.run(`s.loadSetting("quality")`); got != float64(42) {
		t.Fatalf("loadSetting = %v", got)
	}
	wantMsg(t, h.take(), msg{"method": "load_setting", "key": "demo_1", "setting_key": "quality"})

	if got := h.run(`s.isLogged`); got != true {
		t.Fatalf("isLogged = %v", got)
	}
	wantMsg(t, h.take(), msg{"method": "isLogged", "key": "demo_1"})

	// translate reads the locale from the host, then looks the text up.
	if got := h.run(`s.translate("Hello")`); got != "你好" {
		t.Fatalf("translate = %v", got)
	}
	wantMsg(t, h.take(), msg{"method": "getLocale"})
	if got := h.run(`s.translate("Missing")`); got != "Missing" {
		t.Fatalf("translate fallback = %v", got)
	}
	h.take()
}

func TestComicSourceDefaults(t *testing.T) {
	h := newHost(t, nil)
	got := h.run(`(function(){var s=new ComicSource();return JSON.stringify([s.name,s.key,s.translation])})()`)
	if got != `["","",{}]` {
		t.Fatalf("defaults = %v", got)
	}
	// init is overridable; subclasses may make it async.
	if got := h.run(`(function(){class A extends ComicSource{ async init(){ return 5 } }; return typeof new A().init().then})()`); got != "function" {
		t.Fatalf("init override: %v", got)
	}
	if got := h.run(`new ComicSource().init()`); got != nil {
		t.Fatalf("base init = %v", got)
	}
	if got := h.run(`Object.getPrototypeOf(ComicSource.sources) === Object.prototype`); got != true {
		t.Fatal("static sources must be a plain object")
	}
}

// (e) data shapes.

func TestComicDetailsSubtitle(t *testing.T) {
	h := newHost(t, nil)
	cases := []struct{ js, want string }{
		{`new ComicDetails({subtitle: "a", subTitle: "b"}).subtitle`, "a"},
		{`new ComicDetails({subTitle: "b"}).subtitle`, "b"},
		{`String(new ComicDetails({}).subtitle)`, "undefined"},
		{`new ComicDetails({subtitle: "", subTitle: "b"}).subtitle + "|"`, "|"}, // ?? keeps ""
		{`String(new ComicDetails({subtitle: null, subTitle: "b"}).subtitle)`, "b"},
	}
	for _, c := range cases {
		if got := h.run(c.js); got != c.want {
			t.Errorf("%s = %v, want %s", c.js, got, c.want)
		}
	}
	got := h.run(`JSON.stringify(new ComicDetails({title:"T",cover:"C",description:"D",tags:{a:["x"]},chapters:{"1":"c"},isFavorite:true,subId:"s",thumbnails:["t"],recommend:[1],commentCount:3,likesCount:4,isLiked:false,uploader:"u",updateTime:"ut",uploadTime:"up",url:"U",stars:4.5,maxPage:9,comments:[]}))`)
	want := `{"title":"T","cover":"C","description":"D","tags":{"a":["x"]},"chapters":{"1":"c"},"isFavorite":true,"subId":"s","thumbnails":["t"],"recommend":[1],"commentCount":3,"likesCount":4,"isLiked":false,"uploader":"u","updateTime":"ut","uploadTime":"up","url":"U","stars":4.5,"maxPage":9,"comments":[]}`
	if got != want {
		t.Errorf("ComicDetails json\n got %v\nwant %v", got, want)
	}
	// A field outside the known list is kept rather than dropped.
	if got := h.run(`new ComicDetails({title: "T", extraField: 5}).extraField`); got != float64(5) {
		t.Errorf("unlisted field = %v", got)
	}
}

func TestDataShapes(t *testing.T) {
	h := newHost(t, nil)
	cases := []struct{ js, want string }{
		{`JSON.stringify(new Comic({id:"1",title:"t",subtitle:"s",subTitle:"S",cover:"c",tags:["a"],description:"d",maxPage:3,language:"zh",favoriteId:"f",stars:5}))`,
			`{"id":"1","title":"t","subtitle":"s","subTitle":"S","cover":"c","tags":["a"],"description":"d","maxPage":3,"language":"zh","favoriteId":"f","stars":5}`},
		{`JSON.stringify(new Comment({userName:"u",avatar:"a",content:"c",time:"t",replyCount:2,id:"i",isLiked:true,score:7,voteStatus:1}))`,
			`{"userName":"u","avatar":"a","content":"c","time":"t","replyCount":2,"id":"i","isLiked":true,"score":7,"voteStatus":1}`},
		{`(function(){var f=function(){};var c=new ImageLoadingConfig({url:"u",method:"POST",data:"d",headers:{a:"b"},onResponse:f,modifyImage:"m",onLoadFailed:f});return [c.url,c.method,c.data,c.headers.a,c.onResponse===f,c.modifyImage,c.onLoadFailed===f].join()})()`,
			`u,POST,d,b,true,m,true`},
		{`JSON.stringify(new Cookie({name:"n",value:"v",domain:"d"}))`, `{"name":"n","value":"v","domain":"d"}`},
		{`String(new Comic({}).title === undefined && new Comment().id === undefined && new ImageLoadingConfig().url === undefined)`, `true`},
	}
	for _, c := range cases {
		if got := fmt.Sprint(h.run(c.js)); got != c.want {
			t.Errorf("%s\n got %v\nwant %v", c.js, got, c.want)
		}
	}
}

// (f) quirk 1: get/delete treat the third argument as request data.

func TestNetworkGetDeleteQuirk(t *testing.T) {
	for _, verb := range []string{"get", "delete"} {
		t.Run(verb, func(t *testing.T) {
			h := newHost(t, nil)
			h.run(fmt.Sprintf(`Network.%s("https://x/y", {h: "1"}, {x: 1})`, verb))
			got := h.take()
			wantMsg(t, got, httpMsg(strings.ToUpper(verb), msg{"headers": msg{"h": "1"}, "data": msg{"x": 1}}))
			if _, ok := got[0]["extra"]; ok {
				t.Fatal("extra must not be forwarded by get/delete")
			}
		})
	}
	// post keeps data and extra apart.
	h := newHost(t, nil)
	h.run(`Network.post("https://x/y", {}, "d", {x: 1})`)
	m := h.take()[0]
	if m["data"] != "d" || m["extra"] == nil {
		t.Fatalf("post message = %v", m)
	}
}

// Network results and errors.

func TestNetworkResults(t *testing.T) {
	h := newHost(t, nil)
	h.run(`globalThis.r = null; Network.get("https://x/y").then(function (v) { r = v; })`)
	if got := h.run(`r.status + ":" + r.headers.a`); got != "200:b" {
		t.Fatalf("response = %v", got)
	}

	failing := newHost(t, func(m msg) (any, error) {
		if m["method"] == "http" {
			return asyncReply{msg{"status": nil, "error": "boom"}}, nil
		}
		return nil, nil
	})
	failing.run(`globalThis.e = null; Network.get("https://x/y").catch(function (x) { e = x; })`)
	if got := failing.run(`e`); got != "boom" {
		t.Fatalf("rejection = %v, want boom", got)
	}
}

// Quirk 7: fetch.

func TestFetchShape(t *testing.T) {
	h := newHost(t, func(m msg) (any, error) {
		switch m["method"] {
		case "http":
			return asyncReply{msg{"status": 201, "headers": msg{"k": "v"}, "body": []byte(`{"a":1}`), "error": nil}}, nil
		case "convert":
			return `{"a":1}`, nil
		}
		return nil, nil
	})
	h.run(`globalThis.out = {};
fetch("https://x/y").then(function (r) {
  out.ok = r.ok; out.status = r.status; out.statusText = r.statusText; out.hk = r.headers.k;
  return r.json();
}).then(function (j) { out.a = j.a; });`)
	if got := h.run(`JSON.stringify(out)`); got != `{"ok":true,"status":201,"statusText":"","hk":"v","a":1}` {
		t.Fatalf("fetch result = %v", got)
	}
	var sawDecode bool
	for _, m := range h.take() {
		if m["method"] == "convert" && m["type"] == "utf8" && m["isEncode"] == false {
			sawDecode = true
		}
	}
	if !sawDecode {
		t.Fatal("json() must go through Convert.decodeUtf8")
	}
}

// Timers.

func TestTimers(t *testing.T) {
	h := newHost(t, nil)
	h.run(`globalThis.fired = 0; var t = setTimeout(function () { fired++; }, 30); globalThis.hasCancel = typeof t.cancel;`)
	if got := h.run(`fired + ":" + hasCancel`); got != "1:function" {
		t.Fatalf("setTimeout: %v", got)
	}
	if m := h.take(); len(m) != 1 || m[0]["time"] != int64(30) {
		t.Fatalf("delay messages: %v", m)
	}

	// setInterval: the first call is immediate, later calls wait for the host.
	h.run(`globalThis.n = 0; globalThis.seenAtStart = -1;
globalThis.iv = setInterval(function () { n++; if (n === 3) iv.cancel(); }, 10);
seenAtStart = n;`)
	if got := h.run(`seenAtStart + ":" + n`); got != "1:3" {
		t.Fatalf("setInterval: %v", got)
	}
	if m := h.take(); len(m) != 2 {
		t.Fatalf("interval should have waited twice, got %v", m)
	}

	// clearTimeout and clearInterval (extensions), by handle and by number.
	h.run(`globalThis.f1 = 0; globalThis.f2 = 0; globalThis.f3 = 0;
var a = setTimeout(function () { f1++; }, 5); clearTimeout(a);
var b = setTimeout(function () { f2++; }, 5); clearTimeout(Number(b));
globalThis.c = setInterval(function () { f3++; if (f3 === 2) clearInterval(c); }, 5);`)
	if got := h.run(`f1 + "," + f2 + "," + f3`); got != "0,0,2" {
		t.Fatalf("clear*: %v", got)
	}
	h.take()

	// A throwing callback is logged, not fatal.
	h.run(`setTimeout(function () { throw new Error("cb"); }, 1)`)
	var logged bool
	for _, m := range h.take() {
		if m["method"] == "log" && m["level"] == "error" {
			logged = true
		}
	}
	if !logged {
		t.Fatal("callback error was not logged")
	}
	if err := h.runErr(`setTimeout("x", 1)`); err == nil {
		t.Fatal("non-function callback must throw")
	}
}

// Miscellaneous behaviour.

func TestHexEncode(t *testing.T) {
	h := newHost(t, nil)
	for js, want := range map[string]string{
		`Convert.hexEncode(new Uint8Array([0, 15, 16, 255]).buffer)`: "000f10ff",
		`Convert.hexEncode(new Uint8Array([1, 2]))`:                  "0102",
		`Convert.hexEncode([171, 205])`:                              "abcd",
		`Convert.hexEncode(new ArrayBuffer(0))`:                      "",
	} {
		if got := h.run(js); got != want {
			t.Errorf("%s = %v, want %s", js, got, want)
		}
	}
	if len(h.take()) != 0 {
		t.Fatal("hexEncode must be local")
	}
}

func TestAppVersionAndHostValues(t *testing.T) {
	h := newHost(t, nil)
	if got := h.run(`APP.version`); got != "1.2.3" {
		t.Fatalf("APP.version = %v", got)
	}
	if got := h.run(`APP.locale + "/" + APP.platform`); got != "zh_CN/windows" {
		t.Fatalf("locale/platform = %v", got)
	}
	h.run(`globalThis.clip = ""; getClipboard().then(function (v) { clip = v; })`)
	if got := h.run(`clip`); got != "clip" {
		t.Fatalf("clipboard = %v", got)
	}
}

func TestUUIDAndRandomPassThrough(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	h := newHost(t, func(m msg) (any, error) {
		switch m["method"] {
		case "uuid":
			return "123e4567-e89b-42d3-a456-426614174000", nil
		case "random":
			return 7, nil
		}
		return nil, nil
	})
	if got, _ := h.run(`createUuid()`).(string); !uuid.MatchString(got) {
		t.Fatalf("uuid = %q", got)
	}
	if got := h.run(`randomInt(0, 10) + randomDouble(0, 1)`); got != float64(14) {
		t.Fatalf("random = %v", got)
	}
}

func TestHostErrorsPropagate(t *testing.T) {
	h := newHost(t, func(m msg) (any, error) {
		return nil, errors.New("No JavaScript message handler provided for method: " + fmt.Sprint(m["method"]))
	})
	err := h.runErr(`createUuid()`)
	if err == nil || !strings.Contains(err.Error(), "No JavaScript message handler provided for method: uuid") {
		t.Fatalf("error = %v", err)
	}
}

func TestGlobalsCanBeShadowedBySources(t *testing.T) {
	// A source script may declare top-level names that clash with ours.
	h := newHost(t, nil)
	h.run(`var Comment = 1; function log() {}; globalThis.ok = true;`)
	if got := h.run(`ok`); got != true {
		t.Fatal("source shadowing failed")
	}
}
