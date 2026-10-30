package js

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Logging in through the site's own page.
//
// Five sources declare this (`ccc`, `ehentai`, `manga_dex`, `mycomic`,
// `nhentai`) and a client without it can only tell the user "not supported":
//
//	account.loginWithWebview = {
//	  url: "https://…/login",
//	  checkStatus: (url, title) => bool,
//	  onLoginSuccess: async () => { … },
//	}
//
// The two callbacks cannot cross into JSON, so they travel as a handle the client
// hands back: the same shape the image callbacks use. The client opens `url` in a
// real browser, calls CheckWebviewStatus after each navigation, and calls
// WebviewLoginDone once the source says it is logged in.

// WebviewLogin is what a source declares for a browser login.
type WebviewLogin struct {
	// URL is the page the user has to log in on.
	URL string `json:"url"`
	// Handle names the declaration inside the source's runtime; pass it back to
	// the two calls below. Empty when the source declares no browser login.
	Handle string `json:"handle,omitempty"`
	// Callbacks lists which of checkStatus and onLoginSuccess the source wrote.
	Callbacks []string `json:"callbacks,omitempty"`
}

// webviewHandlePattern bounds what is accepted as a handle: the value comes back
// from a client, but a malformed one must not reach the source as code.
var webviewHandlePattern = regexp.MustCompile(`^w[0-9]+$`)

// accountWebviewHelpers keeps the handles in the runtime they belong to.
//
// A handle is only ever resolved through this table, so a dropped handle stops
// working instead of reaching into a newer declaration.
const accountWebviewHelpers = `
globalThis.__vcWebview = globalThis.__vcWebview || (function(){
  var box = {}, next = 0;
  return {
    take: function(cfg){ var id = 'w' + (++next), cbs = [];
      ['checkStatus','onLoginSuccess'].forEach(function(k){
        if(typeof cfg[k] === 'function'){ box[id+'.'+k] = cfg[k]; cbs.push(k); } });
      return {id: id, cbs: cbs}; },
    has: function(handle, name){ return typeof box[handle+'.'+name] === 'function'; },
    call: function(handle, name, args){
      var f = box[handle+'.'+name];
      if(typeof f !== 'function') throw new Error('source does not define this function');
      return f.apply(null, args || []); }
  };
})();`

// AccountWebviewLogin reads <Base>.sources[key].account.loginWithWebview.
//
// A source that logs in with a password or with pasted cookies answers no handle,
// which is how a client tells the three styles apart.
func (r *Runtime) AccountWebviewLogin(ctx context.Context, key string) (WebviewLogin, error) {
	e, err := r.engine(key)
	if err != nil {
		return WebviewLogin{}, err
	}
	src := accountWebviewHelpers + `(function(o){
      var c = o && o.account && o.account.loginWithWebview;
      if(!c) return null;
      var taken = globalThis.__vcWebview.take(c);
      return {url: String(c.url === undefined ? '' : c.url), handle: taken.id, callbacks: taken.cbs};
    })(` + e.kind.registry() + `[` + strconv.Quote(key) + `])`
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return WebviewLogin{}, err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return WebviewLogin{}, nil
	}
	var out WebviewLogin
	if err := json.Unmarshal(raw, &out); err != nil {
		return WebviewLogin{}, fmt.Errorf("js: loginWithWebview: %w", err)
	}
	if out.Handle == "" || out.URL == "" {
		return WebviewLogin{}, nil
	}
	return out, nil
}

// WebviewStatus runs the source's checkStatus(url, title) for the page the
// browser is on. A source without that callback answers false, which leaves the
// client waiting rather than guessing that the login finished.
func (r *Runtime) WebviewStatus(ctx context.Context, key, handle, pageURL, title string) (bool, error) {
	return r.webviewBool(ctx, key, handle, "checkStatus", pageURL, title)
}

// WebviewLoginDone runs onLoginSuccess, if the source wrote one. A source without
// it still logs in: the cookies are already in its jar by the time this is called.
func (r *Runtime) WebviewLoginDone(ctx context.Context, key, handle string) error {
	if !webviewHandlePattern.MatchString(handle) {
		return fmt.Errorf("js: bad webview handle %q", handle)
	}
	e, err := r.engine(key)
	if err != nil {
		return err
	}
	// A source without onLoginSuccess still logs in: the cookies are in its jar
	// by now, and the callback only exists to refresh something else.
	src := accountWebviewHelpers + `(function(){
      if(!globalThis.__vcWebview.has('` + handle + `', 'onLoginSuccess')) return null;
      return Promise.resolve(globalThis.__vcWebview.call('` + handle + `', 'onLoginSuccess', []));
    })()`
	if _, err := evalOn(ctx, e, src); err != nil {
		return fmt.Errorf("js: onLoginSuccess: %w", err)
	}
	return nil
}

func (r *Runtime) webviewBool(ctx context.Context, key, handle, name, pageURL, title string) (bool, error) {
	if !webviewHandlePattern.MatchString(handle) {
		return false, fmt.Errorf("js: bad webview handle %q", handle)
	}
	e, err := r.engine(key)
	if err != nil {
		return false, err
	}
	src := accountWebviewHelpers + `(function(){
      return Promise.resolve(globalThis.__vcWebview.call('` + handle + `', '` + name + `',
        [` + jsString(pageURL) + `, ` + jsString(title) + `]));
    })()`
	raw, err := evalOn(ctx, e, src)
	if err != nil {
		return false, fmt.Errorf("js: %s: %w", name, err)
	}
	var out bool
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, nil
	}
	return out, nil
}

// jsString quotes a client string for the evaluation above: a page URL or title
// may contain quotes, and building the source by concatenation would let them
// break out.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
