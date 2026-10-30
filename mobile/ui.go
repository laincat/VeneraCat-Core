package mobile

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/laincat/veneracat-core/comic/js"
)

// This file adapts the JSON Host to the cookie, UI and clipboard services of
// the comic runtime.

// wireCookie is one cookie as the client sees it.
type wireCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Expires  int64  `json:"expires,omitempty"` // Unix milliseconds
	MaxAge   int    `json:"maxAge,omitempty"`  // seconds
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
}

func toWireCookies(cs []*http.Cookie) []wireCookie {
	out := make([]wireCookie, 0, len(cs))
	for _, c := range cs {
		if c == nil {
			continue
		}
		w := wireCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			MaxAge: c.MaxAge, Secure: c.Secure, HTTPOnly: c.HttpOnly}
		if !c.Expires.IsZero() {
			w.Expires = c.Expires.UnixMilli()
		}
		out = append(out, w)
	}
	return out
}

func fromWireCookies(ws []wireCookie) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(ws))
	for _, w := range ws {
		if w.Name == "" {
			continue
		}
		c := &http.Cookie{Name: w.Name, Value: w.Value, Domain: w.Domain, Path: w.Path,
			MaxAge: w.MaxAge, Secure: w.Secure, HttpOnly: w.HTTPOnly}
		if w.Expires != 0 {
			c.Expires = time.UnixMilli(w.Expires)
		}
		out = append(out, c)
	}
	return out
}

// cookieBridge implements hostapi.CookieStore. The store interface has no
// error results, so host failures are logged and a failed read is empty.
type cookieBridge struct{ b *hostBridge }

func (c cookieBridge) Set(partition, url string, cookies []*http.Cookie) {
	in, _ := json.Marshal(toWireCookies(cookies))
	if err := c.b.h.SaveCookies(partition, url, string(in)); err != nil {
		c.b.h.Log("error", "cookie", "save: "+err.Error())
	}
}

func (c cookieBridge) Get(partition, url string) []*http.Cookie {
	s, err := c.b.h.LoadCookies(partition, url)
	if err != nil {
		c.b.h.Log("error", "cookie", "load: "+err.Error())
		return nil
	}
	if s == "" {
		return nil
	}
	var ws []wireCookie
	if err := json.Unmarshal([]byte(s), &ws); err != nil {
		c.b.h.Log("error", "cookie", "load: "+err.Error())
		return nil
	}
	return fromWireCookies(ws)
}

func (c cookieBridge) Delete(partition, url string) {
	if err := c.b.h.DeleteCookies(partition, url); err != nil {
		c.b.h.Log("error", "cookie", "delete: "+err.Error())
	}
}

// uiBridge implements js.UIHandler. The runtime calls it off the script
// thread, so blocking host methods are safe here.
type uiBridge struct{ b *hostBridge }

type wireAction struct {
	Text  string `json:"text"`
	Style string `json:"style"`
}

// choice decodes {"index":n}; "" means closed without choosing.
func choice(s string, n int) (*int, error) {
	if s == "" {
		return nil, nil
	}
	var w struct {
		Index *int `json:"index"`
	}
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return nil, fmt.Errorf("mobile: UI result: %w", err)
	}
	if w.Index == nil {
		return nil, nil
	}
	if *w.Index < 0 || *w.Index >= n {
		return nil, fmt.Errorf("mobile: UI result index %d out of range", *w.Index)
	}
	return w.Index, nil
}

func (u uiBridge) ShowMessage(message string) { u.b.h.ShowMessage(message) }
func (u uiBridge) LaunchURL(url string)       { u.b.h.LaunchURL(url) }

func (u uiBridge) ShowDialog(title, content string, actions []js.DialogAction) error {
	wire := make([]wireAction, len(actions))
	for i, a := range actions {
		wire[i] = wireAction{Text: a.Text, Style: a.Style}
	}
	in, _ := json.Marshal(wire)
	out, err := u.b.h.ShowDialog(title, content, string(in))
	if err != nil {
		return err
	}
	i, err := choice(out, len(actions))
	if err != nil || i == nil {
		return err
	}
	return actions[*i].Invoke()
}

func (u uiBridge) ShowInputDialog(title, image string, validate func(string) string) (*string, error) {
	message := ""
	for {
		out, err := u.b.h.ShowInputDialog(title, image, message)
		if err != nil || out == "" {
			return nil, err
		}
		var w struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal([]byte(out), &w); err != nil {
			return nil, fmt.Errorf("mobile: UI result: %w", err)
		}
		if w.Text == nil {
			return nil, nil
		}
		if validate == nil {
			return w.Text, nil
		}
		if message = validate(*w.Text); message == "" {
			return w.Text, nil
		}
	}
}

func (u uiBridge) ShowSelectDialog(title string, options []string, initialIndex *int) (*int, error) {
	in, _ := json.Marshal(options)
	initial := int64(-1)
	if initialIndex != nil {
		initial = int64(*initialIndex)
	}
	out, err := u.b.h.ShowSelectDialog(title, string(in), initial)
	if err != nil {
		return nil, err
	}
	return choice(out, len(options))
}

func (u uiBridge) ShowLoading(onCancel func()) int {
	b := u.b
	b.loadingMu.Lock()
	b.nextLoading++
	id := b.nextLoading
	if onCancel != nil {
		b.loading[id] = onCancel
	}
	b.loadingMu.Unlock()
	b.h.ShowLoading(id)
	return int(id)
}

func (u uiBridge) CancelLoading(id int) {
	b := u.b
	b.loadingMu.Lock()
	delete(b.loading, int64(id))
	b.loadingMu.Unlock()
	b.h.CancelLoading(int64(id))
}

// loadingDismissed runs the onCancel of a loading indicator the user closed.
// Each callback runs at most once.
func (b *hostBridge) loadingDismissed(id int64) {
	b.loadingMu.Lock()
	f := b.loading[id]
	delete(b.loading, id)
	b.loadingMu.Unlock()
	if f != nil {
		f()
	}
}

// clipboardBridge implements js.Clipboard.
type clipboardBridge struct{ b *hostBridge }

func (c clipboardBridge) SetText(text string) error { return c.b.h.WriteClipboard(text) }
func (c clipboardBridge) Text() (string, error)     { return c.b.h.ReadClipboard() }
