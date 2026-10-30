package mobile

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const aiKey = "sk-test-key-not-a-real-secret"

func aiHost(answer string) *fakeHost {
	h := &fakeHost{routes: map[string]string{}, data: map[string]string{}}
	h.routes["https://api.openai.com/v1/chat/completions"] = answer
	h.routes["https://api.anthropic.com/v1/messages"] = answer
	return h
}

func TestBuildAIRequestGivesTheClientAUsableRequest(t *testing.T) {
	m := &Core{}
	config := `{"protocol":"chat","apiKey":"` + aiKey + `"}`
	out, err := m.BuildAIRequest(config, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var wire aiRequest
	if err := json.Unmarshal([]byte(out), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Method != "POST" || wire.URL != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("request = %+v", wire)
	}
	if wire.Cookies {
		t.Fatal("the request enabled cookies")
	}
	if got := wire.Headers.Get("Authorization"); got != "Bearer "+aiKey {
		t.Fatalf("Authorization = %q", got)
	}
	body, err := base64.StdEncoding.DecodeString(wire.BodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("body = %s", body)
	}
}

func TestBuildAIRequestRefusesBadInput(t *testing.T) {
	m := &Core{}
	cases := []struct{ config, body string }{
		{`{"protocol":"chat"}`, `{"model":"m","messages":[]}`},
		{`{"protocol":"gemini","apiKey":"k"}`, `{"model":"m","messages":[]}`},
		{`{"protocol":"chat","apiKey":"k"}`, `{"messages":[]}`},
		{`not json`, `{"model":"m","messages":[]}`},
	}
	for _, c := range cases {
		_, err := m.BuildAIRequest(c.config, c.body)
		if err == nil {
			t.Fatalf("config %s body %s was accepted", c.config, c.body)
		}
		if strings.Contains(err.Error(), aiKey) {
			t.Fatalf("the error leaked the key: %v", err)
		}
	}
}

func TestBuildAIModelsRequestIsAGet(t *testing.T) {
	m := &Core{}
	out, err := m.BuildAIModelsRequest(`{"protocol":"messages","apiKey":"` + aiKey + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wire aiRequest
	if err := json.Unmarshal([]byte(out), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Method != "GET" || wire.BodyBase64 != "" {
		t.Fatalf("request = %+v", wire)
	}
	if !strings.HasSuffix(wire.URL, "/models") {
		t.Fatalf("url = %s", wire.URL)
	}
	if wire.Headers.Get("x-api-key") != aiKey {
		t.Fatalf("x-api-key = %q", wire.Headers.Get("x-api-key"))
	}
}

func TestSendAIRequestRunsThroughTheHostAndKeepsExactBytes(t *testing.T) {
	answer := `{"id":"x","usage":{"total_tokens":9007199254740993}}`
	h := aiHost(answer)
	c, err := New(`{"locale":"zh"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out, err := c.SendAIRequest(0, `{"protocol":"chat","apiKey":"`+aiKey+`"}`, `{"model":"m","messages":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		aiResponse
		Streamed bool `json:"streamed"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Status != http.StatusOK {
		t.Fatalf("status = %d", reply.Status)
	}
	body, err := base64.StdEncoding.DecodeString(reply.BodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != answer {
		t.Fatalf("body = %s", body)
	}
	if reply.Streamed {
		t.Fatal("a non-streaming call claimed to be streamed")
	}
}

func TestSendAIRequestSaysWhenStreamingIsNotAvailable(t *testing.T) {
	h := aiHost(`{"ok":true}`)
	c, err := New(`{"locale":"zh"}`, h)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out, err := c.SendAIRequest(0, `{"protocol":"chat","apiKey":"`+aiKey+`"}`, `{"model":"m","messages":[],"stream":true}`)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		aiResponse
		Streamed bool `json:"streamed"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Streamed {
		t.Fatal("the answer claims a streamed reply that never happened")
	}
}
