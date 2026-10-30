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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := s.acquireBusy(ctx, id)
	if err != nil {
		return nil, err
	}
	defer s.release(res)
	result, err := s.callWithTimeout(ctx, res, method, payload)
	if err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.Canceled) {
			s.drop(id, res)
			return nil, err
		}
		// A script error is the service's problem, not the runtime's: keep it
		// resident so a transient failure does not force a reload.
		s.touch(id, res)
		return nil, err
	}
	s.touch(id, res)
	return result, nil
}

// callWithTimeout runs the call and interrupts the runtime when the deadline
// passes. The interrupt is the only way to stop a busy script: the runtime is
// single-threaded and will not look at a context.
func (s *Supervisor) callWithTimeout(ctx context.Context, res *resident, method string, payload json.RawMessage) (json.RawMessage, error) {
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)

	// The interrupt is armed only while the call is running: a goroutine that
	// woke up late must not interrupt a runtime the next call is already using.
	// The flag and the calling goroutine take the same lock, so either the call
	// is still running and gets interrupted, or it finished and is left alone.
	var state struct {
		mu   sync.Mutex
		done bool
	}
	go func() {
		<-callCtx.Done()
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.done {
			return
		}
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			res.interrupt(ErrTimeout)
			return
		}
		res.interrupt(context.Canceled)
	}()

	result, err := res.rt.Call(callCtx, method, payload)
	state.mu.Lock()
	state.done = true
	state.mu.Unlock()
	reason := callCtx.Err()
	cancel()

	if err != nil && reason != nil {
		if errors.Is(reason, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w after %s", ErrTimeout, s.timeout)
		}
		return nil, reason
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// release marks one call on the resident as finished.
func (s *Supervisor) release(res *resident) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res.busy > 0 {
		res.busy--
	}
}

// acquireBusy is acquire for a call: the resident is marked busy before the
// lock is released, so no sweep can close it between acquiring and calling.
func (s *Supervisor) acquireBusy(ctx context.Context, id string) (*resident, error) {
	res, err := s.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	res.busy++
	s.mu.Unlock()
	return res, nil
}

// acquire returns the resident runtime of a service, loading it if needed.
func (s *Supervisor) acquire(ctx context.Context, id string) (*resident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("service: the supervisor is closed")
	}
	if res, ok := s.services[id]; ok {
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
	res := &resident{rt: rt, manifest: manifest, lastUsed: s.now()}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := s.acquireBusy(ctx, id)
	if err != nil {
		return nil, err
	}
	defer s.release(res)
	methods, err := res.rt.Describe(ctx)
	if err != nil {
		s.touch(id, res)
		return nil, err
	}
	s.touch(id, res)
	return methods, nil
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

// interrupt stops the runtime of one service, naming why.
func (r *resident) interrupt(reason error) {
	r.rt.Interrupt(reason)
}
