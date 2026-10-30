package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const echoService = `
export function handle(method, payload) {
  if (method === 'ping') return { ok: true, payload: payload };
  if (method === 'boom') throw new Error('service exploded');
  return null;
}
`

const networkService = `
export function handle(method) {
  if (method === 'get') return { body: fetch('https://example.invalid/x') };
  return null;
}
`

const storageService = `
export function handle(method) {
  if (method === 'get') return { value: storageGet('token') };
  if (method === 'set') { storageSet('token', 'abc'); return { stored: true }; }
  return null;
}
`

func manifestFor(t *testing.T, permissions ...Permission) *Manifest {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": "demo", "name": "Demo", "version": "1", "kind": Kind, "entry": "index.mjs",
		"permissions": permissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRuntimeCallsAHandler(t *testing.T) {
	r := NewRuntime(Host{})
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t), echoService); err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := r.Call(context.Background(), "ping", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var got struct {
		OK      bool   `json:"ok"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result %s: %v", out, err)
	}
	if !got.OK || got.Payload != `{"a":1}` {
		t.Fatalf("result = %s", out)
	}
}

func TestRuntimeReportsAScriptError(t *testing.T) {
	r := NewRuntime(Host{})
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t), echoService); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "boom", nil); err == nil {
		t.Fatal("a throwing handler was accepted")
	} else if !strings.Contains(err.Error(), "service exploded") {
		t.Fatalf("error = %v", err)
	}
}

func TestRuntimeRefusesAModuleWithoutAHandler(t *testing.T) {
	r := NewRuntime(Host{})
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t), "export const other = 1;"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := r.Call(context.Background(), "ping", nil); err == nil {
		t.Fatal("a module without a handler was called")
	} else if !strings.Contains(err.Error(), "handle") {
		t.Fatalf("error = %v", err)
	}
}

// A service that asks for the network may use it; the host's fetch is the only
// way out, so a refused call has to name the permission.
func TestNetworkIsGatedByThePermission(t *testing.T) {
	var calls int
	host := Host{Fetch: func(ctx context.Context, url string) (string, error) {
		calls++
		return "body-of-" + url, nil
	}}

	denied := NewRuntime(host)
	defer denied.Close()
	if err := denied.Load(context.Background(), manifestFor(t), networkService); err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Call(context.Background(), "get", nil); err == nil {
		t.Fatal("the network was reachable without the permission")
	} else if !strings.Contains(err.Error(), "network") {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("the host was called %d times", calls)
	}

	allowed := NewRuntime(host, PermissionNetwork)
	defer allowed.Close()
	if err := allowed.Load(context.Background(), manifestFor(t, PermissionNetwork), networkService); err != nil {
		t.Fatal(err)
	}
	out, err := allowed.Call(context.Background(), "get", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(out), "body-of-https://example.invalid/x") {
		t.Fatalf("result = %s", out)
	}
	if calls != 1 {
		t.Fatalf("the host was called %d times", calls)
	}
}

// The host may grant less than the service asks for. The capability stays
// refused even then.
func TestHostMayGrantLessThanTheServiceAsksFor(t *testing.T) {
	host := Host{Fetch: func(ctx context.Context, url string) (string, error) { return "x", nil }}
	r := NewRuntime(host)
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t, PermissionNetwork), networkService); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "get", nil); err == nil {
		t.Fatal("a permission the host did not grant was usable")
	}
}

func TestStorageIsGatedAndWorks(t *testing.T) {
	values := map[string]string{}
	host := Host{
		StorageGet: func(ctx context.Context, key string) (string, bool, error) {
			v, ok := values[key]
			return v, ok, nil
		},
		StorageSet: func(ctx context.Context, key, value string) error {
			values[key] = value
			return nil
		},
	}
	denied := NewRuntime(host)
	defer denied.Close()
	if err := denied.Load(context.Background(), manifestFor(t), storageService); err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Call(context.Background(), "get", nil); err == nil {
		t.Fatal("storage was reachable without the permission")
	}

	allowed := NewRuntime(host, PermissionStorage)
	defer allowed.Close()
	if err := allowed.Load(context.Background(), manifestFor(t, PermissionStorage), storageService); err != nil {
		t.Fatal(err)
	}
	out, err := allowed.Call(context.Background(), "set", nil)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(string(out), "stored") {
		t.Fatalf("set result = %s", out)
	}
	out, err = allowed.Call(context.Background(), "get", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(string(out), "abc") {
		t.Fatalf("get result = %s", out)
	}
}

// The service's own log lines reach the host, and only the host.
func TestLogReachesTheHost(t *testing.T) {
	var lines []string
	host := Host{Log: func(level, message string) { lines = append(lines, level+":"+message) }}
	r := NewRuntime(host)
	defer r.Close()
	source := `
export function handle() {
  log('info', 'hello');
  return { done: true };
}
`
	if err := r.Load(context.Background(), manifestFor(t), source); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "info:hello" {
		t.Fatalf("log lines = %v", lines)
	}
}

// A cancelled call does not reach the host.
func TestCancelledCallDoesNotRun(t *testing.T) {
	r := NewRuntime(Host{})
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t), echoService); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Call(ctx, "ping", nil); err == nil {
		t.Fatal("a cancelled call ran")
	}
}

// An async handler that settles during the call is read as its result; one that
// is still waiting is refused instead of being turned into an empty object.
func TestAsyncHandlersAreReadOrRefusedClearly(t *testing.T) {
	settled := `
export async function handle(method) {
  if (method === 'ping') return { ok: true };
  if (method === 'boom') throw new Error('async boom');
  return null;
}
`
	r := NewRuntime(Host{})
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t), settled); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("result = %s", out)
	}
	if _, err := r.Call(context.Background(), "boom", nil); err == nil {
		t.Fatal("a rejected async handler was accepted")
	} else if !strings.Contains(err.Error(), "async boom") {
		t.Fatalf("error = %v", err)
	}

	waiting := `
export async function handle() {
  await new Promise(() => {});
  return { never: true };
}
`
	w := NewRuntime(Host{})
	defer w.Close()
	if err := w.Load(context.Background(), manifestFor(t), waiting); err != nil {
		t.Fatal(err)
	}
	_, err = w.Call(context.Background(), "go", nil)
	if err == nil {
		t.Fatal("a promise that never settles was accepted")
	}
	if !strings.Contains(err.Error(), "not settled") {
		t.Fatalf("error = %v", err)
	}
}
