package magpieplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub answers GitHub's device-code and token endpoints as a client
// would see them, so the real Copilot package can be driven with no network
// and no account.
type fakeGitHub struct {
	mu       sync.Mutex
	polls    int
	requests []*Request
	// tokenAt is the poll at which GitHub grants the token; earlier polls
	// answer authorization_pending.
	tokenAt int
	// deny makes GitHub answer an error instead of a token.
	deny string
}

func (f *fakeGitHub) Do(_ context.Context, req *Request) (*Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	reply := func(body string) (*Response, error) {
		return &Response{
			Status: 200, StatusText: "OK",
			Header: map[string][]string{"Content-Type": {"application/json"}},
			Body:   []byte(body), URL: req.URL,
		}, nil
	}
	switch {
	case strings.HasSuffix(req.URL, "/login/device/code"):
		// The verification page is the same host the code was asked from,
		// so an enterprise host sees its own login page.
		host := "https://github.com"
		if at := strings.Index(req.URL, "/login/device/code"); at > 0 {
			host = req.URL[:at]
		}
		return reply(fmt.Sprintf(`{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":%q,"expires_in":900,"interval":0}`, host+"/login/device"))
	case strings.HasSuffix(req.URL, "/copilot_internal/user"):
		return reply(`{"copilot_plan":"individual","quota_reset_date_utc":"2026-01-01T00:00:00Z","quota_snapshots":{"premium_interactions":{"entitlement":300,"quota_remaining":150,"has_quota":true,"quota_reset_at":"2026-01-01T00:00:00Z"},"chat":{"entitlement":0,"quota_remaining":0,"has_quota":true},"completions":{"entitlement":0,"quota_remaining":0,"has_quota":true}}}`)
	case strings.HasSuffix(req.URL, "/chat/completions"):
		return reply(`{"choices":[{"message":{"content":"ok"}}]}`)
	case strings.HasSuffix(req.URL, "/copilot_internal/v2/token"):
		// The refresh endpoint: a short-lived API token with an expiry.
		return reply(`{"token":"tid=copilot_api_token","expires_at":1767225600}`)
	case strings.HasSuffix(req.URL, "/login/oauth/access_token"):
		f.polls++
		if f.deny != "" {
			return reply(fmt.Sprintf(`{"error":%q}`, f.deny))
		}
		if f.polls < f.tokenAt {
			return reply(`{"error":"authorization_pending"}`)
		}
		return reply(`{"access_token":"gho_test_token"}`)
	}
	return reply(`{}`)
}

func (f *fakeGitHub) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

// copilotBundle loads the pinned Copilot files, with a test entry that runs
// the device flow inside JavaScript: the plugin's methods are functions, so
// they cannot be handed to Go and called one by one.
func copilotBundle(t *testing.T, flow string) *Bundle {
	t.Helper()
	files := map[string][]byte{}
	for name, src := range map[string]string{
		"index.mjs":     "copilot-index.mjs",
		"endpoints.mjs": "copilot-endpoints.mjs",
		"usage.mjs":     "copilot-usage.mjs",
	} {
		body, err := os.ReadFile("E:/Temp/nkprobe/magpie-community-audit/" + src)
		if err != nil {
			t.Skip("audit fixtures not present:", err)
		}
		files[name] = body
	}
	files["package.json"] = []byte(`{"name":"copilot-flow","version":"0.0.0","main":"./flow.mjs"}`)
	files["flow.mjs"] = []byte(flow)
	b, err := NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const deviceFlow = `import { CopilotAuthPlugin } from "./index.mjs"

export async function flow(inputs) {
  const plugin = await CopilotAuthPlugin({ client: { auth: { set: async () => {} } } })
  const method = plugin.auth.methods[0]
  const started = await method.authorize(inputs)
  const result = await started.callback()
  return {
    provider: plugin.auth.provider,
    url: started.url,
    instructions: started.instructions,
    result,
  }
}`

func runFlow(t *testing.T, gh Host, inputs map[string]any) map[string]any {
	t.Helper()
	loader, err := Load(copilotBundle(t, deviceFlow))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(gh); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("flow", inputs)
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	root, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("flow returned %#v", got)
	}
	return root
}

// The whole device-code sign-in of the real package, against a fake service.
func TestRealCopilotDeviceFlowCompletes(t *testing.T) {
	gh := &fakeGitHub{tokenAt: 3}
	got := runFlow(t, gh, map[string]any{"deploymentType": "github.com"})

	if got["provider"] != "github-copilot" {
		t.Fatalf("provider = %v", got["provider"])
	}
	if got["url"] != "https://github.com/login/device" {
		t.Fatalf("verification url = %v", got["url"])
	}
	if !strings.Contains(fmt.Sprint(got["instructions"]), "ABCD-1234") {
		t.Fatalf("instructions = %v", got["instructions"])
	}
	result, _ := got["result"].(map[string]any)
	if result["type"] != "success" || result["refresh"] != "gho_test_token" {
		t.Fatalf("result = %#v", result)
	}
	// Two pending polls, then the token: the package polled until it had one.
	if n := gh.pollCount(); n != 3 {
		t.Fatalf("polls = %d, want 3", n)
	}
}

func TestRealCopilotDeviceFlowReportsRefusal(t *testing.T) {
	gh := &fakeGitHub{tokenAt: 1, deny: "access_denied"}
	got := runFlow(t, gh, map[string]any{"deploymentType": "github.com"})
	result, _ := got["result"].(map[string]any)
	if result["type"] != "failed" {
		t.Fatalf("result = %#v", result)
	}
	if n := gh.pollCount(); n != 1 {
		t.Fatalf("polls = %d, want 1", n)
	}
}

// A package may require an enterprise host the user typed; the request must
// then go to that host, not to github.com.
func TestRealCopilotEnterpriseHostIsUsed(t *testing.T) {
	gh := &fakeGitHub{tokenAt: 1}
	got := runFlow(t, gh, map[string]any{"deploymentType": "enterprise", "enterpriseUrl": "company.ghe.com"})
	if got["url"] != "https://company.ghe.com/login/device" {
		t.Fatalf("verification url = %v", got["url"])
	}
}

func TestPreludeProvidesTheWebAPIs(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs":    []byte(`export async function probe() { return [typeof fetch, typeof Headers, typeof Response, typeof URL].join("/") }`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(&recordingHost{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "function/function/function/function"; got != want {
		t.Fatalf("prelude = %v, want %v", got, want)
	}
}

type recordingHost struct{ last *Request }

func (h *recordingHost) Do(_ context.Context, req *Request) (*Response, error) {
	h.last = req
	return &Response{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"echo":true}`), URL: req.URL}, nil
}

func TestFetchReachesTheClientAndParsesJSON(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function ask(url) {
  const r = await fetch(url, { headers: { "X-Test": "yes" }, method: "POST", body: "{\"a\":1}" })
  if (!r.ok) throw new Error("status " + r.status)
  const body = await r.json()
  return body.echo + "/" + r.headers.get("content-type") + "/" + r.status
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	host := &recordingHost{}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("ask", "https://example.test/v1/thing")
	if err != nil {
		t.Fatal(err)
	}
	if want := "true/application/json/200"; got != want {
		t.Fatalf("answer = %v, want %v", got, want)
	}
	// The prelude lowercases header names, as the Web Headers API does.
	if host.last == nil || host.last.Method != "POST" || headerValue(host.last.Header, "x-test") != "yes" || string(host.last.Body) != `{"a":1}` {
		t.Fatalf("request = %+v", host.last)
	}
}

// A failed fetch must reject the promise the package awaits, not resolve with
// a body that hides the failure.
func TestFetchFailureRejects(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function ask() {
  try { await fetch("https://example.test/x"); return "resolved" } catch (e) { return "rejected:" + e.message }
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(failingHost{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("ask", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "rejected:offline" {
		t.Fatalf("answer = %v", got)
	}
}

type failingHost struct{}

func (failingHost) Do(context.Context, *Request) (*Response, error) {
	return nil, fmt.Errorf("offline")
}

func TestPromiseSettlesWithJSONNumbersIntact(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs":    []byte(`export async function big() { const r = await fetch("https://example.test/big"); return await r.text() }`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(bigHost{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("big", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"id":9007199254740993}` {
		t.Fatalf("body = %v", got)
	}
	if !json.Valid([]byte(got.(string))) {
		t.Fatalf("body is not JSON: %v", got)
	}
}

type bigHost struct{}

func (bigHost) Do(_ context.Context, req *Request) (*Response, error) {
	return &Response{Status: 200, Header: map[string][]string{}, Body: []byte(`{"id":9007199254740993}`), URL: req.URL}, nil
}

func headerValue(h map[string][]string, name string) string {
	for k, vs := range h {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// memoryAuth is the client's sign-in store: one provider id, no persistence.
type memoryAuth struct {
	mu      sync.Mutex
	current map[string]any
	sets    []map[string]any
}

func (m *memoryAuth) Get(context.Context, string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current, nil
}

func (m *memoryAuth) Set(_ context.Context, _ string, value map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = value
	m.sets = append(m.sets, value)
	return nil
}

const loaderFlow = `import { CopilotAuthPlugin } from "./index.mjs"

export async function refresh(stored) {
  const plugin = await CopilotAuthPlugin({ client: globalThis.__magpieClient })
  const getAuth = async () => stored
  const options = await plugin.auth.loader(getAuth, { models: {} })
  // The loader defines a fetch; the refresh happens when a request uses it.
  const r = await options.fetch(options.baseURL + "/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{\"model\":\"gpt-4o\"}",
  })
  return {
    baseURL: options.baseURL,
    canFetch: typeof options.fetch,
    status: r.status,
  }
}`

// A stored sign-in with no access token must be refreshed, saved back through
// client.auth.set, and handed to the request layer with the new token.
func TestRealCopilotRefreshSavesTheNewSignIn(t *testing.T) {
	gh := &fakeGitHub{tokenAt: 1}
	store := &memoryAuth{current: map[string]any{
		"type": "oauth", "refresh": "gho_refresh", "access": "", "expires": 0,
	}}
	loader, err := Load(copilotBundle(t, loaderFlow))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(gh); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", store); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("refresh", map[string]any{"type": "oauth", "refresh": "gho_refresh", "access": "", "expires": 0})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	root, _ := got.(map[string]any)
	if root["baseURL"] != "https://api.githubcopilot.com" {
		t.Fatalf("baseURL = %v", root["baseURL"])
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sets) != 1 {
		t.Fatalf("sign-in writes = %d, want 1", len(store.sets))
	}
	saved := store.sets[0]
	if saved["access"] != "tid=copilot_api_token" {
		t.Fatalf("saved access = %v", saved["access"])
	}
	// expires_at is seconds; the package stores milliseconds minus 5 minutes.
	// The value crossed from JavaScript as a number; accept the Go shapes it
	// can arrive in rather than being lenient about its value.
	var expires int64
	switch v := saved["expires"].(type) {
	case float64:
		expires = int64(v)
	case int64:
		expires = v
	case json.Number:
		expires, _ = v.Int64()
	}
	if want := int64(1767225600)*1000 - 5*60*1000; expires != want {
		t.Fatalf("saved expires = %v (%T), want %d", saved["expires"], saved["expires"], want)
	}
	if saved["refresh"] != "gho_refresh" {
		t.Fatalf("refresh token was lost: %v", saved)
	}
}

// One package must not be able to write another provider's sign-in.
func TestPackageCannotWriteAnotherProvidersSignIn(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function steal() {
  try {
    await globalThis.__magpieClient.auth.set({ path: { id: "openai" }, body: { type: "api", key: "stolen" } })
    return "written"
  } catch (e) {
    return "refused"
  }
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	store := &memoryAuth{}
	if err := inst.UseClient("github-copilot", store); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("steal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "refused" {
		t.Fatalf("answer = %v", got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sets) != 0 {
		t.Fatalf("a foreign sign-in was written: %v", store.sets)
	}
}

const usageFlow = `import { CopilotAuthPlugin } from "./index.mjs"

export async function usage() {
  const plugin = await CopilotAuthPlugin({ client: globalThis.__magpieClient })
  const getAuth = async () => ({ type: "oauth", refresh: "gho_refresh", access: "gho_access", expires: 9000000000000 })
  const out = await plugin.auth.usage(getAuth, { models: {} })
  return out
}`

// The package's own usage hook, run for real: it reads GitHub's quota endpoint
// and turns it into windows our client can show.
func TestRealCopilotUsageHookReadsQuota(t *testing.T) {
	gh := &fakeGitHub{tokenAt: 1}
	loader, err := Load(copilotBundle(t, usageFlow))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(gh); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", &memoryAuth{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("usage", nil)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	root, _ := got.(map[string]any)
	windows, ok := root["windows"].([]any)
	if !ok || len(windows) == 0 {
		t.Fatalf("windows = %#v", root["windows"])
	}
	first, _ := windows[0].(map[string]any)
	if first["name"] != "Premium requests" {
		t.Fatalf("first window = %#v", first)
	}
	if used := number(first["used"]); used != 50 {
		t.Fatalf("used = %v (%T), want 50 (300 entitled, 150 left)", first["used"], first["used"])
	}
	if first["resetsAt"] != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("resetsAt = %v", first["resetsAt"])
	}
	// The request carried the account's token and the package's own headers.
	var user *Request
	for _, r := range gh.requests {
		if strings.HasSuffix(r.URL, "/copilot_internal/user") {
			user = r
		}
	}
	if user == nil {
		t.Fatal("the usage endpoint was never asked")
	}
	// The quota endpoint uses the OAuth token, not the inference token.
	if headerValue(user.Header, "authorization") != "token gho_refresh" {
		t.Fatalf("authorization = %v", user.Header)
	}
	if !strings.HasPrefix(headerValue(user.Header, "user-agent"), "GitHubCopilotChat/") {
		t.Fatalf("user-agent = %v", headerValue(user.Header, "user-agent"))
	}
}

// number reads a JavaScript number however it crossed into Go.
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return -1
}

const modelsFlow = `import { Plugin } from "./index.mjs"

export async function list() {
  const plugin = await Plugin({ client: {} })
  const models = await plugin.provider.models({}, { auth: { token: "t-1" } })
  return models
}`

func TestProviderModelsHookAnswersItsOwnList(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./flow.mjs"}`),
		"flow.mjs":     []byte(modelsFlow),
		"index.mjs": []byte(`export async function Plugin() {
  return {
    auth: { provider: "self", methods: [] },
    provider: {
      id: "self",
      async models(given, { auth }) {
        return {
          "self-chat": {
            name: "Self Chat",
            reasoning: true,
            tool_call: true,
            limit: { context: 200000, output: 8192 },
            modalities: { input: ["text", "image"], output: ["text"] },
            seenToken: auth.token,
          },
        }
      },
    },
  }
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	got, err := inst.Call("list", nil)
	if err != nil {
		t.Fatal(err)
	}
	models, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("models = %#v", got)
	}
	one, ok := models["self-chat"].(map[string]any)
	if !ok {
		t.Fatalf("model = %#v", models["self-chat"])
	}
	if one["name"] != "Self Chat" || one["seenToken"] != "t-1" {
		t.Fatalf("model = %#v", one)
	}
	limit, _ := one["limit"].(map[string]any)
	if number(limit["context"]) != 200000 || number(limit["output"]) != 8192 {
		t.Fatalf("limit = %#v", one["limit"])
	}
	modalities, _ := one["modalities"].(map[string]any)
	inputs, _ := modalities["input"].([]any)
	if len(inputs) != 2 || inputs[0] != "text" || inputs[1] != "image" {
		t.Fatalf("modalities = %#v", modalities)
	}
}

// blockingHost answers only when its context ends, so a test can cancel a
// request that is in flight.
type blockingHost struct {
	once    sync.Once
	started chan struct{}
}

func (h *blockingHost) Do(ctx context.Context, _ *Request) (*Response, error) {
	h.once.Do(func() { close(h.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// Aborting a fetch must cancel the client's request and reject the promise the
// package awaits, rather than leaving it pending until the network gives up.
func TestAbortSignalCancelsTheRequest(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function cancel() {
  const c = new AbortController()
  setTimeout(() => c.abort(), 5)
  try {
    await fetch("https://example.test/slow", { signal: c.signal })
    return "resolved"
  } catch (e) {
    return "rejected:" + e.message
  }
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	host := &blockingHost{started: make(chan struct{})}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("cancel", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got != "rejected:context canceled" {
		t.Fatalf("answer = %v", got)
	}
}

// streamHost answers with a head and then three chunks, like a vendor's SSE
// reply.
type streamHost struct{ closed bool }

func (h *streamHost) Do(_ context.Context, req *Request) (*Response, error) {
	return &Response{Status: 200, Header: map[string][]string{}, Body: []byte("unused"), URL: req.URL}, nil
}

func (h *streamHost) DoStream(ctx context.Context, req *Request, onChunk func([]byte) error) (*Response, error) {
	head := &Response{Status: 200, Header: map[string][]string{"Content-Type": {"text/event-stream"}}, URL: req.URL}
	for _, chunk := range []string{"data: one\n\n", "data: two\n\n", "data: [DONE]\n\n"} {
		if err := ctx.Err(); err != nil {
			return head, err
		}
		if err := onChunk([]byte(chunk)); err != nil {
			return head, err
		}
	}
	return head, nil
}

// A package reads a reply as it arrives: the chunks reach the ReadableStream in
// order, and the reader ends when the body does.
func TestStreamingReplyReachesThePackageInChunks(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function read(url) {
  const r = await fetch(url)
  const seen = []
  const reader = r.body.getReader()
  const decoder = new TextDecoder()
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    seen.push(decoder.decode(value))
  }
  return { status: r.status, chunks: seen.length, all: seen.join(""), hasBody: r.body !== null }
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(&streamHost{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("read", "https://example.test/stream")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	root, _ := got.(map[string]any)
	if number(root["chunks"]) != 3 {
		t.Fatalf("chunks = %v", root["chunks"])
	}
	if root["all"] != "data: one\n\ndata: two\n\ndata: [DONE]\n\n" {
		t.Fatalf("body = %q", root["all"])
	}
	if root["hasBody"] != true {
		t.Fatal("body is not a stream")
	}
}

// A body that fails after the head must surface while the package reads, not
// as a silent end of the stream.
func TestStreamFailureSurfacesToTheReader(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function read(url) {
  const r = await fetch(url)
  const reader = r.body.getReader()
  try {
    for (;;) {
      const { done } = await reader.read()
      if (done) return "ended"
    }
  } catch (e) {
    return "failed:" + e.message
  }
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, _ := Load(b)
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(&failingStream{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("read", "https://example.test/stream")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "failed:connection dropped" {
		t.Fatalf("answer = %v", got)
	}
}

type failingStream struct{}

func (failingStream) Do(_ context.Context, req *Request) (*Response, error) {
	return &Response{Status: 200, Header: map[string][]string{}, URL: req.URL}, nil
}

func (failingStream) DoStream(_ context.Context, req *Request, onChunk func([]byte) error) (*Response, error) {
	head := &Response{Status: 200, Header: map[string][]string{}, URL: req.URL}
	if err := onChunk([]byte("data: first\n\n")); err != nil {
		return head, err
	}
	return head, fmt.Errorf("connection dropped")
}

const streamFlow = `import { CopilotAuthPlugin } from "./index.mjs"

export async function streamChat() {
  const plugin = await CopilotAuthPlugin({ client: globalThis.__magpieClient })
  const getAuth = async () => ({ type: "oauth", refresh: "gho_refresh", access: "gho_access", expires: 9000000000000 })
  const options = await plugin.auth.loader(getAuth, { models: {} })
  const r = await options.fetch(options.baseURL + "/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Accept": "text/event-stream" },
    body: '{"model":"gpt-4o","stream":true}',
  })
  const text = await r.text()
  return { status: r.status, text, initiator: null }
}`

// The package's own fetch wrapper must hand the stream through: it rewrites
// headers and token, and the reply is still read chunk by chunk.
func TestRealCopilotStreamsThroughItsOwnFetch(t *testing.T) {
	gh := &streamingGitHub{streamHost: &streamHost{}, fakeGitHub: &fakeGitHub{tokenAt: 1}}
	loader, err := Load(copilotBundle(t, streamFlow))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(gh); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", &memoryAuth{}); err != nil {
		t.Fatal(err)
	}
	got, err := inst.Call("streamChat", nil)
	if err != nil {
		t.Fatalf("streamChat: %v", err)
	}
	root, _ := got.(map[string]any)
	if root["status"] != nil && number(root["status"]) != 200 {
		t.Fatalf("status = %v", root["status"])
	}
	if root["text"] != "data: one\n\ndata: two\n\ndata: [DONE]\n\n" {
		t.Fatalf("text = %q", root["text"])
	}
}

// streamingGitHub answers the token endpoint like fakeGitHub and streams chat.
type streamingGitHub struct {
	*fakeGitHub
	*streamHost
}

func (s *streamingGitHub) Do(ctx context.Context, req *Request) (*Response, error) {
	if strings.HasSuffix(req.URL, "/chat/completions") {
		return s.streamHost.Do(ctx, req)
	}
	return s.fakeGitHub.Do(ctx, req)
}

func (s *streamingGitHub) DoStream(ctx context.Context, req *Request, onChunk func([]byte) error) (*Response, error) {
	if strings.HasSuffix(req.URL, "/chat/completions") {
		return s.streamHost.DoStream(ctx, req, onChunk)
	}
	resp, err := s.fakeGitHub.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
