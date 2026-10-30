package magpieplugin

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A real key-based package settles inside authorize and carries metadata the
// loader needs. Both are pinned here because a key that loses its metadata
// produces a URL with a placeholder in it, which fails much later and looks like
// a network problem.
func TestCloudflareAPIKeySignsInAtOnce(t *testing.T) {
	b := bundleFromDir(t, filepath.Join(communityAuditDir, "cloudflare-workers-ai"))
	loader, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(&pluginHostStub{}); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	answer, err := inst.Authorize(0, map[string]any{"apiKey": "cf-test", "accountId": "acc-1"})
	if err != nil {
		t.Fatal(err)
	}
	immediate, ok := answer["immediate"].(map[string]any)
	if !ok {
		t.Fatalf("cloudflare's key sign-in came back as a handle: %v", answer)
	}
	if immediate["key"] != "cf-test" {
		t.Fatalf("key = %v", immediate["key"])
	}
	metadata, ok := immediate["metadata"].(map[string]any)
	if !ok || metadata["accountId"] != "acc-1" {
		t.Fatalf("metadata = %v, and the loader builds its base URL from it", immediate["metadata"])
	}
}

// A key-based method declares the fields to ask for. Without them a client has
// nowhere to type the key, and the package looks broken rather than unsigned-in.
func TestCloudflareMethodDeclaresItsPrompts(t *testing.T) {
	b := bundleFromDir(t, filepath.Join(communityAuditDir, "cloudflare-workers-ai"))
	loader, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	if err := inst.UseHost(&pluginHostStub{}); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	spec, err := inst.PluginAuth()
	if err != nil {
		t.Fatal(err)
	}
	methods, _ := spec["methods"].([]any)
	if len(methods) == 0 {
		t.Fatal("no methods")
	}
	method, _ := methods[0].(map[string]any)
	prompts, _ := method["prompts"].([]any)
	if len(prompts) != 2 {
		t.Fatalf("prompts = %v", method["prompts"])
	}
	first, _ := prompts[0].(map[string]any)
	if first["key"] != "token" || first["type"] != "password" {
		t.Fatalf("first prompt = %v", first)
	}
	if method["kind"] != "api" {
		t.Fatalf("kind = %v, and the method says type: api", method["kind"])
	}
}

// cloudflareRecorder answers the two calls a chat makes: model discovery and the
// completion, keeping the URL and headers so the test can check that the account
// id reached the base URL and the token reached the header.
type cloudflareRecorder struct {
	mu       sync.Mutex
	urls     []string
	authz    []string
	complete string
}

func (h *cloudflareRecorder) Do(_ context.Context, req *Request) (*Response, error) {
	h.mu.Lock()
	h.urls = append(h.urls, req.URL)
	h.authz = append(h.authz, strings.Join(req.Header["authorization"], ","))
	h.mu.Unlock()
	if strings.HasSuffix(req.URL, "/chat/completions") {
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"
		return &Response{Status: 200, Body: []byte(body)}, nil
	}
	return &Response{Status: 404, Body: []byte(`{}`)}, nil
}

// The account id a key arrived with is not decoration: the package interpolates
// it into its base URL. A key stored without it leaves a URL with a placeholder
// in it, and the failure surfaces much later as a network error.
func TestCloudflareKeyChatUsesTheAccountId(t *testing.T) {
	b := bundleFromDir(t, filepath.Join(communityAuditDir, "cloudflare-workers-ai"))
	loader, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	host := &cloudflareRecorder{}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	auth := &memoryAuth{current: map[string]any{
		"type":     "api",
		"key":      "cf-token",
		"metadata": map[string]any{"accountId": "0123456789abcdef0123456789abcdef"},
	}}
	if err := inst.UseClient("cloudflare-workers-ai", auth); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	err = inst.Chat(context.Background(), &ChatRequest{
		Model:    "@cf/meta/llama-3-8b-instruct",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(e Event) error {
		if e.Kind == EventText {
			text.WriteString(e.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if text.String() != "ok" {
		t.Fatalf("text = %q", text.String())
	}
	joined := strings.Join(host.urls, " ")
	if !strings.Contains(joined, "0123456789abcdef0123456789abcdef") {
		t.Fatalf("the account id never reached the URL: %v", host.urls)
	}
	if strings.Contains(joined, "${") {
		t.Fatalf("a placeholder survived into the URL: %v", host.urls)
	}
	for _, header := range host.authz {
		if !strings.Contains(header, "Bearer cf-token") && !strings.Contains(header, "cf-token") {
			t.Fatalf("the token never reached the request headers: %v", host.authz)
		}
	}
}

// Model discovery for a key-based package: it goes through the package's own
// models hook, so whatever the vendor answers (or refuses) is what the client
// sees. A refusal must be an error naming the status, not an empty list that
// looks like "this account has no models".
func TestCloudflareModelsComeFromTheVendorOrFailLoudly(t *testing.T) {
	b := bundleFromDir(t, filepath.Join(communityAuditDir, "cloudflare-workers-ai"))
	loader, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := loader.NewInstance(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	host := &cloudflareRecorder{}
	if err := inst.UseHost(host); err != nil {
		t.Fatal(err)
	}
	auth := &memoryAuth{current: map[string]any{
		"type":     "api",
		"key":      "cf-token",
		"metadata": map[string]any{"accountId": "0123456789abcdef0123456789abcdef"},
	}}
	if err := inst.UseClient("cloudflare-workers-ai", auth); err != nil {
		t.Fatal(err)
	}
	if err := inst.InstallPluginBridge(); err != nil {
		t.Fatal(err)
	}
	_, err = inst.Models(context.Background())
	if err == nil {
		t.Fatal("a package with no model endpoint answered a list anyway")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Fatalf("the error does not say what failed: %v", err)
	}
}
