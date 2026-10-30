package mobile

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/laincat/veneracat-core/aiapi"
	"github.com/laincat/veneracat-core/host"
)

// aiConfig is the JSON a client passes to build a standard AI request:
// {"protocol":"chat|responses|messages","baseUrl":"","apiKey":"...",
//
//	"anthropicVersion":"","partition":""}.
type aiConfig struct {
	Protocol         aiapi.Protocol `json:"protocol"`
	BaseURL          string         `json:"baseUrl"`
	APIKey           string         `json:"apiKey"`
	AnthropicVersion string         `json:"anthropicVersion"`
	Partition        string         `json:"partition"`
}

// aiRequest is the wire form of a built request. It matches what Host.HTTP
// takes, minus the id: the client decides whether to send it through Core's
// own bridge or on its own connection.
type aiRequest struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	Headers    http.Header `json:"headers"`
	BodyBase64 string      `json:"bodyBase64,omitempty"`
	Cookies    bool        `json:"cookies"`
	Partition  string      `json:"partition,omitempty"`
}

// aiResponse is the wire form of an answer.
type aiResponse struct {
	Status     int         `json:"status"`
	Headers    http.Header `json:"headers"`
	BodyBase64 string      `json:"bodyBase64"`
	FinalURL   string      `json:"finalUrl"`
}

func configFromJSON(configJSON string) (aiapi.Config, error) {
	var raw aiConfig
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return aiapi.Config{}, errors.New("mobile: invalid AI config JSON")
	}
	return aiapi.Config{
		Protocol:         raw.Protocol,
		BaseURL:          raw.BaseURL,
		APIKey:           raw.APIKey,
		AnthropicVersion: raw.AnthropicVersion,
		Partition:        raw.Partition,
	}, nil
}

func toWire(r *host.Request) aiRequest {
	out := aiRequest{Method: r.Method, URL: r.URL, Headers: r.Header, Cookies: r.Cookies, Partition: r.Partition}
	if len(r.Body) > 0 {
		out.BodyBase64 = base64.StdEncoding.EncodeToString(r.Body)
	}
	return out
}

// BuildAIRequest builds a request for one of the standard protocols without
// sending it. The client may send it itself, or use SendAIRequest.
//
// The body is the protocol's own JSON object. It is carried as written, so a
// client can build tools, images and vendor fields without Core rewriting
// them. The answer repeats the credential header: the client holds the key
// already, and Core does not keep it.
func (c *Core) BuildAIRequest(configJSON, bodyJSON string) (_ string, err error) {
	defer guard(&err)
	cfg, err := configFromJSON(configJSON)
	if err != nil {
		return "", err
	}
	request, err := aiapi.BuildRequest(cfg, json.RawMessage(bodyJSON))
	if err != nil {
		return "", err
	}
	return encode(toWire(request))
}

// BuildAIModelsRequest builds the model-list request for the same root and
// credentials. It is a GET without a body.
func (c *Core) BuildAIModelsRequest(configJSON string) (_ string, err error) {
	defer guard(&err)
	cfg, err := configFromJSON(configJSON)
	if err != nil {
		return "", err
	}
	request, err := aiapi.BuildModelsRequest(cfg)
	if err != nil {
		return "", err
	}
	return encode(toWire(request))
}

// SendAIRequest builds a standard request and performs it through the client's
// own transport. The answer carries the body as base64 so exact bytes and
// key order survive.
//
// This is the non-streaming path. A streamed reply needs a transport that can
// hand over chunks while the request is still running, which the host bridge
// does not offer yet; a caller that asks for "stream":true gets the whole
// response at once and is told so in the answer.
func (c *Core) SendAIRequest(callID int64, configJSON, bodyJSON string) (_ string, err error) {
	defer guard(&err)
	defer c.endCall(callID)
	cfg, err := configFromJSON(configJSON)
	if err != nil {
		return "", err
	}
	request, err := aiapi.BuildRequest(cfg, json.RawMessage(bodyJSON))
	if err != nil {
		return "", err
	}
	ctx, done := c.ctx(callID)
	defer done()
	response, err := c.bridge.Do(ctx, request)
	if err != nil {
		return "", err
	}
	out := aiResponse{Status: response.Status, Headers: response.Header, FinalURL: response.FinalURL}
	out.BodyBase64 = base64.StdEncoding.EncodeToString(response.Body)
	answer := struct {
		aiResponse
		Streamed bool `json:"streamed"`
	}{aiResponse: out, Streamed: false}
	encoded, err := json.Marshal(answer)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
