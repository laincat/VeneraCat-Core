package mobile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laincat/veneracat-core/host"
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
	headers  http.Header
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
	resp := map[string]any{
		"status":     status,
		"bodyBase64": base64.StdEncoding.EncodeToString([]byte(f.body)),
		"finalUrl":   req["url"],
	}
	if len(f.headers) > 0 {
		resp["headers"] = f.headers
	}
	out, _ := json.Marshal(resp)
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
	if host.requests[0]["method"] != "GET" || host.requests[0]["id"] == nil {
		t.Fatalf("request = %v", host.requests[0])
	}
}

type blockedServiceHTTP struct {
	*fakeHost
	started  chan wireRequest
	canceled chan int64
	exited   chan int64
	release  chan struct{}
	active   sync.Map
}

func newBlockedServiceHTTP(t *testing.T) *blockedServiceHTTP {
	t.Helper()
	h := &blockedServiceHTTP{fakeHost: &fakeHost{}, started: make(chan wireRequest, 4), canceled: make(chan int64, 4), exited: make(chan int64, 4), release: make(chan struct{})}
	t.Cleanup(func() { close(h.release) })
	return h
}

func (h *blockedServiceHTTP) HTTP(raw string) (string, error) {
	var req wireRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", err
	}
	done := make(chan struct{})
	h.active.Store(req.ID, done)
	h.started <- req
	select {
	case <-h.release:
	case <-done:
	}
	h.active.Delete(req.ID)
	h.exited <- req.ID
	return `{"status":200}`, nil
}

func (h *blockedServiceHTTP) CancelHTTP(id int64) {
	if done, ok := h.active.LoadAndDelete(id); ok {
		close(done.(chan struct{}))
	}
	h.canceled <- id
}

func receiveServiceTest[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for service test")
		var zero T
		return zero
	}
}

func TestServiceAndHostBridgeShareRequestIDs(t *testing.T) {
	h := newBlockedServiceHTTP(t)
	serviceCtx, cancelService := context.WithCancel(context.Background())
	defer cancelService()
	otherCtx, cancelOther := context.WithCancel(context.Background())
	defer cancelOther()
	serviceDone := make(chan error, 1)
	go func() {
		_, err := fetchThroughClient(serviceCtx, h, "fixture", "https://example.com/service")
		serviceDone <- err
	}()
	serviceReq := receiveServiceTest(t, h.started)
	otherDone := make(chan error, 1)
	go func() {
		bridge := hostBridge{h: h}
		_, err := bridge.Do(otherCtx, &host.Request{Method: "GET", URL: "https://example.com/comic"})
		otherDone <- err
	}()
	otherReq := receiveServiceTest(t, h.started)
	if serviceReq.ID == otherReq.ID {
		t.Fatalf("both requests used ID %d", serviceReq.ID)
	}
	cancelService()
	if id := receiveServiceTest(t, h.canceled); id != serviceReq.ID {
		t.Fatalf("canceled request %d, want %d", id, serviceReq.ID)
	}
	if err := receiveServiceTest(t, serviceDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("service error = %v", err)
	}
	select {
	case err := <-otherDone:
		t.Fatalf("other request finished: %v", err)
	default:
	}
	cancelOther()
	if id := receiveServiceTest(t, h.canceled); id != otherReq.ID {
		t.Fatalf("canceled request %d, want %d", id, otherReq.ID)
	}
	if err := receiveServiceTest(t, otherDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("other error = %v", err)
	}
}

type serviceHTTPError struct {
	*fakeHost
	err error
}

func (h serviceHTTPError) HTTP(string) (string, error) { return "", h.err }

func TestServiceHTTPPrecancelTimeoutAndStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const endpoint = "https://example.com/"
	if _, err := fetchThroughClient(ctx, nil, "fixture", endpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}
	timeout := &url.Error{Op: "Get", URL: endpoint, Err: context.DeadlineExceeded}
	if _, err := fetchThroughClient(context.Background(), serviceHTTPError{err: timeout}, "fixture", endpoint); !errors.Is(err, timeout) {
		t.Fatalf("host timeout = %v", err)
	}
	h := newServiceHost()
	h.status = 403
	if _, err := fetchThroughClient(context.Background(), h, "fixture", endpoint); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("HTTP status error = %v", err)
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

// Account values reach a service only when the client hands them over for that
// call, and a value it did not supply is refused by name.
func TestAccountValuesComeFromTheCall(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":["secrets"]}`, `
export function handle() { return { token: secret('token') } }`)
	out, err := ServiceCallWithSecrets(newServiceHost(), data, "fixture", "go",
		`null`, "secrets", `{"token":"s3cret"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(out, "s3cret") {
		t.Fatalf("result = %s", out)
	}

	_, err = ServiceCallWithSecrets(newServiceHost(), data, "fixture", "go",
		`null`, "secrets", `{"other":"x"}`)
	if err == nil || !strings.Contains(err.Error(), `"token"`) {
		t.Fatalf("a missing value must be named: %v", err)
	}
}

// Without the secrets permission the values are not reachable at all, even when
// the client supplied them.
func TestAccountValuesNeedThePermission(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":["secrets"]}`, `
export function handle() { return { token: secret('token') } }`)
	_, err := ServiceCallWithSecrets(newServiceHost(), data, "fixture", "go",
		`null`, "", `{"token":"s3cret"}`)
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("error = %v", err)
	}
}

// Malformed account values never reach a service.
func TestBrokenAccountValuesAreRefused(t *testing.T) {
	if _, err := parseSecrets(`{"token":`); err == nil {
		t.Fatal("broken account values were accepted")
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

// Describing a service names what a screen can offer, and a service without a
// describe export answers with an empty list rather than an error.
func TestDescribeListsWhatAServiceOffers(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":[]}`, `
export function describe() { return { methods: [{ name: 'sync', title: '同步' }] } }
export function handle() { return { ok: true } }`)
	out, err := ServiceDescribe(&serviceHost{fakeHost: &fakeHost{}}, data, "fixture", "")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !strings.Contains(out, `"sync"`) || !strings.Contains(out, `同步`) {
		t.Fatalf("methods = %s", out)
	}
}

func TestDescribeWithoutTheExportIsAnEmptyList(t *testing.T) {
	data := t.TempDir()
	installFixture(t, data, `{"id":"fixture","name":"Fixture","version":"1.0.0","kind":"service.js","entry":"index.mjs","permissions":[]}`, `
export function handle() { return { ok: true } }`)
	out, err := ServiceDescribe(&serviceHost{fakeHost: &fakeHost{}}, data, "fixture", "")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if out != "[]" {
		t.Fatalf("methods = %s", out)
	}
}

func openServiceRunner(t *testing.T, client Host, dir string) *ServiceRunner {
	t.Helper()
	runner, err := NewServiceRunner(client, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	return runner
}

func TestServiceRunnerOwnsItsHostAndReusesEquivalentGrants(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, fetchManifest, `let count = 0; export function handle() { return fetch('https://example.com/') + ++count; }`)
	first, second := newServiceHost(), newServiceHost()
	first.body, second.body = "first", "second"
	left, right := openServiceRunner(t, first, dir), openServiceRunner(t, second, dir)
	for _, tc := range []struct {
		runner *ServiceRunner
		grants string
		want   string
	}{
		{left, "network,storage", `"first1"`},
		{right, "network,storage", `"second1"`},
		{left, " storage, network, network ", `"first2"`},
	} {
		if got, err := tc.runner.Call(0, "fixture", "go", "", tc.grants, ""); err != nil || got != tc.want {
			t.Fatalf("call = %q, %v, want %s", got, err, tc.want)
		}
	}
}

func TestServiceRunnerReplacesAndRemovesActiveRuntime(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, fetchManifest, `export function handle() { return fetch('https://example.com/'); }`)
	h := newBlockedServiceHTTP(t)
	runner := openServiceRunner(t, h, dir)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Call(0, "fixture", "go", "", "network", "")
		done <- err
	}()
	request := receiveServiceTest(t, h.started)
	archive := packageZip(t, strings.Replace(fetchManifest, "1.0.0", "2.0.0", 1), `export function handle() { return 'new'; }`)
	if _, err := ServiceInstall(dir, "fixture", "next", base64.StdEncoding.EncodeToString(archive)); err != nil {
		t.Fatal(err)
	}
	if err := receiveServiceTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("replaced call = %v", err)
	}
	if id := receiveServiceTest(t, h.canceled); id != request.ID {
		t.Fatalf("canceled ID = %d, want %d", id, request.ID)
	}
	if got, err := runner.Call(0, "fixture", "go", "", "network", ""); err != nil || got != `"new"` {
		t.Fatalf("first call after replace = %s, %v", got, err)
	}
	if err := ServiceRemove(dir, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Call(0, "fixture", "go", "", "network", ""); err == nil {
		t.Fatal("removed service could still run")
	}
}

func TestServiceRunnerCancellationAndClose(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, fetchManifest, `export function handle() { return fetch('https://example.com/'); }`)
	h := newBlockedServiceHTTP(t)
	runner := openServiceRunner(t, h, dir)
	preCanceled := runner.NewCall()
	runner.Cancel(preCanceled)
	if _, err := runner.Call(preCanceled, "fixture", "go", "", "network", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled call = %v", err)
	}
	for _, stop := range []func(int64){runner.Cancel, func(int64) { runner.Close() }} {
		callID := runner.NewCall()
		done := make(chan error, 1)
		go func() {
			_, err := runner.Call(callID, "fixture", "go", "", "network", "")
			done <- err
		}()
		request := receiveServiceTest(t, h.started)
		stop(callID)
		if err := receiveServiceTest(t, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled call = %v", err)
		}
		if canceled, exited := receiveServiceTest(t, h.canceled), receiveServiceTest(t, h.exited); canceled != request.ID || exited != request.ID {
			t.Fatalf("request %d canceled = %d, exited = %d", request.ID, canceled, exited)
		}
	}
	if _, err := runner.Call(0, "fixture", "go", "", "network", ""); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed runner = %v", err)
	}
	select {
	case req := <-h.started:
		t.Fatalf("closed runner sent request %d", req.ID)
	default:
	}
}

func TestServiceRunnerRotatesSecretsAndReleasesOldState(t *testing.T) {
	dir := t.TempDir()
	manifest := strings.Replace(fetchManifest, "network", "secrets", 1)
	installFixture(t, dir, manifest, `let cached; export function handle() { return cached ??= secret('token'); }`)
	runner := openServiceRunner(t, newServiceHost(), dir)
	for _, token := range []string{"first", "second"} {
		secrets, _ := json.Marshal(map[string]string{"token": token})
		want, _ := json.Marshal(token)
		if got, err := runner.Call(0, "fixture", "go", "", "secrets", string(secrets)); err != nil || got != string(want) {
			t.Fatalf("call = %s, %v, want %s", got, err, want)
		}
	}
	if _, err := runner.Call(0, "fixture", "go", "", "secrets", ""); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("call without previous secrets = %v", err)
	}
}

func TestServiceFetchInitForwardsMethodHeadersBodyAndStatus(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, fetchManifest, `
export function handle(method, payload) {
  const input = payload ? JSON.parse(payload) : {};
  if (method === 'single') {
    return { body: fetch('https://api.example.test/single') };
  }
  if (method === 'cookie') {
    return fetch('https://api.example.test/cookie', {
      method: 'GET',
      headers: { Cookie: 'sid=forbidden' },
    });
  }
  return fetch('https://api.example.test/' + method, {
    method: input.httpMethod,
    headers: {
      Authorization: 'Bearer tok-xyz',
      'User-Agent': 'VeneraCat/0.1.0',
      'trakt-api-key': 'cid-xyz',
      'Content-Type': 'application/json',
    },
    body: input.body,
  });
}
`)

	sh := newServiceHost()
	runner := openServiceRunner(t, sh, dir)

	cases := []struct {
		name       string
		httpMethod string
		reqBody    string
		status     int
		respBody   string
		wantOK     bool
	}{
		{"post", http.MethodPost, `{"progress":80}`, http.StatusCreated, `{"id":9}`, true},
		{"patch", http.MethodPatch, `{"episode_id":[1,2],"type":2}`, http.StatusUnauthorized, `{"title":"Unauthorized"}`, false},
		{"put", http.MethodPut, `{"type":2}`, http.StatusForbidden, `{"title":"Forbidden"}`, false},
		{"delete", http.MethodDelete, ``, http.StatusNotFound, `{"title":"Not Found"}`, false},
	}

	for i, tc := range cases {
		sh.status = tc.status
		sh.body = tc.respBody
		sh.headers = http.Header{"X-Service-Trace": []string{"trace-" + tc.name}}
		payload, _ := json.Marshal(map[string]string{"httpMethod": tc.httpMethod, "body": tc.reqBody})
		raw, err := runner.Call(0, "fixture", tc.name, string(payload), "network", "")
		if err != nil {
			t.Fatalf("%s: call failed: %v", tc.name, err)
		}
		var res struct {
			Status  int               `json:"status"`
			OK      bool              `json:"ok"`
			Body    string            `json:"body"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", tc.name, raw, err)
		}
		if res.Status != tc.status || res.OK != tc.wantOK || res.Body != tc.respBody || res.Headers["x-service-trace"] != "trace-"+tc.name {
			t.Fatalf("%s: response = %+v", tc.name, res)
		}
		req := sh.requests[i]
		if req["method"] != tc.httpMethod {
			t.Fatalf("%s: wire method = %v, want %s", tc.name, req["method"], tc.httpMethod)
		}
		if cookies, ok := req["cookies"]; !ok || cookies != false {
			t.Fatalf("%s: wire cookies = %v (present=%v), want false", tc.name, cookies, ok)
		}
		if req["partition"] != "service:fixture" {
			t.Fatalf("%s: wire partition = %v", tc.name, req["partition"])
		}
		hdrs, _ := req["headers"].(map[string]any)
		for k, want := range map[string]string{
			"Authorization": "Bearer tok-xyz",
			"User-Agent":    "VeneraCat/0.1.0",
			"Trakt-Api-Key": "cid-xyz",
			"Content-Type":  "application/json",
		} {
			vals, _ := hdrs[k].([]any)
			if len(vals) != 1 || vals[0] != want {
				t.Fatalf("%s: header %s = %v, want %q (all=%v)", tc.name, k, vals, want, hdrs)
			}
		}
		if tc.reqBody != "" {
			rawB64, _ := req["bodyBase64"].(string)
			decoded, err := base64.StdEncoding.DecodeString(rawB64)
			if err != nil || string(decoded) != tc.reqBody {
				t.Fatalf("%s: decoded body = %q (%v), want %q", tc.name, decoded, err, tc.reqBody)
			}
		} else if _, exists := req["bodyBase64"]; exists {
			t.Fatalf("%s: unexpected bodyBase64 for empty body: %v", tc.name, req["bodyBase64"])
		}
	}

	// Cookie header must be refused without invoking Host.HTTP.
	before := len(sh.requests)
	if _, err := runner.Call(0, "fixture", "cookie", "", "network", ""); err == nil || !strings.Contains(err.Error(), "Cookie") {
		t.Fatalf("cookie call = %v, want Cookie refusal", err)
	}
	if len(sh.requests) != before {
		t.Fatalf("cookie refusal still dialed Host.HTTP")
	}

	// Single-arg fetch(url) still rejects non-2xx responses.
	sh.status = http.StatusUnauthorized
	sh.body = "nope"
	if _, err := runner.Call(0, "fixture", "single", "", "network", ""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("single-arg 401 = %v, want HTTP 401 error", err)
	}
}

func TestServiceFetchInitCancellationPropagatesToClient(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, fetchManifest, `
export function handle() {
  return fetch('https://api.example.test/scrobble', {
    method: 'POST',
    headers: { Authorization: 'Bearer tok' },
    body: '{"progress":10}',
  });
}
`)
	h := newBlockedServiceHTTP(t)
	runner := openServiceRunner(t, h, dir)
	callID := runner.NewCall()
	done := make(chan error, 1)
	go func() {
		_, err := runner.Call(callID, "fixture", "go", "", "network", "")
		done <- err
	}()
	req := receiveServiceTest(t, h.started)
	if req.Method != http.MethodPost || req.Cookies || req.Headers.Get("Authorization") != "Bearer tok" {
		t.Fatalf("blocked wire request = %+v", req)
	}
	runner.Cancel(callID)
	if err := receiveServiceTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled POST call = %v", err)
	}
	if canceled := receiveServiceTest(t, h.canceled); canceled != req.ID {
		t.Fatalf("canceled ID = %d, want %d", canceled, req.ID)
	}
}
