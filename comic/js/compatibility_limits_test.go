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

// A function with more than 256 registers does not compile.
//
// The ceiling is measured, not assumed: a source with one very long function
// (a generated bundle, or a `handle` that never splits) fails to load with a
// SyntaxError rather than running slowly. Recording it here means an engine
// bump that moves the limit, in either direction, shows up as a test change
// instead of a surprise on a user's screen.
func TestCompatibilityRegisterCeiling(t *testing.T) {
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
	if _, err := r.Eval(ctx, build(200)); err != nil {
		t.Fatalf("200 locals should compile: %v", err)
	}
	_, err = r.Eval(ctx, build(300))
	if err == nil || !strings.Contains(err.Error(), "more than 256 registers") {
		t.Fatalf("300 locals: %v", err)
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
