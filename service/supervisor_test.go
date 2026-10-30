package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// loader serves packages from a map and counts how often each was opened.
type loader struct {
	mu     sync.Mutex
	source map[string]string
	opened map[string]int
}

func newLoader() *loader {
	return &loader{source: map[string]string{}, opened: map[string]int{}}
}

func (l *loader) add(id, source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.source[id] = source
}

func (l *loader) open(ctx context.Context, id string) (*Manifest, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.opened[id]++
	source, ok := l.source[id]
	if !ok {
		return nil, "", fmt.Errorf("no service %q", id)
	}
	return &Manifest{ID: id, Name: id, Version: "1", Kind: Kind, Entry: "index.mjs"}, source, nil
}

func (l *loader) opens(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.opened[id]
}

func supervisorFor(l *loader, opts SupervisorOptions) *Supervisor {
	return NewSupervisor(Host{}, nil, opts, l.open)
}

func TestSupervisorLoadsOnceAndReuses(t *testing.T) {
	l := newLoader()
	l.add("demo", "export function handle(method) { return { method: method }; }")
	s := supervisorFor(l, SupervisorOptions{})
	defer s.Close()

	for i := 0; i < 3; i++ {
		out, err := s.Call(context.Background(), "demo", "ping", nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !strings.Contains(string(out), "ping") {
			t.Fatalf("result = %s", out)
		}
	}
	if got := l.opens("demo"); got != 1 {
		t.Fatalf("loaded %d times", got)
	}
	if got := s.Loaded(); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("loaded = %v", got)
	}
}

// A script that never returns has to be stopped: the supervisor interrupts it
// and unloads it, so the next call starts from a clean runtime.
func TestSupervisorStopsAScriptThatNeverReturns(t *testing.T) {
	l := newLoader()
	l.add("loop", "export function handle() { while (true) {} }")
	s := supervisorFor(l, SupervisorOptions{Timeout: 400 * time.Millisecond})
	defer s.Close()

	start := time.Now()
	_, err := s.Call(context.Background(), "loop", "go", nil)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %s", elapsed)
	}
	if got := s.Loaded(); len(got) != 0 {
		t.Fatalf("a timed out service stayed loaded: %v", got)
	}
}

// A script error is not a reason to unload: the service is still usable.
func TestSupervisorKeepsTheServiceAfterAScriptError(t *testing.T) {
	l := newLoader()
	l.add("demo", `
export function handle(method) {
  if (method === 'boom') throw new Error('service exploded');
  return { ok: true };
}
`)
	s := supervisorFor(l, SupervisorOptions{})
	defer s.Close()
	if _, err := s.Call(context.Background(), "demo", "boom", nil); err == nil {
		t.Fatal("a throwing handler was accepted")
	}
	if _, err := s.Call(context.Background(), "demo", "ping", nil); err != nil {
		t.Fatalf("call after a script error: %v", err)
	}
	if got := l.opens("demo"); got != 1 {
		t.Fatalf("loaded %d times", got)
	}
}

// The resident set is bounded: the least recently used service goes first.
func TestSupervisorEvictsTheLeastRecentlyUsed(t *testing.T) {
	l := newLoader()
	for _, id := range []string{"a", "b", "c"} {
		l.add(id, "export function handle() { return 1; }")
	}
	s := supervisorFor(l, SupervisorOptions{Max: 2})
	// A controlled clock keeps "least recently used" unambiguous: the system
	// clock on Windows can return the same value for calls this close together.
	now := time.Now()
	s.now = func() time.Time { now = now.Add(time.Second); return now }
	defer s.Close()
	for _, id := range []string{"a", "b"} {
		if _, err := s.Call(context.Background(), id, "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	// Use a again so b is the oldest.
	if _, err := s.Call(context.Background(), "a", "go", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(context.Background(), "c", "go", nil); err != nil {
		t.Fatal(err)
	}
	got := s.Loaded()
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("loaded = %v", got)
	}
}

func TestSupervisorSweepsIdleServices(t *testing.T) {
	l := newLoader()
	l.add("demo", "export function handle() { return 1; }")
	now := time.Now()
	s := supervisorFor(l, SupervisorOptions{Idle: time.Minute})
	// A clock the test controls keeps the sweep deterministic.
	s.now = func() time.Time { return now }
	defer s.Close()

	if _, err := s.Call(context.Background(), "demo", "go", nil); err != nil {
		t.Fatal(err)
	}
	if n := s.Sweep(); n != 0 {
		t.Fatalf("swept %d while fresh", n)
	}
	now = now.Add(2 * time.Minute)
	if n := s.Sweep(); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if got := s.Loaded(); len(got) != 0 {
		t.Fatalf("loaded = %v", got)
	}
	// The next call loads it again.
	if _, err := s.Call(context.Background(), "demo", "go", nil); err != nil {
		t.Fatalf("call after sweep: %v", err)
	}
	if got := l.opens("demo"); got != 2 {
		t.Fatalf("loaded %d times", got)
	}
}

func TestSupervisorRefusesUnknownAndClosed(t *testing.T) {
	l := newLoader()
	s := supervisorFor(l, SupervisorOptions{})
	if _, err := s.Call(context.Background(), "missing", "go", nil); err == nil {
		t.Fatal("an unknown service was called")
	}
	s.Close()
	if _, err := s.Call(context.Background(), "missing", "go", nil); err == nil {
		t.Fatal("a closed supervisor accepted a call")
	}
}

// The loader must return the manifest of the service that was asked for.
func TestSupervisorRefusesAMismatchedManifest(t *testing.T) {
	s := NewSupervisor(Host{}, nil, SupervisorOptions{}, func(ctx context.Context, id string) (*Manifest, string, error) {
		return &Manifest{ID: "other", Name: "other", Version: "1", Kind: Kind, Entry: "index.mjs"},
			"export function handle() { return 1; }", nil
	})
	defer s.Close()
	if _, err := s.Call(context.Background(), "demo", "go", nil); err == nil {
		t.Fatal("a manifest for another service was accepted")
	}
}

// The service's payload survives the trip through the supervisor.
func TestSupervisorPassesThePayload(t *testing.T) {
	l := newLoader()
	l.add("demo", "export function handle(method, payload) { return { payload: JSON.parse(payload) }; }")
	s := supervisorFor(l, SupervisorOptions{})
	defer s.Close()
	out, err := s.Call(context.Background(), "demo", "echo", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `{"a":1}`) {
		t.Fatalf("result = %s", out)
	}
}
