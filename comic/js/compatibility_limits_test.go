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
