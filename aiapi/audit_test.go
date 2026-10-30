package aiapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditRejectsAmbiguousProtocolFields(t *testing.T) {
	for _, body := range []string{
		`{"Model":"m","messages":[]}`,
		`{"model":"m","Messages":[]}`,
		`{"model":"m","messages":[],"Stream":true}`,
		`{"model":"m","MODEL":"n","messages":[]}`,
	} {
		if _, err := BuildRequest(config(Chat), json.RawMessage(body)); err == nil {
			t.Errorf("accepted noncanonical protocol fields: %s", body)
		}
	}
}

func TestAuditRejectsInvalidVersionAndEmptyURLDelimiters(t *testing.T) {
	cfg := config(Messages)
	cfg.AnthropicVersion = "2023-06-01\r\nInjected: yes"
	if _, err := BuildModelsRequest(cfg); err == nil {
		t.Error("accepted injected version header")
	}
	for _, base := range []string{"https://example.test/v1?", "https://example.test/v1#", "https://:443/v1"} {
		cfg = config(Chat)
		cfg.BaseURL = base
		if _, err := BuildModelsRequest(cfg); err == nil {
			t.Errorf("accepted invalid API root %q", base)
		}
	}
}

func TestAuditErrorsDoNotReflectSecretsFromOtherFields(t *testing.T) {
	secret := "audit-private-marker"
	cfg := config(Chat)
	cfg.Protocol = Protocol(secret)
	if _, err := BuildModelsRequest(cfg); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("unknown protocol error reflected secret: %v", err)
	}
	body := `{"model":"m","messages":[],"` + secret + `":1,"` + secret + `":2}`
	if _, err := BuildRequest(config(Chat), json.RawMessage(body)); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("duplicate field error reflected secret: %v", err)
	}
}

func TestAuditResponsesAndMessagesKeepNativeBody(t *testing.T) {
	bodies := map[Protocol]string{
		Responses: ` {"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/img"}]}],"stream":true,"reasoning":{"effort":"high"},"n":9007199254740993} `,
		Messages:  `{"model":"m","max_tokens":9007199254740993,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/img"}}]}],"tools":[{"name":"f","input_schema":{"type":"object"}}]}`,
	}
	for protocol, body := range bodies {
		req, err := BuildRequest(config(protocol), json.RawMessage(body))
		if err != nil {
			t.Fatal(err)
		}
		if string(req.Body) != body {
			t.Fatal("body changed")
		}
	}
}
