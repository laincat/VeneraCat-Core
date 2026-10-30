// Magpie provider packages, reached through the same JSON-in/JSON-out surface
// as the rest of Core. The client supplies HTTP through Host.HTTP, so Core
// still opens no connections; sign-ins stay in the client, which hands one in
// and reads back whatever the package saved.
package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/laincat/veneracat-core/host"
	"github.com/laincat/veneracat-core/magpieplugin"
)

// ErrMagpieClosed reports use of a Magpie after Close.
var ErrMagpieClosed = errors.New("mobile: the Magpie plugin instance is closed")

// Magpie runs one installed provider package for one account. Create it with
// NewMagpie, install with LoadPackage, then Call a hook.
type Magpie struct {
	mu   sync.Mutex
	host Host
	// stream is the client's optional chunked transport. Nil means replies
	// arrive whole; a package that reads a stream still works, it just waits.
	stream HostStream
	loader *magpieplugin.Loader
	inst   *magpieplugin.Instance
	// auth is the account's stored sign-in, as the package reads it.
	auth map[string]any
	// saved is what the package wrote back; the client stores it in Keychain.
	saved    map[string]any
	provider string
	closed   bool

	// options are the plugin's own switches, keyed "provider|option".
	options map[string]bool

	// chats holds the conversations this runner is delivering, by handle.
	chats    map[int64]*chatStream
	chatNext int64
}

// NewMagpie returns a runner using the client's HTTP host.
func NewMagpie(host Host) *Magpie { return &Magpie{host: host} }

// NewMagpieWithStream returns a runner that can also read a reply as it
// arrives. A client that implements HostStream passes itself here; one that
// does not keeps using NewMagpie and gets whole replies.
func NewMagpieWithStream(host Host, stream HostStream) *Magpie {
	return &Magpie{host: host, stream: stream}
}

// LoadPackage installs a package from its files, a JSON object of
// {path: base64 content}. Every file is kept in memory; nothing is written to
// disk and nothing runs until Call.
// ReadPackageArchive expands a downloaded package (zip or npm tar.gz) into the
// base64 file table LoadPackage takes. It lives here rather than in the client so
// both clients agree on what a package archive is, and so the path rules are
// applied once: symlinks and entries that climb out of the archive are refused.
func (m *Magpie) ReadPackageArchive(data []byte) (string, error) {
	files, err := magpieplugin.ReadArchive(data)
	if err != nil {
		return "", err
	}
	encoded := make(map[string]string, len(files))
	for name, body := range files {
		encoded[name] = base64.StdEncoding.EncodeToString(body)
	}
	answer, err := json.Marshal(encoded)
	if err != nil {
		return "", err
	}
	return string(answer), nil
}

// LoadPackage hands Core a package as {path: base64 body}. The path rules and the
// Archive limits live in Core, so a client can pass what it unpacked without
// re-implementing either.
func (m *Magpie) LoadPackage(filesJSON string) error {
	if m == nil {
		return ErrMagpieClosed
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(filesJSON), &encoded); err != nil {
		return fmt.Errorf("mobile: package files: %w", err)
	}
	files := make(map[string][]byte, len(encoded))
	for name, body := range encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return fmt.Errorf("mobile: package file %s: %w", name, err)
		}
		files[name] = raw
	}
	bundle, err := magpieplugin.NewBundle(files)
	if err != nil {
		return err
	}
	loader, err := magpieplugin.Load(bundle)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.loader = loader
	m.mu.Unlock()
	return nil
}

// SetAuth hands the account's sign-in to the package: a JSON object in the
// shape Magpie keeps ({"type":"oauth","refresh":…,"access":…,"expires":…} or
// {"type":"api","key":…}). provider is the id the package serves.
func (m *Magpie) SetAuth(provider, authJSON string) error {
	if m == nil {
		return ErrMagpieClosed
	}
	var auth map[string]any
	if strings.TrimSpace(authJSON) != "" {
		if err := json.Unmarshal([]byte(authJSON), &auth); err != nil {
			return fmt.Errorf("mobile: auth: %w", err)
		}
	}
	m.mu.Lock()
	m.provider, m.auth = provider, auth
	m.mu.Unlock()
	return nil
}

// SavedAuth returns the sign-in the package wrote back during the last Call as
// JSON, or "" when it wrote none. The client stores it where it keeps secrets.
func (m *Magpie) SavedAuth() (string, error) {
	if m == nil {
		return "", ErrMagpieClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saved == nil {
		return "", nil
	}
	body, err := json.Marshal(m.saved)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Call runs an exported hook with one JSON argument and returns its result as
// JSON. The instance is created on first use, so the client can set the auth
// before anything runs.
func (m *Magpie) Call(export, argJSON string) (string, error) {
	if m == nil {
		return "", ErrMagpieClosed
	}
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	var arg any
	if strings.TrimSpace(argJSON) != "" {
		dec := json.NewDecoder(strings.NewReader(argJSON))
		dec.UseNumber()
		if err := dec.Decode(&arg); err != nil {
			return "", fmt.Errorf("mobile: argument: %w", err)
		}
	}
	result, err := inst.Call(export, arg)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Auth describes what the package's plugin offers: provider id, sign-in
// methods and the plugin's own options, as JSON. It is "" when the package has
// no plugin.
func (m *Magpie) Auth() (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	answer, err := inst.PluginAuth()
	if err != nil {
		return "", err
	}
	if answer == nil {
		return "", nil
	}
	return marshalJSON(answer)
}

// Authorize starts one sign-in method and returns the page to show the user:
// {"id","url","userCode","instructions","expiresIn"}.
func (m *Magpie) Authorize(methodIndex int, inputsJSON string) (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	var inputs map[string]any
	if strings.TrimSpace(inputsJSON) != "" {
		dec := json.NewDecoder(strings.NewReader(inputsJSON))
		dec.UseNumber()
		if err := dec.Decode(&inputs); err != nil {
			return "", fmt.Errorf("mobile: sign-in inputs: %w", err)
		}
	}
	answer, err := inst.Authorize(methodIndex, inputs)
	if err != nil {
		return "", err
	}
	return marshalJSON(answer)
}

// Poll waits once for the user to finish signing in and returns the package's
// answer, usually {"type":"success","refresh":…} or {"type":"failed"}.
func (m *Magpie) Poll(id int64) (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	answer, err := inst.Poll(id)
	if err != nil {
		return "", err
	}
	if answer == nil {
		return "", nil
	}
	return marshalJSON(answer)
}

// CancelSignIn forgets a sign-in the user abandoned.
func (m *Magpie) CancelSignIn(id int64) error {
	inst, err := m.use()
	if err != nil {
		return err
	}
	return inst.CancelSignIn(id)
}

// Usage runs the package's usage hook as JSON, or "" when it has none.
func (m *Magpie) Usage(provider string) (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	answer, err := inst.Usage(provider)
	if err != nil {
		return "", err
	}
	if answer == nil {
		return "", nil
	}
	return marshalJSON(answer)
}

// UsageSummary runs the package's usage hook and answers the normalised form, so
// a client shows one shape whatever vendor answered. It is "" when the package
// reports no usage.
func (m *Magpie) UsageSummary(provider string) (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	return inst.UsageJSON(provider)
}

// SetPluginOptions hands the plugin its own switches: a JSON object of
// {"provider|option": true}. Magpie keeps them per provider, and a package that
// declares an option reads it through input.magpie.option.
func (m *Magpie) SetPluginOptions(optionsJSON string) error {
	if m == nil {
		return ErrMagpieClosed
	}
	var options map[string]bool
	if strings.TrimSpace(optionsJSON) != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
			return fmt.Errorf("mobile: plugin options: %w", err)
		}
	}
	m.mu.Lock()
	m.options = options
	m.mu.Unlock()
	if inst := m.existing(); inst != nil {
		inst.SetPluginOptions(options)
	}
	return nil
}

// Checkin presses the account's check-in and returns Magpie's fixed answer:
// {"outcome","credit","streak","message","until"}. An outcome outside the set
// comes back as "failed" with the plugin's own words.
func (m *Magpie) Checkin(provider string) (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	return inst.CheckinJSON(provider)
}

// Models asks the vendor what it serves, as JSON. A vendor that does not list
// models answers an error naming that, rather than an empty or invented list.
func (m *Magpie) Models() (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	return inst.ModelsJSON(context.Background())
}

// existing returns the instance without creating one.
func (m *Magpie) existing() *magpieplugin.Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inst
}

// use returns the instance, creating it when this is the first call.
func (m *Magpie) use() (*magpieplugin.Instance, error) {
	if m == nil {
		return nil, ErrMagpieClosed
	}
	m.mu.Lock()
	loader, closed := m.loader, m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrMagpieClosed
	}
	if loader == nil {
		return nil, errors.New("mobile: no package loaded")
	}
	return m.instance(loader)
}

func marshalJSON(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Close ends the instance and cancels its requests.
func (m *Magpie) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	inst, closed := m.inst, m.closed
	m.inst, m.closed = nil, true
	// A conversation still running when the runner goes away would keep
	// producing into a channel nobody reads.
	streams := make([]*chatStream, 0, len(m.chats))
	for handle, stream := range m.chats {
		streams = append(streams, stream)
		delete(m.chats, handle)
	}
	m.mu.Unlock()
	for _, stream := range streams {
		stream.cancel()
	}
	if !closed && inst != nil {
		inst.Close()
	}
}

func (m *Magpie) instance(loader *magpieplugin.Loader) (*magpieplugin.Instance, error) {
	m.mu.Lock()
	if m.inst != nil {
		inst := m.inst
		m.mu.Unlock()
		return inst, nil
	}
	provider, auth, host := m.provider, m.auth, m.host
	m.mu.Unlock()

	inst, err := loader.NewInstance(magpieplugin.Options{})
	if err != nil {
		return nil, err
	}
	var pluginHost magpieplugin.Host = &mobileHost{host: host}
	if m.stream != nil {
		pluginHost = &mobileStreamHost{mobileHost: mobileHost{host: host}, stream: m.stream}
	}
	if err := inst.UseHost(pluginHost); err != nil {
		inst.Close()
		return nil, err
	}
	if provider != "" {
		if err := inst.UseClient(provider, &mobileAuth{m: m, provider: provider, initial: auth}); err != nil {
			inst.Close()
			return nil, err
		}
	}
	m.mu.Lock()
	options := m.options
	m.mu.Unlock()
	if options != nil {
		inst.SetPluginOptions(options)
	}
	// The plugin ABI lives in Core: a client asks for a sign-in, it does not
	// reproduce Magpie's factory convention in its own language.
	if err := inst.InstallPluginBridge(); err != nil {
		inst.Close()
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		inst.Close()
		return nil, ErrMagpieClosed
	}
	m.inst = inst
	m.mu.Unlock()
	return inst, nil
}

// mobileHost sends a package's requests through the client's Host.HTTP, which
// is the same path the comic sources use.
type mobileHost struct{ host Host }

// mobileStreamHost is the same host plus DoStream. It exists as its own type
// because the plugin host type-asserts for magpieplugin.Streamer: a host that
// always had the method would be asked to stream even when the client cannot,
// and the package would fail instead of falling back to whole replies.
type mobileStreamHost struct {
	mobileHost
	stream HostStream
}

func (h *mobileStreamHost) DoStream(ctx context.Context, req *magpieplugin.Request, onChunk func([]byte) error) (*magpieplugin.Response, error) {
	bridge := streamBridge{h: h.stream, assign: nextHostRequestID}
	return bridge.DoStream(ctx, req, onChunk)
}

func (h *mobileHost) Do(ctx context.Context, req *magpieplugin.Request) (*magpieplugin.Response, error) {
	// Reuse the wire contract and cancellation used by comic requests. In
	// particular, Swift requires an id to decode the request and CancelHTTP must
	// name that same task. Magpie never reads or writes the comic cookie jars.
	bridge := hostBridge{h: h.host}
	reply, err := bridge.Do(ctx, &host.Request{
		Method: req.Method, URL: req.URL, Header: http.Header(req.Header).Clone(),
		Body: req.Body, Cookies: false, Partition: "magpie",
	})
	if err != nil {
		return nil, err
	}
	return &magpieplugin.Response{
		Status: reply.Status, StatusText: http.StatusText(reply.Status),
		Header: reply.Header, Body: reply.Body, URL: reply.FinalURL,
	}, nil
}

// mobileAuth is the account's sign-in as the package sees it: it reads the
// value the client supplied and records what the package saves.
type mobileAuth struct {
	m        *Magpie
	provider string
	initial  map[string]any
}

func (a *mobileAuth) Get(context.Context, string) (map[string]any, error) {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	return a.initial, nil
}

func (a *mobileAuth) Set(_ context.Context, _ string, value map[string]any) error {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	a.m.saved = value
	a.initial = value
	return nil
}

// A conversation is delivered the way gomobile allows: the client starts one,
// then asks for events. Nothing blocks waiting on the other side, so a Swift
// task can await each question in its own loop and stop whenever the view goes
// away.

// ChatStart begins a conversation turn and returns {"id":n}. Events are JSON:
// {"kind":"text","text":…}, {"kind":"reasoning",…}, {"kind":"done",…},
// {"kind":"error","text":…}, and finally {"kind":"end"}.
//
// The handle comes back as JSON rather than as a second return value: gomobile
// turns extra returns into out-parameters, which Swift has to pass pointers for.
func (m *Magpie) ChatStart(model, messagesJSON, system string, maxTokens int) (string, error) {
	inst, err := m.use()
	if err != nil {
		return "", err
	}
	var messages []magpieplugin.Message
	if strings.TrimSpace(messagesJSON) != "" {
		if err := json.Unmarshal([]byte(messagesJSON), &messages); err != nil {
			return "", fmt.Errorf("mobile: messages: %w", err)
		}
	}
	req := &magpieplugin.ChatRequest{Model: model, Messages: messages, System: system, MaxTokens: maxTokens}

	ctx, cancel := context.WithCancel(context.Background())
	stream := &chatStream{events: make(chan string, 64), cancel: cancel}
	go func() {
		defer close(stream.events)
		defer cancel()
		err := inst.Chat(ctx, req, func(e magpieplugin.Event) error {
			body, marshalErr := chatEventJSON(e)
			if marshalErr != nil {
				return marshalErr
			}
			select {
			case stream.events <- body:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			body, _ := json.Marshal(map[string]string{"kind": "error", "text": err.Error()})
			stream.events <- string(body)
		}
		stream.events <- `{"kind":"end"}`
	}()

	m.mu.Lock()
	if m.chats == nil {
		m.chats = map[int64]*chatStream{}
	}
	m.chatNext++
	handle := m.chatNext
	m.chats[handle] = stream
	m.mu.Unlock()
	return fmt.Sprintf(`{"id":%d}`, handle), nil
}

// ChatNext returns the next event as JSON, "" when none is ready yet, and
// {"kind":"end"} once the conversation is over. An unknown handle answers
// {"kind":"end"} as well: the client may have stopped the view.
func (m *Magpie) ChatNext(handle int64) (string, error) {
	if m == nil {
		return "", ErrMagpieClosed
	}
	m.mu.Lock()
	stream := m.chats[handle]
	m.mu.Unlock()
	if stream == nil {
		return `{"kind":"end"}`, nil
	}
	select {
	case event, ok := <-stream.events:
		if !ok {
			m.forgetChat(handle)
			return `{"kind":"end"}`, nil
		}
		if event == `{"kind":"end"}` {
			m.forgetChat(handle)
		}
		return event, nil
	default:
		return "", nil
	}
}

// ChatCancel stops a conversation: the package's request is cancelled and its
// handle is dropped.
func (m *Magpie) ChatCancel(handle int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	stream := m.chats[handle]
	delete(m.chats, handle)
	m.mu.Unlock()
	if stream != nil {
		stream.cancel()
	}
}

func (m *Magpie) forgetChat(handle int64) {
	m.mu.Lock()
	delete(m.chats, handle)
	m.mu.Unlock()
}

type chatStream struct {
	events chan string
	cancel context.CancelFunc
}

func chatEventJSON(e magpieplugin.Event) (string, error) {
	payload := map[string]any{"kind": string(e.Kind), "text": e.Text}
	if e.Finish != "" {
		payload["finish"] = e.Finish
	}
	// The vendor's word is `finish`; `outcome` is the same ending in this core's
	// vocabulary, so a client can ask "was the reply cut short" without knowing
	// which vendor answered.
	if e.Outcome != "" {
		payload["outcome"] = string(e.Outcome)
	}
	if e.Usage != nil {
		payload["usage"] = map[string]any{
			"input": e.Usage.Input, "output": e.Usage.Output,
			"cacheRead": e.Usage.CacheRead, "cacheWrite": e.Usage.CacheWrite,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
