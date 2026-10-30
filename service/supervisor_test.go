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

// Cancelling a call has to stop the script, not just the wait: the runtime is
// interrupted and unloaded, so the next call starts from a clean one.
func TestSupervisorCancelsAScriptThatNeverReturns(t *testing.T) {
	l := newLoader()
	l.add("loop", "export function handle() { while (true) {} }")
	l.add("quick", "export function handle() { return { ok: true }; }")
	s := supervisorFor(l, SupervisorOptions{Timeout: 30 * time.Second})
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Call(ctx, "loop", "go", nil)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("took %s to stop", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled call never returned")
	}
	if got := s.Loaded(); len(got) != 0 {
		t.Fatalf("a cancelled service stayed loaded: %v", got)
	}
	// The supervisor still works after interrupting a script.
	if _, err := s.Call(context.Background(), "quick", "go", nil); err != nil {
		t.Fatalf("call after cancelling: %v", err)
	}
}

// One host per service: the client keeps credentials per service, so the
// supervisor must ask for the right one.
func TestSupervisorBuildsAHostPerService(t *testing.T) {
	l := newLoader()
	l.add("a", "export function handle() { return { who: storageGet('who') }; }")
	l.add("b", "export function handle() { return { who: storageGet('who') }; }")
	var mu sync.Mutex
	asked := []string{}
	open := func(ctx context.Context, id string) (*Manifest, string, error) {
		source, ok := l.source[id]
		if !ok {
			return nil, "", fmt.Errorf("no service %q", id)
		}
		return &Manifest{ID: id, Name: id, Version: "1", Kind: Kind, Entry: "index.mjs",
			Permissions: []Permission{PermissionStorage}}, source, nil
	}
	s := NewSupervisorFor(func(id string) Host {
		mu.Lock()
		asked = append(asked, id)
		mu.Unlock()
		return Host{StorageGet: func(ctx context.Context, key string) (string, bool, error) {
			return "value-of-" + id, true, nil
		}}
	}, []Permission{PermissionStorage}, SupervisorOptions{}, open)
	defer s.Close()
	for _, id := range []string{"a", "b"} {
		out, err := s.Call(context.Background(), id, "go", nil)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !strings.Contains(string(out), "value-of-"+id) {
			t.Fatalf("%s got %s", id, out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("host asked for %v", asked)
	}
}

func TestSupervisorDescribesAService(t *testing.T) {
	l := newLoader()
	l.add("demo", `export function describe() { return { methods: [{ name: 'sync', title: '同步' }] }; }
export function handle() { return null; }`)
	s := supervisorFor(l, SupervisorOptions{})
	defer s.Close()
	methods, err := s.Describe(context.Background(), "demo")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if len(methods) != 1 || methods[0].Name != "sync" {
		t.Fatalf("methods = %+v", methods)
	}
	if got := s.Loaded(); len(got) != 1 {
		t.Fatalf("loaded = %v", got)
	}
}

// A call that outlives the idle window must not be unloaded under itself.
// lastUsed is only updated when the call returns, so a sweep in the middle of a
// long method would otherwise close the runtime the method is running on.
func TestSweepDoesNotUnloadARunningCall(t *testing.T) {
	s := newTestSupervisor(t, SupervisorOptions{Idle: 50 * time.Millisecond}, map[string]string{
		"slow": `export function handle(method) {
  if (method == "slow") { const end = Date.now() + 250; while (Date.now() < end) {} }
  return { ok: true };
}`,
	})
	defer s.Close()

	done := make(chan error, 1)
	go func() {
		_, err := s.Call(context.Background(), "slow", "slow", nil)
		done <- err
	}()
	time.Sleep(120 * time.Millisecond) // the call is running, and past the idle window
	if n := s.Sweep(); n != 0 {
		t.Fatalf("sweep unloaded %d service(s) while a call was running", n)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the running call failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never returned")
	}
	// Once it is idle again the sweep may take it.
	time.Sleep(80 * time.Millisecond)
	if n := s.Sweep(); n != 1 {
		t.Fatalf("sweep after the call unloaded %d, want 1", n)
	}
}

// newTestSupervisor builds a supervisor over in-memory services.
func newTestSupervisor(t *testing.T, opts SupervisorOptions, sources map[string]string) *Supervisor {
	t.Helper()
	open := func(_ context.Context, id string) (*Manifest, string, error) {
		src, ok := sources[id]
		if !ok {
			return nil, "", errors.New("service: not installed: " + id)
		}
		return &Manifest{ID: id, Name: id, Version: "1", Entry: "index.mjs"}, src, nil
	}
	return NewSupervisor(Host{}, []Permission{PermissionNetwork, PermissionStorage, PermissionSecrets}, opts, open)
}

// The LRU limit is a memory bound, not a reason to fail a call. When every
// resident is running one, the cap is exceeded until one finishes.
func TestBusyServicesAreNotEvicted(t *testing.T) {
	s := newTestSupervisor(t, SupervisorOptions{Max: 1}, map[string]string{
		"a": `export function handle(method) {
  if (method == "slow") { const end = Date.now() + 250; while (Date.now() < end) {} }
  return { from: "a" };
}`,
		"b": `export function handle() { return { from: "b" }; }`,
	})
	defer s.Close()

	done := make(chan error, 1)
	go func() {
		_, err := s.Call(context.Background(), "a", "slow", nil)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	out, err := s.Call(context.Background(), "b", "go", nil)
	if err != nil {
		t.Fatalf("the second service could not load while the first was busy: %v", err)
	}
	if !strings.Contains(string(out), `"from":"b"`) {
		t.Fatalf("second call = %s", out)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the busy call was closed underneath: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the busy call never returned")
	}
	if loaded := s.Loaded(); len(loaded) != 2 {
		t.Fatalf("residents = %v, want both while one is busy", loaded)
	}
}
