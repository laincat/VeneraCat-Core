package js

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Exercise the public Core boundary, rather than just upstream compiler tests.
// These are the limits of the pinned engine and the alternatives available to
// source authors; changing the engine requires updating the documented limits.
func TestCompatibilityReadOnlyAtob(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	if _, err := r.Eval(ctx, `"use strict"; globalThis.atob = function () { return "custom" };`); err == nil || !strings.Contains(err.Error(), "read only property 'atob'") {
		t.Fatalf("strict overwrite: %v", err)
	}
	// A sloppy write is silently ignored; it must not be mistaken for a
	// successfully installed decoder just because evaluation did not throw.
	got, err := r.Eval(ctx, `globalThis.atob = function () { return "custom" }; atob("QQ==")`)
	if err != nil || string(got) != `"A"` {
		t.Fatalf("sloppy overwrite: got %s, err %v", got, err)
	}
	got, err = r.Eval(ctx, `(function () { const atob = function () { return "custom" }; return atob("QQ==") })()`)
	if err != nil || string(got) != `"custom"` {
		t.Fatalf("local decoder: got %s, err %v", got, err)
	}
	got, err = r.Eval(ctx, `atob("QQ==")`)
	if err != nil || string(got) != `"A"` {
		t.Fatalf("local decoder changed the global: got %s, err %v", got, err)
	}
}

func TestCompatibilityCallRegisterWindow(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	literalCall := func(n int) string {
		return `(function (...a) { return a.length })(` + strings.TrimSuffix(strings.Repeat("1,", n), ",") + `)`
	}
	got, err := r.Eval(ctx, literalCall(254))
	if err != nil || string(got) != "254" {
		t.Fatalf("254 literal arguments: got %s, err %v", got, err)
	}
	if _, err := r.Eval(ctx, literalCall(255)); err == nil || !strings.Contains(err.Error(), "256 registers") {
		t.Fatalf("255 literal arguments: %v", err)
	}
	if _, err := r.Eval(ctx, literalCall(256)); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("256 literal arguments: %v", err)
	}
	// The limit is a compiler register window, not a 254-element data cap.
	// A dynamic spread succeeds, and the earlier compile errors leave the
	// runtime usable for the next request.
	got, err = r.Eval(ctx, `(function (...a) { return a.length })(...Array.from({length:1000}, () => 1))`)
	if err != nil || string(got) != "1000" {
		t.Fatalf("1000 spread arguments after compile errors: got %s, err %v", got, err)
	}
}

// A promise rejection is delivered as a value, and only the value's message used
// to survive: "Cannot read properties of null" without the TypeError in front of
// it. A source author reading that has no way to tell an engine TypeError from a
// sentence their own code produced.
func TestAsyncRejectionsKeepTheErrorName(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	_, err = r.Eval(ctx, `(async () => { const x = null; return x.length })()`)
	if err == nil || !strings.Contains(err.Error(), "TypeError: Cannot read properties") {
		t.Fatalf("async rejection = %v", err)
	}
	// A thrown string has no name; it must not grow one.
	_, err = r.Eval(ctx, `(async () => { throw "plain refusal" })()`)
	if err == nil || !strings.Contains(err.Error(), "plain refusal") || strings.Contains(err.Error(), "Error: ") {
		t.Fatalf("string rejection = %v", err)
	}
	// Synchronous throws already carried the name; this keeps it that way.
	_, err = r.Eval(ctx, `(function () { const x = null; return x.length })()`)
	if err == nil || !strings.Contains(err.Error(), "TypeError:") {
		t.Fatalf("sync throw = %v", err)
	}
}

// TextDecoder is the JS builtin's UTF-8 decoder, not the browser's: a source that
// opens a GBK page cannot decode it here. The alternative is the source API's own
// convert route (sendMessage with method "convert"), which goes through Go's
// encoding tables; this test pins both halves so the limit is documented by
// something that fails when it changes.
func TestCompatibilityTextDecoderIsUTF8Only(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()

	got, err := r.Eval(ctx, `new TextDecoder('utf-8').decode(new Uint8Array([0xe4, 0xb8, 0xad]))`)
	if err != nil || string(got) != `"中"` {
		t.Fatalf("utf-8: got %s, err %v", got, err)
	}
	_, err = r.Eval(ctx, `new TextDecoder('gbk').decode(new Uint8Array([0xd6, 0xd0]))`)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("gbk should be refused, got err %v", err)
	}
	// Exercise the source-facing bridge with known GBK bytes, rather than
	// merely checking that sendMessage exists. Typed arrays must survive
	// conversion across the JS/Go boundary in both directions.
	for _, tc := range []struct{ name, script, want string }{
		{"decode typed bytes", `Convert.decodeGbk(new Uint8Array([214,208,206,196]))`, `"中文"`},
		{"decode array", `Convert.decodeGbk([214,208,206,196])`, `"中文"`},
		{"invalid byte replacement", `Convert.decodeGbk(new Uint8Array([97,255,98]))`, `"a�b"`},
		{"truncated sequence replacement", `Convert.decodeGbk(new Uint8Array([214]))`, `"�"`},
		{"encode", `Array.from(new Uint8Array(Convert.encodeGbk("中文")))`, `[214,208,206,196]`},
		{"unrepresentable character", `Convert.encodeGbk("🐾")`, `null`},
		{"decode after failure", `Convert.decodeGbk([214,208])`, `"中"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Eval(ctx, tc.script)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s, want %s, err %v", got, tc.want, err)
			}
		})
	}
}

// A large function compiles: moejs v0.1.0 lifted the 256-local-variable
// ceiling the pinned alpha used to have. Measured on this version: a function
// with 65,536 locals still compiles, so the old "300 locals must fail"
// expectation is no longer true.
//
// The fact is recorded rather than dropped: an engine change that brings the
// ceiling back, or a source that still hits a compiler window, has to show up
// as a test change instead of a surprise on a user's screen. The per-call
// register window is separate and is still covered by
// TestCompatibilityCallRegisterWindow.
func TestCompatibilityLargeFunctionsCompile(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	build := func(n int) string {
		var b strings.Builder
		b.WriteString("(function(){")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "let v%d = %d;", i, i)
		}
		b.WriteString("return 0})()")
		return b.String()
	}
	for _, n := range []int{200, 300, 4096} {
		got, err := r.Eval(ctx, build(n))
		if err != nil {
			t.Fatalf("%d locals should compile: %v", n, err)
		}
		if string(got) != "0" {
			t.Fatalf("%d locals returned %s, want 0", n, got)
		}
	}
}

// Intl is partial: DateTimeFormat exists, Collator does not. A source that
// sorts with `Intl.Collator` gets a TypeError, so the client must not assume a
// full Intl the way a browser has one.
func TestCompatibilityIntlIsPartial(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if out, err := r.Eval(ctx, `typeof Intl.DateTimeFormat`); err != nil || string(out) != `"function"` {
		t.Fatalf("DateTimeFormat: %s %v", out, err)
	}
	if out, err := r.Eval(ctx, `typeof Intl.Collator`); err != nil || string(out) != `"undefined"` {
		t.Fatalf("Collator: %s %v", out, err)
	}
}

// `Intl.Collator` is absent, but `String.prototype.localeCompare` is not: a
// source that sorts with it works. `komga.js` sorts its libraries exactly that
// way, so "no Collator" must not be read as "sorting is broken".
//
// What is missing is the locale: the argument is accepted and ignored, so the
// order is the engine's own (case folded, upper before lower on a tie), not
// Swedish's or German's. A source that needs a language's order has to carry
// its own table.
func TestCompatibilityLocaleCompareWorksWithoutCollator(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if out, err := r.Eval(ctx, `typeof "a".localeCompare`); err != nil || string(out) != `"function"` {
		t.Fatalf("localeCompare: %s %v", out, err)
	}
	if out, err := r.Eval(ctx, `["b","A","c"].sort((x,y) => x.localeCompare(y)).join(",")`); err != nil || string(out) != `"A,b,c"` {
		t.Fatalf("sort: %s %v", out, err)
	}
	// The locale argument changes nothing: this is why the gap is documented as
	// "no locale", not "no comparison".
	if out, err := r.Eval(ctx, `"ä".localeCompare("z", "sv")`); err != nil || string(out) != "-1" {
		t.Fatalf("locale argument was honoured: %s %v", out, err)
	}
	if out, err := r.Eval(ctx, `typeof Intl.Collator`); err != nil || string(out) != `"undefined"` {
		t.Fatalf("Collator: %s %v", out, err)
	}
	if _, err := r.Eval(ctx, `new Intl.Collator("en")`); err == nil || !strings.Contains(err.Error(), "not a constructor") {
		t.Fatalf("Intl.Collator: %v", err)
	}
}

// A conversion that fails yields null and a log line, as the official client
// does; it does not throw. The sweep's one remaining failure, jm's init, is
// this and not an engine gap: jm's init awaits refreshApiDomains, which fetches
// an encrypted domain list, and the offline probe answered that fetch with an
// HTML page. Convert.decodeBase64 returned null and jm's own convertData read
// `.length` of it. With the list answered in the encrypted shape the site uses,
// jm loads; removing that answer brings the failure back. Pinning the null
// keeps the next sweep from filing the same failure as a compatibility gap.
func TestCompatibilityFailedConversionIsNullAndLogged(t *testing.T) {
	var lines []string
	r, err := New(Options{}, Services{Log: func(level, title, content string) {
		lines = append(lines, level+"|"+content)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	out, err := r.Eval(ctx, `(function () {
  try { const d = Convert.decodeBase64('<html>'); return d === null ? 'null' : typeof d }
  catch (e) { return 'threw: ' + e }
})()`)
	if err != nil || string(out) != `"null"` {
		t.Fatalf("decodeBase64 of a page: %s %v", out, err)
	}
	logged := false
	for _, line := range lines {
		if strings.HasPrefix(line, "error|") && strings.Contains(line, "Failed to convert base64") {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("the failure was not logged: %q", lines)
	}
	// What jm's convertData does next, and the exact error the sweep reported.
	_, err = r.Eval(ctx, `Convert.decryptAesEcb(Convert.decodeBase64('<html>'), Convert.encodeUtf8('0123456789abcdef')).length`)
	if err == nil || !strings.Contains(err.Error(), "Cannot read properties of null (reading 'length')") {
		t.Fatalf("null fed onward: %v", err)
	}
}

// A source whose single call expression is too large is told why, in words a
// source author can act on, rather than only the engine's internal wording.
//
// The 256-register window still applies to one call expression: 255 literal
// arguments do not compile even though a function may now hold many more
// locals (see TestCompatibilityLargeFunctionsCompile).
func TestCompatibilityCallRegisterWindowExplainsItself(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	args := strings.TrimSuffix(strings.Repeat("1,", 255), ",")
	var b strings.Builder
	b.WriteString(`class Big extends ComicSource {
 name = "Big"; key = "big"; version = "1.0.0"
`)
	b.WriteString(" loadInfo = async (id) => (function (...a) { return a.length })(" + args + ")\n}")
	_, err = r.LoadSource(context.Background(), b.String())
	if err == nil {
		t.Fatal("a source past the call register window loaded")
	}
	if !strings.Contains(err.Error(), "split it into smaller functions") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "key") && !strings.Contains(err.Error(), "evaluate") {
		t.Fatalf("error lost its context: %v", err)
	}
}

// Dates a source formats for display work, including the time zone option.
//
// Two of the official sources call `toLocaleDateString`, so "Intl is partial"
// must not be read as "dates are broken": what is missing is Collator and
// Temporal, not DateTimeFormat.
func TestCompatibilityLocaleDatesWork(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	utc, err := r.Eval(ctx, `new Date(1700000000000).toLocaleString('en-US', {timeZone:'UTC'})`)
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := r.Eval(ctx, `new Date(1700000000000).toLocaleString('en-US', {timeZone:'Asia/Tokyo'})`)
	if err != nil {
		t.Fatal(err)
	}
	if string(utc) == string(tokyo) {
		t.Fatalf("the time zone option was ignored: both said %s", utc)
	}
	if !strings.Contains(string(utc), "11/14/2023") || !strings.Contains(string(tokyo), "11/15/2023") {
		t.Fatalf("utc=%s tokyo=%s", utc, tokyo)
	}
	day, err := r.Eval(ctx, `new Date(1700000000000).toLocaleDateString('zh-CN')`)
	if err != nil || strings.Trim(string(day), `"`) == "" {
		t.Fatalf("toLocaleDateString: %s %v", day, err)
	}
}
