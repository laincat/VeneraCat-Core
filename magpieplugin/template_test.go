package magpieplugin

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The template package is the shape a pure-Web provider takes: it needs fetch,
// Headers and URL, and nothing from Node. Keeping it as a fixture means the
// example cannot rot: the tests below load the files as they are.
func templateBundle(t *testing.T) *Bundle {
	t.Helper()
	dir := filepath.Join("testdata", "openai-compatible")
	files := map[string][]byte{}
	for _, name := range []string{"package.json", "index.mjs"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = body
	}
	b, err := NewBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func templateInstance(t *testing.T, host Host, auth AuthStore) *Instance {
	t.Helper()
	loader, err := Load(templateBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inst.Close)
	if host != nil {
		if err := inst.UseHost(host); err != nil {
			t.Fatal(err)
		}
	}
	if auth != nil {
		if err := inst.UseClient("openai-compatible", auth); err != nil {
			t.Fatal(err)
		}
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	return inst
}

// templateHost answers the two endpoints the template calls and keeps the last
// request, so a test can check the header the package added.
type templateHost struct {
	last   string
	header map[string][]string
	models string
	reply  string
	status int
}

func (h *templateHost) Do(_ context.Context, req *Request) (*Response, error) {
	h.last = req.URL
	h.header = req.Header
	status := h.status
	if status == 0 {
		status = 200
	}
	body := h.reply
	if strings.HasSuffix(req.URL, "/models") {
		body = h.models
	}
	return &Response{Status: status, Body: []byte(body), URL: req.URL}, nil
}

func templateAuth() *memoryAuth {
	return &memoryAuth{current: map[string]any{
		"type":     "api",
		"key":      "sk-test",
		"metadata": map[string]any{"baseURL": "https://example.test/v1"},
	}}
}

// A client must be able to describe the sign-in screen without knowing the
// vendor: the label, the field keys, and which field is secret.
func TestTemplateDescribesItsSignIn(t *testing.T) {
	inst := templateInstance(t, nil, nil)
	spec, err := inst.PluginAuth()
	if err != nil {
		t.Fatal(err)
	}
	if spec["provider"] != "openai-compatible" {
		t.Fatalf("provider = %v", spec["provider"])
	}
	methods, _ := spec["methods"].([]any)
	if len(methods) != 1 {
		t.Fatalf("methods = %+v", methods)
	}
	method, _ := methods[0].(map[string]any)
	prompts, _ := method["prompts"].([]any)
	if len(prompts) != 2 {
		t.Fatalf("prompts = %+v", prompts)
	}
	first, _ := prompts[0].(map[string]any)
	second, _ := prompts[1].(map[string]any)
	if first["key"] != "baseURL" || second["key"] != "apiKey" {
		t.Fatalf("prompt keys = %v / %v", first["key"], second["key"])
	}
	if second["type"] != "password" {
		t.Fatalf("the key field should be secret, got %v", second["type"])
	}
}

// A key settles at once: the host must not poll a callback that does not exist.
func TestTemplateSignsInWithAKey(t *testing.T) {
	inst := templateInstance(t, nil, nil)
	started, err := inst.Authorize(0, map[string]any{
		"apiKey":  "sk-test",
		"baseURL": "https://example.test/v1/",
	})
	if err != nil {
		t.Fatal(err)
	}
	immediate, ok := started["immediate"].(map[string]any)
	if !ok {
		t.Fatalf("no immediate result: %+v", started)
	}
	if immediate["type"] != "success" || immediate["key"] != "sk-test" {
		t.Fatalf("immediate = %+v", immediate)
	}
	metadata, _ := immediate["metadata"].(map[string]any)
	if metadata["baseURL"] != "https://example.test/v1" {
		t.Fatalf("base URL kept as %v (trailing slash should be gone)", metadata["baseURL"])
	}
}

// The loader turns stored metadata into the request the vendor sees: the base
// URL kept at sign-in, and an Authorization header the package added itself.
func TestTemplateListsModelsWithItsOwnHeader(t *testing.T) {
	host := &templateHost{models: `{"data":[{"id":"gpt-4o"},{"id":"o3"}]}`}
	inst := templateInstance(t, host, templateAuth())
	models, err := inst.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-4o" {
		t.Fatalf("models = %+v", models)
	}
	if host.last != "https://example.test/v1/models" {
		t.Fatalf("asked %s", host.last)
	}
	t.Logf("host saw %s with headers %v", host.last, host.header)
	if got := headerValue(host.header, "Authorization"); got != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", got)
	}
}

// A streamed reply arrives as text events, with the same headers.
func TestTemplateStreamsAChat(t *testing.T) {
	// SSE dispatches an event at a blank line; without one the three data lines are
	// a single unterminated event and nothing arrives.
	host := &templateHost{reply: strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")}
	inst := templateInstance(t, host, templateAuth())
	var text strings.Builder
	err := inst.Chat(context.Background(), &ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(event Event) error {
		if event.Kind == EventText {
			text.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q", text.String())
	}
	if !strings.HasSuffix(host.last, "/chat/completions") {
		t.Fatalf("posted to %s", host.last)
	}
	t.Logf("chat posted to %s with headers %v", host.last, host.header)
	if got := headerValue(host.header, "Authorization"); got != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", got)
	}
}

// The Anthropic-shaped template declares a dialect and its own header names; the
// host builds a Messages body and reads Messages events without knowing the
// vendor, which is the point of the split.
func anthropicInstance(t *testing.T, host Host) *Instance {
	t.Helper()
	dir := filepath.Join("testdata", "anthropic-compatible")
	files := map[string][]byte{}
	for _, name := range []string{"package.json", "index.mjs"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = body
	}
	b, err := NewBundle(files)
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
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	auth := &memoryAuth{current: map[string]any{
		"type":     "api",
		"key":      "sk-ant-test",
		"metadata": map[string]any{"baseURL": "https://anthropic.test/v1"},
	}}
	if err := inst.UseClient("anthropic-compatible", auth); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestAnthropicTemplateStreamsMessages(t *testing.T) {
	host := &templateHost{reply: strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n\n")}
	inst := anthropicInstance(t, host)
	var text strings.Builder
	err := inst.Chat(context.Background(), &ChatRequest{
		Model:    "claude-test",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(event Event) error {
		if event.Kind == EventText {
			text.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hi" {
		t.Fatalf("text = %q", text.String())
	}
	if host.last != "https://anthropic.test/v1/messages" {
		t.Fatalf("posted to %s", host.last)
	}
	if got := headerValue(host.header, "x-api-key"); got != "sk-ant-test" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := headerValue(host.header, "anthropic-version"); got == "" {
		t.Fatal("the package's own version header did not travel")
	}
}

// A template has to survive the way a user actually installs one: zipped with the
// files at the root, expanded by Core, then loaded. This is the only test that
// checks the packaging step and the package content together.
func TestTemplateSurvivesBeingZipped(t *testing.T) {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	dir := filepath.Join("testdata", "openai-compatible")
	for _, name := range []string{"package.json", "index.mjs"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	expanded, err := ReadArchive(buf.Bytes())
	if err != nil {
		t.Fatalf("the zipped template was not accepted: %v", err)
	}
	b, err := NewBundle(expanded)
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
	spec, err := inst.PluginAuth()
	if err != nil {
		t.Fatal(err)
	}
	if spec["provider"] != "openai-compatible" {
		t.Fatalf("provider = %v", spec["provider"])
	}
}

// The third dialect takes the same route: the package says "responses", the host
// posts to /responses and reads its events. This is the one path a template did
// not cover, and the path a package cannot guess for itself.
func TestResponsesDialectUsesItsOwnPath(t *testing.T) {
	files := map[string][]byte{
		"package.json": []byte(`{"name":"responses-shape","version":"0.0.0","main":"./index.mjs"}`),
		"index.mjs": []byte(`export async function P() {
  return {
    auth: {
      provider: "responses-shape",
      methods: [],
      async loader() {
        const call = (url, init) => fetch(url, init)
        return { baseURL: "https://responses.test/v1", dialect: "responses", fetch: call }
      },
    },
  }
}`),
	}
	b, err := NewBundle(files)
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

	host := &templateHost{reply: strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hi"}`,
		"",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n\n")}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	if err := inst.UseClient("responses-shape", &memoryAuth{current: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	var done bool
	err = inst.Chat(context.Background(), &ChatRequest{
		Model:    "gpt-5",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(event Event) error {
		switch event.Kind {
		case EventText:
			text.WriteString(event.Text)
		case EventDone:
			done = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hi" {
		t.Fatalf("text = %q", text.String())
	}
	if !done {
		t.Fatal("the completed event never arrived")
	}
	if host.last != "https://responses.test/v1/responses" {
		t.Fatalf("posted to %s", host.last)
	}
}

// A package that reports no usage must read as "the vendor did not say", not as a
// zero. The template has no usage hook at all, which is the common case.
func TestTemplateReportsNoUsageAsSuch(t *testing.T) {
	inst := templateInstance(t, &templateHost{}, templateAuth())
	usage, err := inst.Usage("openai-compatible")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nothing invented", usage)
	}
}

// A package without a check-in says so, instead of pretending it claimed
// something.
func TestTemplateHasNoCheckin(t *testing.T) {
	inst := templateInstance(t, &templateHost{}, templateAuth())
	_, err := inst.Checkin("openai-compatible")
	if err == nil {
		t.Fatal("a package with no check-in answered as if it had one")
	}
	if !strings.Contains(err.Error(), "check-in") {
		t.Fatalf("error = %v", err)
	}
}
