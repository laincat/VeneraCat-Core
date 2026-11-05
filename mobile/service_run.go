package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/laincat/veneracat-core/service"
)

// Running an installed service on a client that owns the network.
//
// Core does not ship a transport of its own to a phone: the client already has
// one, with its cookies, its proxy and its certificate decisions. So the entry
// here takes that client's Host and builds the service's host from it: the HTTP
// a service makes goes out through `Host.HTTP`, storage lives under the package
// directory, and account values are refused because a service's secrets belong
// to the client that stores them.
//
// One Supervisor is kept per data directory, so a service that is called twice
// is loaded once and then cleaned up by the same idle rule the desktop uses.

type serviceRunner struct {
	mu  sync.Mutex
	sup map[string]*service.Supervisor
}

var serviceRunners = &serviceRunner{sup: map[string]*service.Supervisor{}}

func (r *serviceRunner) supervisor(client Host, dataDir, granted string, dir string) (*service.Supervisor, error) {
	allowed, err := parsePermissions(granted)
	if err != nil {
		return nil, err
	}
	key := dataDir + "\x00" + granted
	r.mu.Lock()
	defer r.mu.Unlock()
	if sup, ok := r.sup[key]; ok {
		return sup, nil
	}
	hostFor := func(id string) service.Host {
		return service.Host{
			Fetch: func(ctx context.Context, raw string) (string, error) {
				return fetchThroughClient(ctx, client, id, raw)
			},
			StorageGet: func(_ context.Context, k string) (string, bool, error) {
				store, err := readServiceStorage(dir, id)
				if err != nil {
					return "", false, err
				}
				v, ok := store[k]
				return v, ok, nil
			},
			StorageSet: func(_ context.Context, k, v string) error {
				store, err := readServiceStorage(dir, id)
				if err != nil {
					return err
				}
				store[k] = v
				return writeServiceStorage(dir, id, store)
			},
			// A phone's account values live in its own keychain; Core has no
			// way to read them and says so rather than pretending they are
			// empty.
			Secret: func(_ context.Context, k string) (string, error) {
				return "", fmt.Errorf("service: %q reads secrets, which the client owns; %q is not available to Core", id, k)
			},
			Log: func(string, string) {},
		}
	}
	open := func(ctx context.Context, id string) (*service.Manifest, string, error) {
		return service.ReadInstalledPackage(dataDir, id)
	}
	r.sup[key] = service.NewSupervisorFor(hostFor, allowed, service.SupervisorOptions{}, open)
	return r.sup[key], nil
}

// ServiceCall runs one method of an installed service.
//
// client does the HTTP; granted lists the permissions the host allows, comma
// separated, as `ServiceCall`'s caller decided from what the user approved. The
// permission in force is that intersection: a service that asked for less does
// not get more, and one that asked for more does not get what was withheld.
//
// payload is the request's JSON text; the result is the service's JSON text.
func ServiceCall(client Host, dataDir, id, method, payload, granted string) (_ string, err error) {
	defer guard(&err)
	if client == nil {
		return "", fmt.Errorf("mobile: ServiceCall needs a host for its network")
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("mobile: ServiceCall needs a service id")
	}
	sup, err := serviceRunners.supervisor(client, dataDir, granted, filepath.Join(dataDir, "services"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(payload) == "" {
		payload = "null"
	}
	if !json.Valid([]byte(payload)) {
		return "", fmt.Errorf("mobile: payload is not JSON")
	}
	out, err := sup.Call(context.Background(), id, method, json.RawMessage(payload))
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "null", nil
	}
	return string(out), nil
}

// ServiceDescribe returns what an installed service offers to press, as JSON
// like [{"name":"sync","title":"同步"}].
//
// A service without a describe export answers with an empty list: it is used by
// other code rather than by a button, which is not an error. The same host is
// used as a call would, because describing compiles the package too.
func ServiceDescribe(client Host, dataDir, id, granted string) (_ string, err error) {
	defer guard(&err)
	if client == nil {
		return "", fmt.Errorf("mobile: ServiceDescribe needs a host for its network")
	}
	sup, err := serviceRunners.supervisor(client, dataDir, granted, filepath.Join(dataDir, "services"))
	if err != nil {
		return "", err
	}
	methods, err := sup.Describe(context.Background(), id)
	if err != nil {
		return "", err
	}
	if methods == nil {
		return "[]", nil
	}
	return encode(methods)
}

// ServiceRunForget drops the supervisors for one directory: a client calls it
// when it removes a package or leaves an environment, so the next call loads
// the code from disk again.
func ServiceRunForget(dataDir string) {
	r := serviceRunners
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.sup {
		if strings.HasPrefix(key, dataDir+"\x00") {
			delete(r.sup, key)
		}
	}
}

func parsePermissions(granted string) ([]service.Permission, error) {
	if strings.TrimSpace(granted) == "" {
		return nil, nil
	}
	var out []service.Permission
	for _, part := range strings.Split(granted, ",") {
		name := service.Permission(strings.TrimSpace(part))
		switch name {
		case service.PermissionNetwork, service.PermissionStorage, service.PermissionSecrets:
			out = append(out, name)
		case "":
		default:
			return nil, fmt.Errorf("mobile: unknown permission %q", name)
		}
	}
	return out, nil
}

// fetchThroughClient asks the client's Host for one URL and returns the body.
//
// It carries the request id so the client can be told to drop the request when
// Core stops waiting for it, which is what makes closing a screen stop the
// service's traffic rather than leaving it to its own timeout.
func fetchThroughClient(ctx context.Context, client Host, id, rawURL string) (string, error) {
	n := serviceRequestSeq.Add(1)
	req := map[string]any{
		"id":      n,
		"method":  "GET",
		"url":     rawURL,
		"headers": map[string][]string{},
		// The host contract lists cookies, so the field is sent even when this
		// request carries none: a client that decodes the documented shape must
		// not fail on a missing key before it even dials.
		"cookies":   false,
		"partition": "service:" + id,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			client.CancelHTTP(n)
		case <-done:
		}
	}()
	out, err := client.HTTP(string(body))
	if err != nil {
		return "", err
	}
	var res struct {
		Status     int    `json:"status"`
		BodyBase64 string `json:"bodyBase64"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return "", fmt.Errorf("mobile: the host's answer is not a response: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(res.BodyBase64)
	if err != nil {
		return "", fmt.Errorf("mobile: the host's body is not base64: %v", err)
	}
	if res.Status < 200 || res.Status > 299 {
		return "", fmt.Errorf("service: %s answered HTTP %d", rawURL, res.Status)
	}
	return string(decoded), nil
}

var serviceRequestSeq atomic.Int64

func serviceStoragePath(dir, id string) string {
	return filepath.Join(dir, id, "storage.json")
}

func readServiceStorage(dir, id string) (map[string]string, error) {
	raw, err := os.ReadFile(serviceStoragePath(dir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := map[string]string{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("service: storage for %q is unreadable: %v", id, err)
	}
	return out, nil
}

func writeServiceStorage(dir, id string, store map[string]string) error {
	path := serviceStoragePath(dir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
