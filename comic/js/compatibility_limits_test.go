package js

import (
	"context"
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
	// The convert route is reachable from a source, whatever it does with the
	// bytes; a missing host function would throw here.
	got, err = r.Eval(ctx, `typeof sendMessage`)
	if err != nil || string(got) != `"function"` {
		t.Fatalf("sendMessage: got %s, err %v", got, err)
	}
}
