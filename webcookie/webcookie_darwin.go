//go:build darwin

package webcookie

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	loadOnce       sync.Once
	loadErr        error
	dispatchAsyncF func(queue, ctx, work uintptr)
	mainQueue      uintptr
	mainWork       uintptr
	mainMu         sync.Mutex
	mainCalls      []func()
)

// load opens WebKit and libdispatch once. WKHTTPCookieStore answers on the
// main thread only, so calls go there through dispatch_async_f.
func load() error {
	loadOnce.Do(func() {
		if _, loadErr = purego.Dlopen("/System/Library/Frameworks/WebKit.framework/WebKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); loadErr != nil {
			return
		}
		var lib uintptr
		if lib, loadErr = purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL); loadErr != nil {
			return
		}
		purego.RegisterLibFunc(&dispatchAsyncF, lib, "dispatch_async_f")
		if mainQueue, loadErr = purego.Dlsym(lib, "_dispatch_main_q"); loadErr != nil {
			return
		}
		mainWork = purego.NewCallback(func(uintptr) {
			mainMu.Lock()
			fn := mainCalls[0]
			mainCalls = mainCalls[1:]
			mainMu.Unlock()
			fn()
		})
	})
	return loadErr
}

func onMain(fn func()) {
	mainMu.Lock()
	mainCalls = append(mainCalls, fn)
	mainMu.Unlock()
	dispatchAsyncF(mainQueue, 0, mainWork)
}

func read(ctx context.Context, u *url.URL, _ Options) ([]*http.Cookie, error) {
	if err := load(); err != nil {
		return nil, err
	}
	done := make(chan []*http.Cookie, 1)
	onMain(func() {
		store := objc.ID(objc.GetClass("WKWebsiteDataStore")).Send(sel("defaultDataStore")).Send(sel("httpCookieStore"))
		handler := objc.NewBlock(func(_ objc.Block, list objc.ID) {
			now := time.Now()
			var out []*http.Cookie
			for i := range objc.Send[uint](list, sel("count")) {
				c := cookie(list.Send(sel("objectAtIndex:"), i))
				if matches(c, u, now) {
					out = append(out, c)
				}
			}
			done <- out
		})
		store.Send(sel("getAllCookies:"), handler)
		handler.Release()
	})
	select {
	case out := <-done:
		return out, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func cookie(c objc.ID) *http.Cookie {
	out := &http.Cookie{
		Name:     goString(c.Send(sel("name"))),
		Value:    goString(c.Send(sel("value"))),
		Domain:   goString(c.Send(sel("domain"))),
		Path:     goString(c.Send(sel("path"))),
		Secure:   objc.Send[bool](c, sel("isSecure")),
		HttpOnly: objc.Send[bool](c, sel("isHTTPOnly")),
	}
	if date := c.Send(sel("expiresDate")); date != 0 {
		out.Expires = time.UnixMilli(int64(objc.Send[float64](date, sel("timeIntervalSince1970")) * 1000))
	}
	return out
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[*byte](ns, sel("UTF8String"))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}
