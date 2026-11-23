package mobile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/service"
)

type serviceConfiguration struct {
	permissions string
	secrets     [32]byte
	installed   time.Time
	version     string
	digest      string
}

type serviceEntry struct {
	config     serviceConfiguration
	supervisor *service.Supervisor
	ctx        context.Context
	cancel     context.CancelFunc
}

type serviceSecretKey struct{}

// ServiceRunner owns the resident services and active calls of one host session.
type ServiceRunner struct {
	client  Host
	dir     string
	calls   Core
	mu      sync.Mutex
	closed  bool
	groups  map[string]*service.Supervisor
	entries map[string]serviceEntry
}

var serviceSessions = struct {
	sync.Mutex
	byDir map[string]map[*ServiceRunner]struct{}
}{byDir: make(map[string]map[*ServiceRunner]struct{})}

func serviceProfile(dataDir string) (string, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved, nil
	}
	return dir, nil
}

// NewServiceRunner binds a runner to one profile and HTTP host. Close releases it.
func NewServiceRunner(client Host, dataDir string) (*ServiceRunner, error) {
	if client == nil {
		return nil, fmt.Errorf("mobile: service runner needs a host for its network")
	}
	dir, err := serviceProfile(dataDir)
	if err != nil {
		return nil, err
	}
	runner := &ServiceRunner{client: client, dir: dir, groups: make(map[string]*service.Supervisor), entries: make(map[string]serviceEntry)}
	serviceSessions.Lock()
	defer serviceSessions.Unlock()
	if serviceSessions.byDir[dir] == nil {
		serviceSessions.byDir[dir] = make(map[*ServiceRunner]struct{})
	}
	serviceSessions.byDir[dir][runner] = struct{}{}
	return runner, nil
}

// NewCall reserves an ID for a call or description that can be canceled later.
func (r *ServiceRunner) NewCall() int64 { return r.calls.NewCall() }

// Cancel stops work associated with a reserved call ID.
func (r *ServiceRunner) Cancel(callID int64) { r.calls.Cancel(callID) }

// Close cancels active calls and releases all resident runtimes.
func (r *ServiceRunner) Close() {
	r.calls.mu.Lock()
	r.calls.closed = true
	if r.calls.cancelLifetime != nil {
		r.calls.cancelLifetime()
	}
	clear(r.calls.cancels)
	clear(r.calls.reserved)
	r.calls.mu.Unlock()

	r.mu.Lock()
	r.closed = true
	for id := range r.entries {
		r.invalidate(id)
	}
	for _, sup := range r.groups {
		sup.Close()
	}
	clear(r.groups)
	r.client = nil
	r.mu.Unlock()

	serviceSessions.Lock()
	defer serviceSessions.Unlock()
	delete(serviceSessions.byDir[r.dir], r)
	if len(serviceSessions.byDir[r.dir]) == 0 {
		delete(serviceSessions.byDir, r.dir)
	}
}

func (r *ServiceRunner) invalidate(id string) {
	if entry, ok := r.entries[id]; ok {
		delete(r.entries, id)
		entry.cancel()
		entry.supervisor.Invalidate(id)
	}
}

func serviceRecordConfig(record *service.Installed) serviceConfiguration {
	return serviceConfiguration{installed: record.Installed, version: record.Version, digest: record.SHA256}
}

func serviceRunnersFor(dataDir string) []*ServiceRunner {
	dir, err := serviceProfile(dataDir)
	if err != nil {
		return nil
	}
	serviceSessions.Lock()
	defer serviceSessions.Unlock()
	var runners []*ServiceRunner
	for runner := range serviceSessions.byDir[dir] {
		runners = append(runners, runner)
	}
	return runners
}

func refreshServiceRunners(dataDir, id string) {
	for _, runner := range serviceRunnersFor(dataDir) {
		runner.mu.Lock()
		if entry, ok := runner.entries[id]; ok {
			record, err := service.InstalledPackage(runner.dir, id)
			if err != nil || entry.config.installed != record.Installed || entry.config.digest != record.SHA256 || entry.config.version != record.Version {
				runner.invalidate(id)
			}
		}
		runner.mu.Unlock()
	}
}

func (r *ServiceRunner) acquire(ctx context.Context, id, granted string, secrets map[string]string) (*service.Supervisor, context.Context, func(), error) {
	allowed, err := parsePermissions(granted)
	if err != nil {
		return nil, nil, nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, nil, nil, fmt.Errorf("mobile: ServiceCall needs a service id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, nil, fmt.Errorf("mobile: the service runner is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	record, err := service.InstalledPackage(r.dir, id)
	if err != nil {
		r.invalidate(id)
		return nil, nil, nil, err
	}
	config := serviceRecordConfig(record)
	for _, permission := range allowed {
		config.permissions += string(permission) + ","
	}
	if len(secrets) > 0 {
		canonical, _ := json.Marshal(secrets)
		config.secrets = sha256.Sum256(canonical)
	}
	entry, ok := r.entries[id]
	if !ok || entry.config != config {
		r.invalidate(id)
		sup := r.groups[config.permissions]
		if sup == nil {
			client := r.client
			sup = service.NewSupervisorFor(func(id string) service.Host {
				return serviceHostFor(client, r.dir, id)
			}, allowed, service.SupervisorOptions{}, func(_ context.Context, id string) (*service.Manifest, string, error) {
				return service.ReadInstalledPackage(r.dir, id)
			})
			r.groups[config.permissions] = sup
		}
		lifetime, cancel := context.WithCancel(context.Background())
		entry = serviceEntry{config: config, supervisor: sup, ctx: lifetime, cancel: cancel}
		r.entries[id] = entry
	}
	call, cancel := context.WithCancel(context.WithValue(ctx, serviceSecretKey{}, secrets))
	stop := context.AfterFunc(entry.ctx, cancel)
	return entry.supervisor, call, func() { stop(); cancel() }, nil
}

func serviceHostFor(client Host, dir, id string) service.Host {
	storage, _ := service.OpenStorage(dir, id)
	return service.Host{
		Fetch: func(ctx context.Context, raw string) (string, error) {
			return fetchThroughClient(ctx, client, id, raw)
		},
		StorageGet: storage.Get,
		StorageSet: storage.Set,
		Secret: func(ctx context.Context, key string) (string, error) {
			secrets, _ := ctx.Value(serviceSecretKey{}).(map[string]string)
			if value, ok := secrets[key]; ok {
				return value, nil
			}
			return "", fmt.Errorf("service: %q asked for the account value %q, which this call did not supply", id, key)
		},
	}
}

// Call runs an installed service method with the supplied grants and account values.
func (r *ServiceRunner) Call(callID int64, id, method, payload, granted, secretsJSON string) (_ string, err error) {
	defer guard(&err)
	defer r.calls.endCall(callID)
	ctx, cancel := r.calls.ctx(callID)
	defer cancel()
	secrets, err := parseSecrets(secretsJSON)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(payload) == "" {
		payload = "null"
	}
	if !json.Valid([]byte(payload)) {
		return "", fmt.Errorf("mobile: payload is not JSON")
	}
	sup, call, release, err := r.acquire(ctx, id, granted, secrets)
	if err != nil {
		return "", err
	}
	defer release()
	sup.Sweep()
	out, err := sup.Call(call, id, method, json.RawMessage(payload))
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "null", nil
	}
	return string(out), nil
}

// Describe returns the methods advertised by an installed service as JSON.
func (r *ServiceRunner) Describe(callID int64, id, granted string) (_ string, err error) {
	defer guard(&err)
	defer r.calls.endCall(callID)
	ctx, cancel := r.calls.ctx(callID)
	defer cancel()
	sup, call, release, err := r.acquire(ctx, id, granted, nil)
	if err != nil {
		return "", err
	}
	defer release()
	sup.Sweep()
	methods, err := sup.Describe(call, id)
	if err != nil {
		return "", err
	}
	if methods == nil {
		return "[]", nil
	}
	return encode(methods)
}

// ServiceCall runs one method through the compatibility entry point.
func ServiceCall(client Host, dataDir, id, method, payload, granted string) (string, error) {
	return ServiceCallWithSecrets(client, dataDir, id, method, payload, granted, "")
}

// ServiceCallWithSecrets opens a short-lived runner for an existing caller.
func ServiceCallWithSecrets(client Host, dataDir, id, method, payload, granted, secretsJSON string) (string, error) {
	runner, err := NewServiceRunner(client, dataDir)
	if err != nil {
		return "", err
	}
	defer runner.Close()
	return runner.Call(0, id, method, payload, granted, secretsJSON)
}

// ServiceDescribe describes a service through the compatibility entry point.
func ServiceDescribe(client Host, dataDir, id, granted string) (string, error) {
	runner, err := NewServiceRunner(client, dataDir)
	if err != nil {
		return "", err
	}
	defer runner.Close()
	return runner.Describe(0, id, granted)
}

// ServiceRunForget invalidates the resident runtimes for a profile.
func ServiceRunForget(dataDir string) {
	for _, runner := range serviceRunnersFor(dataDir) {
		runner.mu.Lock()
		for id := range runner.entries {
			runner.invalidate(id)
		}
		runner.mu.Unlock()
	}
}

func parseSecrets(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("mobile: the account values are not a JSON object of strings: %v", err)
	}
	return out, nil
}

func parsePermissions(granted string) ([]service.Permission, error) {
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
	slices.Sort(out)
	return slices.Compact(out), nil
}

// fetchThroughClient asks the client's Host for one URL and returns the body.
//
// It carries the request id so the client can be told to drop the request when
// Core stops waiting for it, which is what makes closing a screen stop the
// service's traffic rather than leaving it to its own timeout.
func fetchThroughClient(ctx context.Context, client Host, id, rawURL string) (string, error) {
	bridge := hostBridge{h: client}
	response, err := bridge.Do(ctx, &host.Request{
		Method: "GET", URL: rawURL, Partition: "service:" + id,
	})
	if err != nil {
		return "", err
	}
	if response.Status < 200 || response.Status > 299 {
		return "", fmt.Errorf("service: %s answered HTTP %d", rawURL, response.Status)
	}
	return string(response.Body), nil
}
