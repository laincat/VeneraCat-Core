package aiapi

import (
	"encoding/json"
	"strings"
	"testing"
)

const markerKey = "sk-marker-9f3a-do-not-leak"

func config(protocol Protocol) Config {
	return Config{Protocol: protocol, APIKey: markerKey}
}

func TestBuildRequestUsesTheProtocolEndpointAndCredentials(t *testing.T) {
	cases := []struct {
		protocol Protocol
		url      string
		header   string
		value    string
	}{
		{Chat, "https://api.openai.com/v1/chat/completions", "Authorization", "Bearer " + markerKey},
		{Responses, "https://api.openai.com/v1/responses", "Authorization", "Bearer " + markerKey},
		{Messages, "https://api.anthropic.com/v1/messages", "x-api-key", markerKey},
	}
	bodies := map[Protocol]string{
		Chat:      `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		Responses: `{"model":"gpt-4o","input":"hi"}`,
		Messages:  `{"model":"claude","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`,
	}
	for _, c := range cases {
		request, err := BuildRequest(config(c.protocol), json.RawMessage(bodies[c.protocol]))
		if err != nil {
			t.Fatalf("%s: %v", c.protocol, err)
		}
		if request.URL != c.url {
			t.Fatalf("%s url = %s", c.protocol, request.URL)
		}
		if got := request.Header.Get(c.header); got != c.value {
			t.Fatalf("%s %s = %q", c.protocol, c.header, got)
		}
		if request.Cookies {
			t.Fatalf("%s enabled cookies", c.protocol)
		}
		if request.Method != "POST" {
			t.Fatalf("%s method = %s", c.protocol, request.Method)
		}
	}
}

func TestAnthropicVersionHeader(t *testing.T) {
	cfg := config(Messages)
	request, err := BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[],"max_tokens":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("anthropic-version"); got != defaultAnthropicVersion {
		t.Fatalf("anthropic-version = %q", got)
	}
	cfg.AnthropicVersion = "2024-10-22"
	request, err = BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[],"max_tokens":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("anthropic-version"); got != "2024-10-22" {
		t.Fatalf("anthropic-version = %q", got)
	}
}

func TestCustomBaseURLIsTakenAsTheWholeRoot(t *testing.T) {
	cfg := config(Chat)
	cfg.BaseURL = "https://gateway.example/openai"
	request, err := BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.URL != "https://gateway.example/openai/chat/completions" {
		t.Fatalf("url = %s", request.URL)
	}
	cfg.BaseURL = "https://gateway.example/openai/"
	request, err = BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.URL != "https://gateway.example/openai/chat/completions" {
		t.Fatalf("url = %s", request.URL)
	}
}

func TestModelsRequestIsAGetWithoutBody(t *testing.T) {
	for _, protocol := range []Protocol{Chat, Responses, Messages} {
		request, err := BuildModelsRequest(config(protocol))
		if err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		if request.Method != "GET" || len(request.Body) != 0 {
			t.Fatalf("%s method/body = %s/%d", protocol, request.Method, len(request.Body))
		}
		if request.Cookies {
			t.Fatalf("%s enabled cookies", protocol)
		}
		if !strings.HasSuffix(request.URL, "/models") {
			t.Fatalf("%s url = %s", protocol, request.URL)
		}
	}
}

func TestBodyIsKeptByteForByte(t *testing.T) {
	// A number a float64 could not hold, plus vendor fields Core does not know.
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}],"tools":[{"type":"function","function":{"name":"f"}}],"max_completion_tokens":9007199254740993,"vendor_extra":{"deep":{"a":[1,2]}},"stream":false}`
	request, err := BuildRequest(config(Chat), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if string(request.Body) != body {
		t.Fatalf("body changed:\n got %s\nwant %s", request.Body, body)
	}
}

func TestStreamFieldIsPassedThroughUnchanged(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		body := `{"model":"m","messages":[],"stream":` + value + `}`
		request, err := BuildRequest(config(Chat), json.RawMessage(body))
		if err != nil {
			t.Fatal(err)
		}
		if string(request.Body) != body {
			t.Fatalf("body changed: %s", request.Body)
		}
	}
}

func TestCallerCannotChangeTheBodyAfterBuilding(t *testing.T) {
	body := json.RawMessage(`{"model":"m","messages":[]}`)
	request, err := BuildRequest(config(Chat), body)
	if err != nil {
		t.Fatal(err)
	}
	copy(body, `{"model":"other","messages":[]}`)
	if string(request.Body) != `{"model":"m","messages":[]}` {
		t.Fatalf("body followed the caller: %s", request.Body)
	}
}

func TestCookiesAreNeverEnabledWhateverThePartitionIs(t *testing.T) {
	for _, partition := range []string{"", "comic:source", "magpie"} {
		cfg := config(Chat)
		cfg.Partition = partition
		request, err := BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if request.Cookies {
			t.Fatalf("partition %q enabled cookies", partition)
		}
		if request.Partition != partition {
			t.Fatalf("partition = %q, want %q", request.Partition, partition)
		}
	}
}

func TestBodyCannotOverrideAuthentication(t *testing.T) {
	body := `{"model":"m","messages":[],"headers":{"Authorization":"Bearer stolen","x-api-key":"stolen"},"Authorization":"Bearer stolen"}`
	request, err := BuildRequest(config(Chat), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+markerKey {
		t.Fatalf("Authorization = %q", got)
	}
	if len(request.Header["Authorization"]) != 1 {
		t.Fatalf("duplicate Authorization headers: %v", request.Header)
	}
	if request.Header.Get("x-api-key") != "" {
		t.Fatalf("x-api-key leaked into an OpenAI request: %v", request.Header)
	}
}

func TestRejectedInputs(t *testing.T) {
	cases := []struct {
		name     string
		protocol Protocol
		base     string
		key      string
		body     string
	}{
		{"no protocol", "", "", markerKey, `{"model":"m","messages":[]}`},
		{"unknown protocol", "gemini", "", markerKey, `{"model":"m","messages":[]}`},
		{"no key", Chat, "", "", `{"model":"m","messages":[]}`},
		{"blank key", Chat, "", "   ", `{"model":"m","messages":[]}`},
		{"key with control character", Chat, "", "abc\ndef", `{"model":"m","messages":[]}`},
		{"relative base", Chat, "api.example/v1", markerKey, `{"model":"m","messages":[]}`},
		{"base with scheme", Chat, "ftp://api.example", markerKey, `{"model":"m","messages":[]}`},
		{"base with credentials", Chat, "https://user:pw@api.example/v1", markerKey, `{"model":"m","messages":[]}`},
		{"base with query", Chat, "https://api.example/v1?x=1", markerKey, `{"model":"m","messages":[]}`},
		{"base with fragment", Chat, "https://api.example/v1#f", markerKey, `{"model":"m","messages":[]}`},
		{"empty body", Chat, "", markerKey, ``},
		{"array body", Chat, "", markerKey, `[{"model":"m"}]`},
		{"string body", Chat, "", markerKey, `"model"`},
		{"null body", Chat, "", markerKey, `null`},
		{"two values", Chat, "", markerKey, `{"model":"m","messages":[]}{"model":"n"}`},
		{"repeated key", Chat, "", markerKey, `{"model":"m","model":"n","messages":[]}`},
		{"no model", Chat, "", markerKey, `{"messages":[]}`},
		{"blank model", Chat, "", markerKey, `{"model":"  ","messages":[]}`},
		{"no messages", Chat, "", markerKey, `{"model":"m"}`},
		{"messages not an array", Chat, "", markerKey, `{"model":"m","messages":{}}`},
		{"responses without input", Responses, "", markerKey, `{"model":"m"}`},
		{"responses input number", Responses, "", markerKey, `{"model":"m","input":7}`},
		{"messages without max_tokens", Messages, "", markerKey, `{"model":"m","messages":[]}`},
		{"messages fractional max_tokens", Messages, "", markerKey, `{"model":"m","messages":[],"max_tokens":1.5}`},
		{"messages zero max_tokens", Messages, "", markerKey, `{"model":"m","messages":[],"max_tokens":0}`},
		{"messages negative max_tokens", Messages, "", markerKey, `{"model":"m","messages":[],"max_tokens":-3}`},
		{"stream not boolean", Chat, "", markerKey, `{"model":"m","messages":[],"stream":"yes"}`},
		{"stream number", Chat, "", markerKey, `{"model":"m","messages":[],"stream":1}`},
		{"broken json", Chat, "", markerKey, `{"model":`},
	}
	for _, c := range cases {
		cfg := Config{Protocol: c.protocol, BaseURL: c.base, APIKey: c.key}
		if _, err := BuildRequest(cfg, json.RawMessage(c.body)); err == nil {
			t.Fatalf("%s was accepted", c.name)
		} else if strings.Contains(err.Error(), markerKey) {
			t.Fatalf("%s leaked the key: %v", c.name, err)
		}
	}
}

func TestErrorsNeverCarryTheKey(t *testing.T) {
	cfg := Config{Protocol: Chat, BaseURL: "https://user:" + markerKey + "@api.example"}
	_, err := BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[]}`))
	if err == nil {
		t.Fatal("a base URL with credentials was accepted")
	}
	if strings.Contains(err.Error(), markerKey) {
		t.Fatalf("the error leaked the key: %v", err)
	}
	cfg = Config{Protocol: Chat, APIKey: markerKey + "\nInjected: 1"}
	_, err = BuildRequest(cfg, json.RawMessage(`{"model":"m","messages":[]}`))
	if err == nil {
		t.Fatal("a key with a newline was accepted")
	}
	if strings.Contains(err.Error(), markerKey) {
		t.Fatalf("the error leaked the key: %v", err)
	}
}

func TestModelsRequestRejectsTheSameBadInput(t *testing.T) {
	cfg := Config{Protocol: Chat, BaseURL: "https://api.example/v1?x=1", APIKey: markerKey}
	if _, err := BuildModelsRequest(cfg); err == nil {
		t.Fatal("a base URL with a query was accepted")
	}
	cfg = Config{Protocol: Chat}
	if _, err := BuildModelsRequest(cfg); err == nil {
		t.Fatal("a request without a key was accepted")
	}
}
