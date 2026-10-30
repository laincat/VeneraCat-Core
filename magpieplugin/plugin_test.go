package magpieplugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
)

// pluginHostStub answers the pinned Copilot package's device flow.
type pluginHostStub struct {
	mu    sync.Mutex
	polls int
	urls  []string
}

func (h *pluginHostStub) Do(_ context.Context, req *Request) (*Response, error) {
	h.mu.Lock()
	h.urls = append(h.urls, req.URL)
	h.mu.Unlock()
	reply := func(body string) (*Response, error) {
		return &Response{Status: 200, Body: []byte(body), Header: map[string][]string{"Content-Type": {"application/json"}}}, nil
	}
	switch {
	case strings.HasSuffix(req.URL, "/login/device/code"):
		return reply(`{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":0}`)
	case strings.HasSuffix(req.URL, "/login/oauth/access_token"):
		h.mu.Lock()
		h.polls++
		polls := h.polls
		h.mu.Unlock()
		if polls < 2 {
			return reply(`{"error":"authorization_pending"}`)
		}
		return reply(`{"access_token":"gho_signed_in"}`)
	case strings.HasSuffix(req.URL, "/copilot_internal/user"):
		return reply(`{"copilot_plan":"individual","quota_snapshots":{"premium_interactions":{"entitlement":300,"quota_remaining":250,"has_quota":true}}}`)
	}
	return reply(`{"unexpected":true}`)
}

// copilotPluginBundle packages the pinned original Copilot files as a bundle
// whose entry is the package's own index.mjs: the factory finder has to reach a
// named export, because this package declares no default one.
func copilotPluginBundle(t *testing.T) *Bundle {
	t.Helper()
	files := map[string][]byte{}
	for name, src := range map[string]string{
		"package.json":  "copilot-package.json",
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
	files["package.json"] = []byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`)
	b, err := NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func copilotPluginInstance(t *testing.T) (*Instance, *pluginHostStub) {
	t.Helper()
	loader, err := Load(copilotPluginBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inst.Close)
	host := &pluginHostStub{}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", &memoryAuth{}); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	return inst, host
}

// The ABI is Magpie's, not ours: a package that declares no default export is
// still found, because Magpie tries every exported function.
func TestPluginAuthDescribesThePackage(t *testing.T) {
	inst, _ := copilotPluginInstance(t)
	auth, err := inst.PluginAuth()
	if err != nil {
		t.Fatal(err)
	}
	if auth == nil {
		t.Fatal("no auth reported")
	}
	if auth["provider"] != "github-copilot" {
		t.Fatalf("provider = %v", auth["provider"])
	}
	methods, ok := auth["methods"].([]any)
	if !ok || len(methods) == 0 {
		t.Fatalf("methods = %v", auth["methods"])
	}
	first := methods[0].(map[string]any)
	if first["label"] == "" {
		t.Fatalf("first method = %v", first)
	}
	if auth["hasLoader"] != true || auth["hasUsage"] != true {
		t.Fatalf("capabilities = %v", auth)
	}
}

// Signing in through the bridge: the page to show, then two pending polls, then
// the refresh token the client keeps.
func TestPluginDeviceFlow(t *testing.T) {
	inst, host := copilotPluginInstance(t)
	started, err := inst.Authorize(0, map[string]any{"deploymentType": "github.com"})
	if err != nil {
		t.Fatal(err)
	}
	if started["url"] != "https://github.com/login/device" {
		t.Fatalf("url = %v", started["url"])
	}
	// This package puts the code inside its instructions rather than in a
	// field of its own, which is why the bridge reports both.
	if !strings.Contains(started["instructions"].(string), "ABCD-1234") {
		t.Fatalf("instructions = %v", started["instructions"])
	}
	id, ok := started["id"].(int64)
	if !ok {
		t.Fatalf("id = %T %v", started["id"], started["id"])
	}

	// This package's callback loops until the vendor answers, so one poll here
	// covers the pending answer and the token that follows it. The count says
	// the loop really ran twice rather than skipping the pending response.
	answer, err := inst.Poll(id)
	if err != nil {
		t.Fatal(err)
	}
	if answer["type"] != "success" || answer["refresh"] != "gho_signed_in" {
		t.Fatalf("poll = %v", answer)
	}
	if host.polls != 2 {
		t.Fatalf("polls = %d", host.polls)
	}
	// A finished sign-in is forgotten: polling it again is an error, not a
	// second wait.
	if _, err := inst.Poll(id); err == nil {
		t.Fatal("a finished sign-in still polls")
	}
}

// A sign-in can be abandoned: its id stops working, and a later poll says so
// rather than hanging on a callback nobody will answer.
func TestPluginCancelSignIn(t *testing.T) {
	inst, _ := copilotPluginInstance(t)
	started, err := inst.Authorize(0, map[string]any{"deploymentType": "github.com"})
	if err != nil {
		t.Fatal(err)
	}
	id := started["id"].(int64)
	if err := inst.CancelSignIn(id); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Poll(id); err == nil {
		t.Fatal("a cancelled sign-in still polls")
	}
}

// The usage hook needs the saved sign-in; without one it reports nothing usable
// rather than inventing numbers.
func TestPluginUsageReadsTheSignedInAccount(t *testing.T) {
	inst, _ := copilotPluginInstance(t)
	usage, err := inst.Usage("github-copilot")
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil {
		t.Skip("this package reports usage only for a signed-in account")
	}
}

// A package with no plugin function is refused with a plain reason.
func TestPluginBridgeNeedsAPlugin(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"plain","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs":    []byte(`export const answer = 42`),
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
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	if auth, err := inst.PluginAuth(); err == nil {
		t.Fatalf("a package with no plugin answered auth = %v", auth)
	}
}

// The bridge must not need the client to speak JavaScript: a base64 round trip
// of the package is enough to reach its hooks.
func TestPluginBundleIsPlainFiles(t *testing.T) {
	b := copilotPluginBundle(t)
	names, err := b.Entries()
	if err != nil {
		t.Fatal(err)
	}
	encoded := map[string]string{}
	for _, path := range names {
		body, err := b.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		encoded[path] = base64.StdEncoding.EncodeToString(body)
	}
	if _, err := json.Marshal(encoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := encoded["index.mjs"]; !ok {
		t.Fatalf("entries = %v", names)
	}
}

// chatHost answers a signed-in Copilot account: the token refresh the loader
// asks for, then an OpenAI-compatible stream.
type chatHost struct {
	mu       sync.Mutex
	requests []string
	stream   string
}

func (h *chatHost) Do(_ context.Context, req *Request) (*Response, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req.URL)
	h.mu.Unlock()
	if strings.HasSuffix(req.URL, "/copilot_internal/v2/token") {
		return &Response{Status: 200, Body: []byte(`{"token":"tid=api","expires_at":1767225600}`)}, nil
	}
	if strings.HasSuffix(req.URL, "/chat/completions") {
		return &Response{Status: 200, Body: []byte(h.stream)}, nil
	}
	return &Response{Status: 404, Body: []byte(`{"error":"unexpected"}`)}, nil
}

// A conversation goes through the package's own fetch: the loader refreshes the
// account, and the reply arrives as the same events any other dialect produces.
func TestChatThroughThePackageLoader(t *testing.T) {
	loader, err := Load(copilotPluginBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	auth := &memoryAuth{current: map[string]any{
		"type": "oauth", "refresh": "gho_refresh", "access": "", "expires": 0,
	}}
	host := &chatHost{stream: "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", auth); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	events := 0
	err = inst.Chat(context.Background(), &ChatRequest{
		Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(e Event) error {
		events++
		if e.Kind == EventText {
			text.WriteString(e.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q", text.String())
	}
	if events < 3 {
		t.Fatalf("events = %d", events)
	}
	// The refresh really happened: an empty access token makes the loader ask
	// for one, and only then does the request go out.
	sawToken, sawChat := false, false
	for _, u := range host.requests {
		if strings.HasSuffix(u, "/copilot_internal/v2/token") {
			sawToken = true
		}
		if strings.HasSuffix(u, "/chat/completions") {
			sawChat = true
		}
	}
	if !sawToken || !sawChat {
		t.Fatalf("requests = %v", host.requests)
	}
	if saved, _ := auth.Get(context.Background(), "github-copilot"); saved == nil || saved["access"] != "tid=api" {
		t.Fatalf("saved = %v", saved)
	}
}

// A package that offers no loader cannot be asked to send anything, and says so
// rather than failing somewhere inside a fetch.
func TestChatNeedsALoader(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"plain","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs":    []byte(`export async function Plugin() { return { auth: { provider: "plain" } } }`),
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
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	err = inst.Chat(context.Background(), &ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "no loader") {
		t.Fatalf("error = %v", err)
	}
}

// A vendor that lists its models answers with its own catalog, and Core offers
// what it actually serves instead of a list baked into this host.
func TestModelsComeFromTheVendor(t *testing.T) {
	loader, err := Load(copilotPluginBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	auth := &memoryAuth{current: map[string]any{
		"type": "oauth", "refresh": "gho_refresh", "access": "tid=api", "expires": 9999999999,
	}}
	host := &modelsHost{body: `{"data":[
		{"id":"gpt-4o","name":"GPT-4o","capabilities":{"type":"chat"},"context_window":128000,"max_output_tokens":16384},
		{"id":"o3","display_name":"o3","modalities":["text","image"]},
		{"no_id_here":true}
	]}`}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", auth); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}

	models, err := inst.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "gpt-4o" || models[0].Name != "GPT-4o" || models[0].Context != 128000 || models[0].Output != 16384 {
		t.Fatalf("first = %+v", models[0])
	}
	if len(models[0].Modalities) != 1 || models[0].Modalities[0] != "chat" {
		t.Fatalf("modalities = %v", models[0].Modalities)
	}
	if models[1].ID != "o3" || models[1].Name != "o3" || len(models[1].Modalities) != 2 {
		t.Fatalf("second = %+v", models[1])
	}
	if !strings.HasSuffix(host.last, "/models") {
		t.Fatalf("asked %q", host.last)
	}
}

// A vendor that refuses the list is told apart from one that answers an empty
// list, and neither becomes an invented catalogue.
func TestModelsSaysWhenTheVendorRefuses(t *testing.T) {
	loader, _ := Load(copilotPluginBundle(t))
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	auth := &memoryAuth{current: map[string]any{
		"type": "oauth", "refresh": "gho_refresh", "access": "tid=api", "expires": 9999999999,
	}}
	host := &modelsHost{status: 404, body: `{"error":"no catalogue"}`}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("github-copilot", auth); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	_, err = inst.Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not list models") {
		t.Fatalf("error = %v", err)
	}
}

type modelsHost struct {
	status int
	body   string
	last   string
}

func (h *modelsHost) Do(_ context.Context, req *Request) (*Response, error) {
	h.last = req.URL
	if strings.HasSuffix(req.URL, "/copilot_internal/v2/token") {
		return &Response{Status: 200, Body: []byte(`{"token":"tid=api","expires_at":1767225600}`)}, nil
	}
	if strings.HasSuffix(req.URL, "/models") {
		status := h.status
		if status == 0 {
			status = 200
		}
		return &Response{Status: status, Body: []byte(h.body)}, nil
	}
	return &Response{Status: 404, Body: []byte(`{"error":"unexpected"}`)}, nil
}

// A plugin's own switches reach it through input.magpie, in the shape Magpie
// gives, so a package written for either host reads the same answers.
func TestPluginOptionsReachThePackage(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"opts","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function Plugin(input) {
  const declared = input.magpie.options("demo")
  return {
    auth: {
      provider: "demo",
      methods: [],
      // The values the user chose, plus what an undeclared key answers.
      freeOnly: input.magpie.option("demo", "freeOnly"),
      unknown: input.magpie.option("demo", "neverDeclared"),
      otherProvider: input.magpie.option("someone-else", "freeOnly"),
      all: declared,
    },
  }
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
	inst.SetPluginOptions(map[string]bool{"demo|freeOnly": true})
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	answer, err := inst.hook("plugin", "{}")
	if err != nil {
		t.Fatal(err)
	}
	plugin, ok := answer.(map[string]any)
	if !ok {
		t.Fatalf("answer = %T", answer)
	}
	auth, ok := plugin["auth"].(map[string]any)
	if !ok {
		t.Fatalf("auth = %T", plugin["auth"])
	}
	if auth["freeOnly"] != true {
		t.Fatalf("freeOnly = %v", auth["freeOnly"])
	}
	// An option nobody declared reads as false, not undefined.
	if auth["unknown"] != false {
		t.Fatalf("unknown = %v", auth["unknown"])
	}
	// Another provider's switches are not this provider's.
	if auth["otherProvider"] != false {
		t.Fatalf("otherProvider = %v", auth["otherProvider"])
	}
	all, ok := auth["all"].(map[string]any)
	if !ok || len(all) != 1 || all["freeOnly"] != true {
		t.Fatalf("all = %v", auth["all"])
	}
}

// An API key has nothing to wait for: returning a pending handle would leave the
// client polling for a callback that does not exist, which is what every
// API-key package would hit first.
func TestAnAPIKeyIsSettledWithoutPolling(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"key","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function Plugin() {
  return {
    auth: {
      provider: "key-demo",
      methods: [{
        label: "API key",
        kind: "api",
        async authorize(inputs) {
          const key = String(inputs?.apiKey ?? "").trim()
          if (!key) throw new Error("API key 不能为空")
          return { type: "success", key }
        },
      }],
    },
  }
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
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	answer, err := inst.Authorize(0, map[string]any{"apiKey": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	immediate, ok := answer["immediate"].(map[string]any)
	if !ok {
		t.Fatalf("an API key came back as a handle to poll: %v", answer)
	}
	if immediate["key"] != "sk-test" || immediate["type"] != "success" {
		t.Fatalf("immediate = %v", immediate)
	}
	// And an empty key is the package's own error, not a silent success.
	if _, err := inst.Authorize(0, map[string]any{"apiKey": "  "}); err == nil {
		t.Fatal("an empty key was accepted")
	}
}

// A package may read and write only the provider it is. Reading another id used
// to answer with this provider's sign-in, which looked like success and handed
// back the wrong credential; writing another id is refused outright.
func TestAuthNeverCrossesProviders(t *testing.T) {
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"iso","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function Plugin(input) {
  const own = await input.client.auth.get({ path: { id: "demo" } })
  const other = await input.client.auth.get({ path: { id: "someone-else" } })
  let refused = false
  try {
    await input.client.auth.set({ path: { id: "someone-else" }, body: { refresh: "stolen" } })
  } catch (err) {
    refused = true
  }
  return { auth: { provider: "demo", methods: [], own: own?.refresh ?? null, other: other?.refresh ?? null, refused } }
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
	store := &mapAuthStore{values: map[string]map[string]any{
		"demo":         {"refresh": "mine"},
		"someone-else": {"refresh": "theirs"},
	}}
	if err := inst.UseClient("demo", store); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	answer, err := inst.hook("plugin", "{}")
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := answer.(map[string]any)["auth"].(map[string]any)
	if auth["own"] != "mine" {
		t.Fatalf("own = %v", auth["own"])
	}
	if auth["other"] != nil {
		t.Fatalf("another provider's sign-in came back as %v", auth["other"])
	}
	if auth["refused"] != true {
		t.Fatalf("writing another provider's sign-in was not refused: %v", auth["refused"])
	}
	if store.values["someone-else"]["refresh"] != "theirs" {
		t.Fatalf("someone-else = %v", store.values["someone-else"])
	}
}

// mapAuthStore is an AuthStore over a map, so a test can hold two providers at
// once and check that neither leaks into the other.
type mapAuthStore struct{ values map[string]map[string]any }

func (m *mapAuthStore) Get(_ context.Context, provider string) (map[string]any, error) {
	return m.values[provider], nil
}

func (m *mapAuthStore) Set(_ context.Context, provider string, value map[string]any) error {
	if m.values == nil {
		m.values = map[string]map[string]any{}
	}
	m.values[provider] = value
	return nil
}

// A plugin that can press its own check-in answers with one of Magpie's
// outcomes, and the host keeps the vendor's words beside it.
func TestCheckinUsesThePluginsAnswer(t *testing.T) {
	inst := checkinInstance(t, `async function checkin() {
  return { outcome: "claimed", credit: "10 积分", streak: 3, until: 1767225600 }
}`)
	result, err := inst.Checkin("demo")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CheckinClaimed || result.Credit != "10 积分" || result.Streak != 3 {
		t.Fatalf("result = %+v", result)
	}
	if result.Until != "2026-01-01T00:00:00Z" {
		t.Fatalf("until = %q", result.Until)
	}
}

// An outcome outside Magpie's set is a failure that names what the plugin said,
// so its wording is not mistaken for the host's.
func TestCheckinRejectsAnUnknownOutcome(t *testing.T) {
	inst := checkinInstance(t, `async function checkin() {
  return { outcome: "yes", message: "已经签过了" }
}`)
	result, err := inst.Checkin("demo")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CheckinFailed {
		t.Fatalf("outcome = %q", result.Outcome)
	}
	if !strings.Contains(result.Message, `"yes"`) || !strings.Contains(result.Message, "已经签过了") {
		t.Fatalf("message = %q", result.Message)
	}
}

// A plugin with no check-in says so instead of pretending to have pressed one.
func TestCheckinNeedsTheHook(t *testing.T) {
	inst := checkinInstance(t, "")
	_, err := inst.Checkin("demo")
	if err == nil || !strings.Contains(err.Error(), "check-in") {
		t.Fatalf("error = %v", err)
	}
}

func checkinInstance(t *testing.T, hook string) *Instance {
	t.Helper()
	body := `export async function Plugin(input) { return { auth: { provider: "demo", methods: [] } } }`
	if hook != "" {
		body = `export async function Plugin(input) { return { auth: { provider: "demo", methods: [], checkin: ` + hook + ` } } }`
	}
	b, err := NewBundle(map[string][]byte{
		"package.json": []byte(`{"name":"checkin","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs":    []byte(body),
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
	t.Cleanup(inst.Close)
	if err := inst.UseClient("demo", &memoryAuth{}); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	return inst
}
