package mobile

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/magpieplugin"
)

// magpieHostStub is the client's Host, with HTTP answered for the pinned
// Copilot package's device flow and token refresh. The rest of Host comes from
// the comic-source fake in mobile_test.go, which this test never reaches.
type magpieHostStub struct {
	fakeHost
	mu       sync.Mutex
	requests []map[string]any
	polls    int
}

func (h *magpieHostStub) HTTP(requestJSON string) (string, error) {
	var req map[string]any
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	url, _ := req["url"].(string)
	reply := func(body string) (string, error) {
		encoded := base64.StdEncoding.EncodeToString([]byte(body))
		return fmt.Sprintf(`{"status":200,"headers":{"Content-Type":["application/json"]},"bodyBase64":%q,"finalUrl":%q}`, encoded, url), nil
	}
	switch {
	case strings.HasSuffix(url, "/login/device/code"):
		return reply(`{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":0}`)
	case strings.HasSuffix(url, "/login/oauth/access_token"):
		h.mu.Lock()
		h.polls++
		polls := h.polls
		h.mu.Unlock()
		if polls < 2 {
			return reply(`{"error":"authorization_pending"}`)
		}
		return reply(`{"access_token":"gho_from_host"}`)
	case strings.HasSuffix(url, "/copilot_internal/v2/token"):
		return reply(`{"token":"tid=api","expires_at":1767225600}`)
	case strings.HasSuffix(url, "/chat/completions"):
		// Four events: two text pieces, a finish, and the end marker.
		stream := "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}" + "\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}" + "\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}" + "\n\n" +
			"data: [DONE]" + "\n\n"
		encoded := base64.StdEncoding.EncodeToString([]byte(stream))
		return fmt.Sprintf(`{"status":200,"headers":{"Content-Type":["text/event-stream"]},"bodyBase64":%q,"finalUrl":%q}`, encoded, url), nil
	case strings.HasSuffix(url, "/models"):
		return reply(`{"data":[{"id":"gpt-4o","name":"GPT-4o","context_window":128000,"max_output_tokens":16384}]}`)
	case strings.HasSuffix(url, "/copilot_internal/user"):
		return reply(`{"copilot_plan":"individual","quota_snapshots":{"premium_interactions":{"entitlement":300,"quota_remaining":300,"has_quota":true}}}`)
	}
	return reply(`{"unexpected":true}`)
}

func copilotFiles(t *testing.T) string {
	t.Helper()
	files := map[string]string{}
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
		files[name] = base64.StdEncoding.EncodeToString(body)
	}
	files["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./flow.mjs"}`))
	files["flow.mjs"] = base64.StdEncoding.EncodeToString([]byte(`import { CopilotAuthPlugin } from "./index.mjs"

export async function flow(inputs) {
  const plugin = await CopilotAuthPlugin({ client: globalThis.__magpieClient })
  const method = plugin.auth.methods[0]
  const started = await method.authorize(inputs)
  const result = await started.callback()
  return { provider: plugin.auth.provider, url: started.url, result }
}`))
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A client can install a package, run its device flow through Host.HTTP, and
// get the result as JSON — the whole iOS path minus the platform itself.
func TestMagpieRunsAPackageThroughTheHost(t *testing.T) {
	host := &magpieHostStub{}
	m := NewMagpie(host)
	defer m.Close()
	if err := m.LoadPackage(copilotFiles(t)); err != nil {
		t.Fatal(err)
	}
	got, err := m.Call("flow", `{"deploymentType":"github.com"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var out struct {
		Provider string `json:"provider"`
		URL      string `json:"url"`
		Result   struct {
			Type    string `json:"type"`
			Refresh string `json:"refresh"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "github-copilot" || out.URL != "https://github.com/login/device" {
		t.Fatalf("answer = %s", got)
	}
	if out.Result.Type != "success" || out.Result.Refresh != "gho_from_host" {
		t.Fatalf("result = %+v", out.Result)
	}
	// The request carried no cookies: an AI sign-in is not a comic source's.
	for _, req := range host.requests {
		if req["partition"] != "magpie" {
			t.Fatalf("partition = %v", req["partition"])
		}
	}
}

const refreshFlow = `import { CopilotAuthPlugin } from "./index.mjs"

export async function refresh() {
  const plugin = await CopilotAuthPlugin({ client: globalThis.__magpieClient })
  const getAuth = async () => ({ type: "oauth", refresh: "gho_refresh", access: "", expires: 0 })
  const options = await plugin.auth.loader(getAuth, { models: {} })
  const r = await options.fetch(options.baseURL + "/chat/completions", { method: "POST", body: "{}" })
  return { status: r.status }
}`

// What the package saves through client.auth.set comes back to the client, so
// it can put the new sign-in in Keychain.
// A package downloaded as an archive has to become the same file table a folder
// install produces, through the same method the clients call.
func TestMagpieReadsAPackageArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries := map[string]string{
		"copilot-0.1.0/package.json": `{"name":"archive-demo","version":"0.0.0","main":"./index.mjs"}`,
		"copilot-0.1.0/index.mjs":    "export async function Plugin() { return { auth: { provider: 'archive-demo', methods: [] } } }",
	}
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	m := NewMagpie(&magpieHostStub{})
	defer m.Close()
	filesJSON, err := m.ReadPackageArchive(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var files map[string]string
	if err := json.Unmarshal([]byte(filesJSON), &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	if _, ok := files["index.mjs"]; !ok {
		t.Fatalf("the enclosing directory was not stripped: %v", keysOf(files))
	}
	if err := m.LoadPackage(filesJSON); err != nil {
		t.Fatalf("the extracted table did not load: %v", err)
	}
	answer, err := m.Auth()
	if err != nil {
		t.Fatalf("the extracted package did not answer auth: %v", err)
	}
	if !strings.Contains(answer, "archive-demo") {
		t.Fatalf("auth = %s", answer)
	}
	if _, err := m.ReadPackageArchive([]byte("not an archive")); err == nil {
		t.Fatal("arbitrary bytes were accepted as a package")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMagpieReturnsTheSavedSignIn(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["flow.mjs"] = base64.StdEncoding.EncodeToString([]byte(refreshFlow))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	host := &magpieHostStub{}
	m := NewMagpie(host)
	defer m.Close()
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"","expires":0}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("refresh", ""); err != nil {
		t.Fatalf("call: %v", err)
	}
	saved, err := m.SavedAuth()
	if err != nil || saved == "" {
		t.Fatalf("saved = %q, %v", saved, err)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(saved), &value); err != nil {
		t.Fatal(err)
	}
	if value["access"] != "tid=api" {
		t.Fatalf("saved = %s", saved)
	}
	if value["refresh"] != "gho_refresh" {
		t.Fatalf("refresh token lost: %s", saved)
	}
}

// The sign-in a client shows: the package's own methods, the page to open, and
// the answer after one poll (this package's callback loops by itself).
// A key-based method settles inside authorize, and that has to survive the trip
// through gomobile as well: the client decides from this JSON whether to poll.
func TestMagpieReturnsAnImmediateKey(t *testing.T) {
	host := &magpieHostStub{}
	m := NewMagpie(host)
	defer m.Close()
	files := map[string]string{
		"package.json": base64.StdEncoding.EncodeToString([]byte(`{"name":"key-demo","version":"0.0.0","main":"./index.mjs"}`)),
	}
	files["index.mjs"] = base64.StdEncoding.EncodeToString([]byte(`export async function Plugin() {
  return {
    auth: {
      provider: "key-demo",
      methods: [{ label: "API key", type: "api",
        prompts: [{ type: "password", key: "token", message: "API Token" }],
        async authorize(inputs) {
          const token = String(inputs?.token ?? "").trim()
          if (!token) return { type: "failed", error: "token required" }
          return { type: "success", key: token, metadata: { region: "eu" } }
        } }],
    },
  }
}`))
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	spec, err := m.Auth()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec, "prompts") || !strings.Contains(spec, "token") {
		t.Fatalf("the method's prompts did not survive: %s", spec)
	}
	answer, err := m.Authorize(0, `{"token":"sk-live"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "immediate") || !strings.Contains(answer, "sk-live") {
		t.Fatalf("authorize = %s", answer)
	}
	if !strings.Contains(answer, "eu") {
		t.Fatalf("the key's metadata did not survive: %s", answer)
	}
}

func TestMagpieSignsInThroughThePluginABI(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	// The package as shipped: its entry is its own index.mjs, so the bridge has
	// to find the named export rather than a default one.
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	delete(decoded, "flow.mjs")
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}

	host := &magpieHostStub{}
	m := NewMagpie(host)
	defer m.Close()
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	auth, err := m.Auth()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(auth, `"provider":"github-copilot"`) {
		t.Fatalf("auth = %s", auth)
	}
	started, err := m.Authorize(0, `{"deploymentType":"github.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		ID           int64  `json:"id"`
		URL          string `json:"url"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(started), &start); err != nil {
		t.Fatal(err)
	}
	if start.URL != "https://github.com/login/device" || start.ID == 0 {
		t.Fatalf("started = %s", started)
	}
	answer, err := m.Poll(start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "gho_from_host") {
		t.Fatalf("answer = %s", answer)
	}
	// The usage body's own shape belongs to the package, not to us: the bridge
	// only has to hand over what it answered without inventing numbers.
	usage, err := m.Usage("github-copilot")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(usage), &parsed); err != nil {
		t.Fatalf("usage = %q: %v", usage, err)
	}
	if _, ok := parsed["windows"]; !ok {
		t.Fatalf("usage = %s", usage)
	}
}

// A sign-in nobody finishes can be dropped: its id stops working, so the client
// does not wait on a callback that will never answer.
func TestMagpieCancelsASignIn(t *testing.T) {
	host := &magpieHostStub{}
	m := NewMagpie(host)
	defer m.Close()
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	started, err := m.Authorize(0, `{"deploymentType":"github.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(started), &start); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelSignIn(start.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Poll(start.ID); err == nil {
		t.Fatal("a cancelled sign-in still polls")
	}
}

// A conversation is delivered event by event, because gomobile cannot push:
// the client asks, gets what is ready, and asks again.
func TestMagpieChatDeliversEvents(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	host := &magpieHostStub{}
	m := NewMagpie(host)
	defer m.Close()
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"","expires":0}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}

	started, err := m.ChatStart("gpt-4o", `[{"role":"user","content":"hi"}]`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(started), &start); err != nil {
		t.Fatal(err)
	}
	handle := start.ID
	var text strings.Builder
	var kinds []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		event, err := m.ChatNext(handle)
		if err != nil {
			t.Fatal(err)
		}
		if event == "" {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		var parsed struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(event), &parsed); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, parsed.Kind)
		if parsed.Kind == "text" {
			text.WriteString(parsed.Text)
		}
		if parsed.Kind == "end" {
			break
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q (kinds %v)", text.String(), kinds)
	}
	if kinds[len(kinds)-1] != "end" {
		t.Fatalf("kinds = %v", kinds)
	}
	// The refresh happened through the package, not through us.
	sawToken := false
	for _, req := range host.requests {
		if url, _ := req["url"].(string); strings.HasSuffix(url, "/copilot_internal/v2/token") {
			sawToken = true
		}
	}
	if !sawToken {
		t.Fatal("no token refresh was requested")
	}
}

func TestMagpieChatCancelEndsIt(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMagpie(&magpieHostStub{})
	defer m.Close()
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"tid=api","expires":9999999999}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	started, err := m.ChatStart("gpt-4o", `[{"role":"user","content":"hi"}]`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(started), &start); err != nil {
		t.Fatal(err)
	}
	handle := start.ID
	m.ChatCancel(handle)
	// After cancellation the handle is gone, which the client reads as an end.
	event, err := m.ChatNext(handle)
	if err != nil {
		t.Fatal(err)
	}
	if event != `{"kind":"end"}` && event != "" {
		t.Fatalf("event = %s", event)
	}
}

// The client gets the normalised form, not the vendor's own JSON: one card can
// then show any provider.
func TestMagpieUsageSummaryIsNormalised(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMagpie(&magpieHostStub{})
	defer m.Close()
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"tid=api","expires":9999999999}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	summary, err := m.UsageSummary("github-copilot")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatalf("summary = %q: %v", summary, err)
	}
	// The shape is ours, not the vendor's: these keys only exist after
	// normalisation.
	for _, key := range []string{"windows", "plan", "signIn", "resets"} {
		if _, ok := parsed[key]; !ok {
			t.Fatalf("summary missing %q: %s", key, summary)
		}
	}
}

// The model list is the vendor's, asked for through the package's own fetch.
func TestMagpieListsModels(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMagpie(&magpieHostStub{})
	defer m.Close()
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"tid=api","expires":9999999999}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	models, err := m.Models()
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Context int64  `json:"context"`
	}
	if err := json.Unmarshal([]byte(models), &list); err != nil {
		t.Fatalf("models = %q: %v", models, err)
	}
	if len(list) != 1 || list[0].ID != "gpt-4o" || list[0].Context != 128000 {
		t.Fatalf("models = %+v", list)
	}
}

// A runner that goes away must stop its conversations: a package writing into a
// channel nobody reads is a leak the client cannot see.
func TestMagpieCloseStopsRunningChats(t *testing.T) {
	files := copilotFiles(t)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(files), &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "flow.mjs")
	decoded["package.json"] = base64.StdEncoding.EncodeToString([]byte(`{"name":"copilot-flow","version":"0.0.0","main":"./index.mjs"}`))
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMagpie(&magpieHostStub{})
	if err := m.SetAuth("github-copilot", `{"type":"oauth","refresh":"gho_refresh","access":"tid=api","expires":9999999999}`); err != nil {
		t.Fatal(err)
	}
	if err := m.LoadPackage(string(encoded)); err != nil {
		t.Fatal(err)
	}
	started, err := m.ChatStart("gpt-4o", `[{"role":"user","content":"hi"}]`, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(started), &start); err != nil {
		t.Fatal(err)
	}
	m.Close()
	// The handle is gone, which a client reads as the end of the conversation.
	event, err := m.ChatNext(start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if event != "" && event != `{"kind":"end"}` {
		t.Fatalf("event after close = %s", event)
	}
	if _, err := m.ChatStart("gpt-4o", `[]`, "", 0); err == nil {
		t.Fatal("a closed runner started a conversation")
	}
}

func TestMagpieRefusesBadInput(t *testing.T) {
	m := NewMagpie(&magpieHostStub{})
	if err := m.LoadPackage("{"); err == nil {
		t.Fatal("broken JSON accepted")
	}
	if err := m.LoadPackage(`{"package.json":"!!!"}`); err == nil {
		t.Fatal("bad base64 accepted")
	}
	if _, err := m.Call("Plugin", ""); err == nil {
		t.Fatal("call without a package accepted")
	}
	if err := m.LoadPackage(copilotFiles(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("NoSuchExport", ""); err == nil {
		t.Fatal("missing export accepted")
	}
	if _, err := m.Call("flow", "{"); err == nil {
		t.Fatal("bad argument accepted")
	}
	m.Close()
	if _, err := m.Call("flow", ""); !errors.Is(err, ErrMagpieClosed) {
		t.Fatalf("closed instance = %v", err)
	}
}

// templateFiles packs one of Core's own provider templates, which travel with the
// repository: the audit fixtures live outside it and make these tests skip in CI.
func templateFiles(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join("..", "magpieplugin", "testdata", dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("template %s: %v", dir, err)
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = base64.StdEncoding.EncodeToString(body)
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A package with no check-in says so, in English, rather than returning an empty
// answer the app would show as "done".
func TestMagpieRefusesACheckinItDoesNotHave(t *testing.T) {
	m := NewMagpie(&magpieHostStub{})
	defer m.Close()
	if err := m.LoadPackage(templateFiles(t, "openai-compatible")); err != nil {
		t.Fatal(err)
	}
	_, err := m.Checkin("openai-compatible")
	if err == nil {
		t.Fatal("a package without a check-in answered as if it had one")
	}
	if !strings.Contains(err.Error(), "declares no check-in") {
		t.Fatalf("error = %v", err)
	}
}

// Plugin options are stored per provider and handed to the package; malformed JSON
// is an error rather than being half-applied.
func TestMagpieStoresPluginOptions(t *testing.T) {
	m := NewMagpie(&magpieHostStub{})
	defer m.Close()
	if err := m.LoadPackage(templateFiles(t, "openai-compatible")); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPluginOptions(`{"openai-compatible|reasoning":true}`); err != nil {
		t.Fatalf("set options: %v", err)
	}
	if err := m.SetPluginOptions(`{broken`); err == nil {
		t.Fatal("malformed options were accepted")
	}
	// Clearing them is allowed and leaves the package with its defaults.
	if err := m.SetPluginOptions(""); err != nil {
		t.Fatalf("clear options: %v", err)
	}
}

// The app asks "was the reply cut short" and must not have to know which vendor
// answered: the event carries the vendor's own word and this core's reading of it.
func TestChatEventCarriesBothEndings(t *testing.T) {
	body, err := chatEventJSON(magpieplugin.Event{
		Kind:    magpieplugin.EventDone,
		Finish:  "max_tokens",
		Outcome: magpieplugin.FinishLength,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("event is not JSON: %v", err)
	}
	if payload["finish"] != "max_tokens" {
		t.Fatalf("finish = %v, want the vendor's own word", payload["finish"])
	}
	if payload["outcome"] != "length" {
		t.Fatalf("outcome = %v, want the normalised ending", payload["outcome"])
	}
}

// Nothing normalised means no field: an empty outcome would read as "finished".
func TestChatEventOmitsAnEmptyOutcome(t *testing.T) {
	body, err := chatEventJSON(magpieplugin.Event{Kind: magpieplugin.EventText, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("event is not JSON: %v", err)
	}
	if _, present := payload["outcome"]; present {
		t.Fatalf("outcome should be absent, got %v", payload["outcome"])
	}
}
