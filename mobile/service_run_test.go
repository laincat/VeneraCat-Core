package mobile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/service"
)

// serviceHost is the client side: it answers the JSON Core's host contract
// sends and records what it was asked for.
type serviceHost struct {
	// The rest of the Host contract is the suite's existing fake; these two
	// are the ones a service run uses.
	*fakeHost
	requests []map[string]any
	body     string
	status   int
	canceled []int64
}

func (f *serviceHost) HTTP(requestJSON string) (string, error) {
	var req map[string]any
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", err
	}
	f.requests = append(f.requests, req)
	status := f.status
	if status == 0 {
		status = 200
	}
	out, _ := json.Marshal(map[string]any{
		"status":     status,
		"bodyBase64": base64.StdEncoding.EncodeToString([]byte(f.body)),
		"finalUrl":   req["url"],
	})
	return string(out), nil
}

func (f *serviceHost) CancelHTTP(id int64) { f.canceled = append(f.canceled, id) }

func newServiceHost() *serviceHost {
	return &serviceHost{fakeHost: &fakeHost{}}
}

func packageZip(t *testing.T, manifest, index string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{service.ManifestFileName: manifest, "index.mjs": index} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func installFixture(t *testing.T, data, manifest, index string) {
	t.Helper()
	if _, err := service.InstallPackage(context.Background(), data, "fixture", "digest", packageZip(t, manifest, index)); err != nil {
		t.Fatalf("install: %v", err)
	}
}

const fetchManifest = `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":["network"]}`

// A service's fetch reaches the client's transport rather than a socket of
// Core's own, and its answer comes back as the method's result.
func TestAServiceFetchGoesThroughTheClient(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, fetchManifest, `
export function handle(method, payload) {
  return (async () => {
    // The service host's fetch answers with the body itself, not a Response.
    const body = await fetch('https://example.com/api?q=1')
    return { body, method }
  })()
}`)
	host := newServiceHost()
	host.body = `{"hello":"world"}`
	out, err := ServiceCall(host, data, "fixture", "go", `{"q":1}`, "network")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(out, `hello`) {
		t.Fatalf("result = %s", out)
	}
	if len(host.requests) != 1 {
		t.Fatalf("the client was asked %d times, want 1", len(host.requests))
	}
	if url, _ := host.requests[0]["url"].(string); url != "https://example.com/api?q=1" {
		t.Fatalf("url = %q", url)
	}
	if part, _ := host.requests[0]["partition"].(string); part != "service:fixture" {
		t.Fatalf("partition = %q", part)
	}
	// Every field the host contract lists is present, so a client that decodes
	// the documented shape cannot fail on a missing key before dialling.
	if cookies, ok := host.requests[0]["cookies"]; !ok || cookies != false {
		t.Fatalf("cookies = %v (present=%v), want false", cookies, ok)
	}
	for _, field := range []string{"id", "method", "url", "headers", "bodyBase64"} {
		if _, ok := host.requests[0][field]; !ok && field != "bodyBase64" {
			t.Fatalf("request has no %q: %v", field, host.requests[0])
		}
	}
}

// Without the network permission in force the service cannot reach the client
// at all, and the refusal says which permission is missing.
func TestAFetchWithoutTheGrantNeverReachesTheClient(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, fetchManifest, `
export function handle() {
  return (async () => await fetch('https://example.com/'))()
}`)
	host := newServiceHost()
	host.body = "reached"
	_, err := ServiceCall(host, data, "fixture", "go", `null`, "")
	if err == nil {
		t.Fatal("a fetch without the network grant was allowed")
	}
	if !strings.Contains(err.Error(), "network") {
		t.Fatalf("error = %v", err)
	}
	if len(host.requests) != 0 {
		t.Fatalf("the client was asked %d times although the grant was missing", len(host.requests))
	}
}

// Storage the service writes is still there for the next run, and it lives
// beside the package rather than in the caller's directory.
func TestServiceStorageSurvivesTheCall(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":["storage"]}`, `
export function handle(method) {
  if (method === 'write') { storageSet('k', 'v'); return { ok: true } }
  return { value: storageGet('k') }
}`)
	host := newServiceHost()
	if _, err := ServiceCall(host, data, "fixture", "write", `null`, "storage"); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := ServiceCall(host, data, "fixture", "read", `null`, "storage")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, `"v"`) {
		t.Fatalf("storage did not survive: %s", out)
	}
}

// A service that asks for secrets is refused by name, because a phone keeps
// account values where Core cannot read them.
func TestSecretsAreRefusedByName(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":["secrets"]}`, `
export function handle() { return { token: secret('token') } }`)
	_, err := ServiceCall(newServiceHost(), data, "fixture", "go", `null`, "secrets")
	if err == nil {
		t.Fatal("a service read a secret")
	}
	if !strings.Contains(err.Error(), "client owns") {
		t.Fatalf("error = %v", err)
	}
}

// An unknown permission is a caller mistake, not something to guess at.
func TestAnUnknownGrantIsRefused(t *testing.T) {
	if _, err := parsePermissions("network,telepathy"); err == nil {
		t.Fatal("an unknown permission was accepted")
	}
}

// Payload text that is not JSON never reaches the service.
func TestANonJSONPayloadIsRefused(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, fetchManifest, `export function handle() { return 1 }`)
	if _, err := ServiceCall(newServiceHost(), data, "fixture", "go", `{`, "network"); err == nil {
		t.Fatal("a broken payload was sent")
	}
}
