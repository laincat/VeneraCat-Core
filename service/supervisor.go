package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultCallTimeout bounds one call into a service. A plugin runs arbitrary
// JavaScript, and a loop that never ends would otherwise hold the host's
// goroutine for as long as the process lives.
const DefaultCallTimeout = 15 * time.Second

// ErrTimeout is what a call returns when it runs past its deadline.
var ErrTimeout = errors.New("service: call timed out")

// Supervisor runs several services, loading each one on first use and letting
// an idle one go.
//
// It exists because a service is a plugin: the host decides when it starts,
// how long one call may take, how many may be resident, and when it stops.
// A runtime that has been idle is not special — the next call loads it again
// from the same bytes.
type Supervisor struct {
	hostFor func(id string) Host
	granted []Permission
	open    func(ctx context.Context, id string) (*Manifest, string, error)

	// max bounds how many services stay resident at once.
	max      int
	idle     time.Duration
	timeout  time.Duration
	now      func() time.Time
	mu       sync.Mutex
	services map[string]*resident
	closed   bool
}

// resident is one loaded service.
type resident struct {
	rt       *Runtime
	manifest *Manifest
	lastUsed time.Time
	gate     chan struct{}
	// busy counts the calls running on this runtime. A sweep or an LRU
	// eviction must leave it alone: closing the runtime under a call would
	// fail work that is already in progress, for a reason the caller never
	// asked for.
	busy int
}

// SupervisorOptions configures a Supervisor. The zero value is usable.
type SupervisorOptions struct {
	// Max is how many services stay resident; zero means 4.
	Max int
	// Idle is how long an unused service stays loaded; zero means 5 minutes.
	Idle time.Duration
	// Timeout bounds one call; zero means DefaultCallTimeout.
	Timeout time.Duration
}

// NewSupervisor builds a supervisor that loads services through open.
//
// open returns the manifest and the entry source of one service. The supervisor
// never touches the disk itself: a host may read a package, a database or a URL,
// and its errors are its own.
func NewSupervisor(host Host, allowed []Permission, opts SupervisorOptions, open func(ctx context.Context, id string) (*Manifest, string, error)) *Supervisor {
	return NewSupervisorFor(func(string) Host { return host }, allowed, opts, open)
}

// NewSupervisorFor is NewSupervisor with one host per service: a client that
// keeps credentials and storage per service builds the host it needs for each.
func NewSupervisorFor(hostFor func(id string) Host, allowed []Permission, opts SupervisorOptions, open func(ctx context.Context, id string) (*Manifest, string, error)) *Supervisor {
	if open == nil {
		open = func(ctx context.Context, id string) (*Manifest, string, error) {
			return nil, "", fmt.Errorf("service: no loader for %q", id)
		}
	}
	max := opts.Max
	if max <= 0 {
		max = 4
	}
	idle := opts.Idle
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	if hostFor == nil {
		hostFor = func(string) Host { return Host{} }
	}
	return &Supervisor{
		hostFor: hostFor, granted: allowed, open: open,
		max: max, idle: idle, timeout: timeout, now: time.Now,
		services: map[string]*resident{},
	}
}

// Call runs one method of a service, loading it first when it is not resident.
//
// The call is bounded by the supervisor's timeout. On timeout the runtime is
// interrupted and discarded: a script that is still running cannot be trusted
// to have left its own state consistent, so the next call loads it again.
func (s *Supervisor) Call(ctx context.Context, id, method string, payload json.RawMessage) (json.RawMessage, error) {
	return supervise(ctx, s, id, func(ctx context.Context, rt *Runtime) (json.RawMessage, error) {
		return rt.Call(ctx, method, payload)
	})
}

func supervise[T any](ctx context.Context, s *Supervisor, id string, run func(context.Context, *Runtime) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	res, err := s.acquire(ctx, id)
	if err != nil {
		return zero, err
	}
	defer s.release(res)
	select {
	case res.gate <- struct{}{}:
		defer func() { <-res.gate }()
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-res.rt.ctx.Done():
		return zero, res.rt.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	result, err := run(ctx, res.rt)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.drop(id, res)
		return zero, fmt.Errorf("%w after %s", ErrTimeout, s.timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		s.drop(id, res)
		return zero, ctx.Err()
	}
	s.touch(id, res)
	return result, err
}

// release marks one call on the resident as finished.
func (s *Supervisor) release(res *resident) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res.busy > 0 {
		res.busy--
	}
}

// acquire returns the resident runtime of a service, loading it if needed.
func (s *Supervisor) acquire(ctx context.Context, id string) (*resident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("service: the supervisor is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if res, ok := s.services[id]; ok {
		res.busy++
		return res, nil
	}
	if len(s.services) >= s.max {
		s.evictLocked()
	}
	manifest, source, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	if manifest.ID != id {
		return nil, fmt.Errorf("service: loader returned %q for %q", manifest.ID, id)
	}
	rt := NewRuntime(s.hostFor(id), s.granted...)
	if err := rt.Load(ctx, manifest, source); err != nil {
		rt.Close()
		return nil, err
	}
	res := &resident{rt: rt, manifest: manifest, lastUsed: s.now(), gate: make(chan struct{}, 1), busy: 1}
	s.services[id] = res
	return res, nil
}

// touch records that a service was used.
func (s *Supervisor) touch(id string, res *resident) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.services[id]; ok && current == res {
		current.lastUsed = s.now()
	}
}

// drop unloads a service that misbehaved.
func (s *Supervisor) drop(id string, res *resident) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.services[id]; ok && current == res {
		delete(s.services, id)
		current.rt.Close()
	}
}

// evictLocked unloads the least recently used service that is not running a
// call. When every resident is busy the limit is exceeded rather than a call
// being closed underneath: the caller is waiting on that work.
func (s *Supervisor) evictLocked() {
	var oldest string
	var when time.Time
	for id, res := range s.services {
		if res.busy > 0 {
			continue
		}
		if oldest == "" || res.lastUsed.Before(when) {
			oldest, when = id, res.lastUsed
		}
	}
	if oldest != "" {
		s.services[oldest].rt.Close()
		delete(s.services, oldest)
	}
}

// Describe asks a service what it offers, loading it first when it is not
// resident.
func (s *Supervisor) Describe(ctx context.Context, id string) ([]Method, error) {
	return supervise(ctx, s, id, func(ctx context.Context, rt *Runtime) ([]Method, error) {
		return rt.Describe(ctx)
	})
}

// Invalidate cancels the work of one resident and unloads its runtime.
func (s *Supervisor) Invalidate(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res := s.services[id]; res != nil {
		delete(s.services, id)
		res.rt.Close()
	}
}

// Sweep unloads services that have been idle longer than the idle timeout. It
// returns how many were unloaded.
func (s *Supervisor) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-s.idle)
	unloaded := 0
	for id, res := range s.services {
		if res.busy > 0 {
			continue
		}
		if res.lastUsed.Before(cutoff) {
			res.rt.Close()
			delete(s.services, id)
			unloaded++
		}
	}
	return unloaded
}

// Loaded lists the resident services, sorted.
func (s *Supervisor) Loaded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.services))
	for id := range s.services {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Manifest returns the manifest of a resident service.
func (s *Supervisor) Manifest(id string) (*Manifest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.services[id]
	if !ok {
		return nil, false
	}
	return res.manifest, true
}

// Close unloads every service and refuses later calls.
func (s *Supervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for id, res := range s.services {
		res.rt.Close()
		delete(s.services, id)
	}
}
