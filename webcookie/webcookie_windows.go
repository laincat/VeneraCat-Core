//go:build windows

package webcookie

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// WebView2 keeps a profile's cookies in the browser process of its folder and
// hands them out only through ICoreWebView2CookieManager, which hangs off a web
// view. Read makes one on a hidden window, in a thread of its own with its own
// apartment, asks for the address's cookies and closes it. Its environment joins
// the client's browser process on the same folder, so the answer is the session
// as it is now rather than what was last flushed to disk.

var (
	ole32    = syscall.NewLazyDLL("ole32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCoInitializeEx     = ole32.NewProc("CoInitializeEx")
	procCoUninitialize     = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree      = ole32.NewProc("CoTaskMemFree")
	procCreateWindowEx     = user32.NewProc("CreateWindowExW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procGetMessage         = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessage    = user32.NewProc("DispatchMessageW")
	procPostThreadMessage  = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
)

var (
	iidUnknown             = parseGUID("00000000-0000-0000-c000-000000000046")
	iidEnvironment         = parseGUID("b96d755e-0319-4e92-a296-23436f46a1fc")
	iidEnvironmentDone     = parseGUID("4e8a3389-c9d8-4bd2-b6b5-124fee6cc14d")
	iidControllerDone      = parseGUID("6c4819f3-c9b7-4260-8127-c9f5bde7f68c")
	iidWebView2            = parseGUID("9e8f0cf8-e670-4b5e-b2bc-73e061e3184c")
	iidGetCookiesCompleted = parseGUID("5a4f5069-5c15-47c3-8646-f4de1c116670")
)

const (
	coinitApartmentThreaded = 0x2
	wmQuit                  = 0x0012
	wsPopup                 = 0x80000000
	sOK                     = 0
	eNoInterface            = 0x80004002

	comQueryInterface = 0
	comAddRef         = 1
	comRelease        = 2

	envCreateController     = 3  // ICoreWebView2Environment
	controllerClose         = 24 // ICoreWebView2Controller
	controllerGetWebView    = 25
	webGetCookieManager     = 66 // ICoreWebView2_2
	managerGetCookies       = 5  // ICoreWebView2CookieManager
	cookieListGetCount      = 3  // ICoreWebView2CookieList
	cookieListGetValueAt    = 4
	cookieGetName           = 3 // ICoreWebView2Cookie
	cookieGetValue          = 4
	cookieGetDomain         = 6
	cookieGetPath           = 7
	cookieGetExpires        = 8
	cookieGetIsHTTPOnly     = 10
	cookieGetSameSite       = 12
	cookieGetIsSecure       = 14
	cookieGetIsSession      = 16
	evergreenClient         = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
	checkRunningInstance    = 1
	installedRuntime        = 0
	sameSiteLax, sameSiteSt = 1, 2
)

func read(ctx context.Context, u *url.URL, opts Options) ([]*http.Cookie, error) {
	if opts.WebView2Folder == "" {
		return nil, errors.New("webcookie: Options.WebView2Folder names no profile folder")
	}
	dll, err := findWebView2()
	if err != nil {
		return nil, err
	}
	type result struct {
		cookies []*http.Cookie
		err     error
	}
	done := make(chan result, 1)
	ready := make(chan uintptr, 1)
	go func() {
		// The thread keeps its apartment and window until it ends, and it ends
		// with this goroutine because it is never unlocked.
		runtime.LockOSThread()
		cookies, err := readOnThread(dll, opts.WebView2Folder, u.String(), ready)
		done <- result{cookies, err}
	}()
	thread := <-ready
	select {
	case r := <-done:
		return r.cookies, r.err
	case <-ctx.Done():
		if thread != 0 {
			procPostThreadMessage.Call(thread, wmQuit, 0, 0)
		}
		return nil, ctx.Err()
	}
}

// readOnThread runs on a locked thread. It reports the thread on ready once its
// message queue exists, so a cancelled Read can end its loop.
func readOnThread(dll, folder, rawURL string, ready chan<- uintptr) (cookies []*http.Cookie, err error) {
	if hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); failed(hr) {
		ready <- 0
		return nil, hresultError("webcookie: COM", hr)
	}
	defer procCoUninitialize.Call()
	static, _ := syscall.UTF16PtrFromString("STATIC")
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(static)), 0, wsPopup, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		ready <- 0
		return nil, errors.New("webcookie: no window for the web view")
	}
	defer procDestroyWindow.Call(hwnd)
	thread, _, _ := procGetCurrentThreadID.Call()
	ready <- thread

	var env, controller *comObject
	finish := func(e error) {
		if err == nil {
			err = e
		}
		procPostThreadMessage.Call(thread, wmQuit, 0, 0)
	}
	onCookies := newHandler(&iidGetCookiesCompleted, func(hr uintptr, list unsafe.Pointer) {
		if failed(hr) || list == nil {
			finish(hresultError("webcookie: WebView2 gave no cookies", hr))
			return
		}
		cookies = cookieList((*comObject)(list))
		finish(nil)
	})
	defer onCookies.release()
	onController := newHandler(&iidControllerDone, func(hr uintptr, result unsafe.Pointer) {
		if failed(hr) || result == nil {
			finish(hresultError("webcookie: WebView2 made no web view", hr))
			return
		}
		controller = (*comObject)(result)
		controller.addRef()
		var view *comObject
		if failed(controller.call(controllerGetWebView, uintptr(unsafe.Pointer(&view)))) || view == nil {
			finish(errors.New("webcookie: WebView2 controller has no web view"))
			return
		}
		defer view.release()
		view2 := view.query(&iidWebView2)
		if view2 == nil {
			finish(errors.New("webcookie: this WebView2 runtime has no cookie manager"))
			return
		}
		defer view2.release()
		var manager *comObject
		if failed(view2.call(webGetCookieManager, uintptr(unsafe.Pointer(&manager)))) || manager == nil {
			finish(errors.New("webcookie: WebView2 gave no cookie manager"))
			return
		}
		defer manager.release()
		address, _ := syscall.UTF16PtrFromString(rawURL)
		if hr := manager.call(managerGetCookies, uintptr(unsafe.Pointer(address)), onCookies.ptr()); failed(hr) {
			finish(hresultError("webcookie: GetCookies", hr))
		}
	})
	defer onController.release()
	onEnvironment := newHandler(&iidEnvironmentDone, func(hr uintptr, result unsafe.Pointer) {
		if failed(hr) || result == nil {
			finish(hresultError("webcookie: WebView2 could not start", hr))
			return
		}
		if env = (*comObject)(result).query(&iidEnvironment); env == nil {
			finish(errors.New("webcookie: WebView2 gave no environment"))
			return
		}
		if hr := env.call(envCreateController, hwnd, onController.ptr()); failed(hr) {
			finish(hresultError("webcookie: CreateCoreWebView2Controller", hr))
		}
	})
	defer onEnvironment.release()

	lib, lerr := syscall.LoadDLL(dll)
	if lerr != nil {
		return nil, lerr
	}
	create, lerr := lib.FindProc("CreateWebViewEnvironmentWithOptionsInternal")
	if lerr != nil {
		return nil, lerr
	}
	dir, _ := syscall.UTF16PtrFromString(folder)
	// No options: the client's environment on this folder is made with none,
	// and WebView2 refuses a second one whose options differ.
	if hr, _, _ := create.Call(checkRunningInstance, installedRuntime, uintptr(unsafe.Pointer(dir)), 0, onEnvironment.ptr()); failed(hr) {
		return nil, hresultError("webcookie: WebView2 could not start", hr)
	}
	var msg [48]byte // MSG, larger than either architecture's
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
	if controller != nil {
		controller.call(controllerClose)
		controller.release()
	}
	env.release()
	if err == nil && cookies == nil {
		err = errors.New("webcookie: WebView2 did not answer")
	}
	return cookies, err
}

func cookieList(list *comObject) []*http.Cookie {
	var n uint32
	if failed(list.call(cookieListGetCount, uintptr(unsafe.Pointer(&n)))) {
		return []*http.Cookie{}
	}
	out := make([]*http.Cookie, 0, n)
	for i := uint32(0); i < n; i++ {
		var c *comObject
		if failed(list.call(cookieListGetValueAt, uintptr(i), uintptr(unsafe.Pointer(&c)))) || c == nil {
			continue
		}
		out = append(out, cookieOf(c))
		c.release()
	}
	return out
}

func cookieOf(c *comObject) *http.Cookie {
	k := &http.Cookie{
		Name:   comString(c, cookieGetName),
		Value:  comString(c, cookieGetValue),
		Domain: comString(c, cookieGetDomain),
		Path:   comString(c, cookieGetPath),
	}
	var session, httpOnly, secure, site int32
	c.call(cookieGetIsSession, uintptr(unsafe.Pointer(&session)))
	if session == 0 {
		var expires float64 // seconds since 1970
		if !failed(c.call(cookieGetExpires, uintptr(unsafe.Pointer(&expires)))) && expires > 0 {
			k.Expires = time.UnixMilli(int64(expires * 1000))
		}
	}
	c.call(cookieGetIsHTTPOnly, uintptr(unsafe.Pointer(&httpOnly)))
	c.call(cookieGetIsSecure, uintptr(unsafe.Pointer(&secure)))
	c.call(cookieGetSameSite, uintptr(unsafe.Pointer(&site)))
	k.HttpOnly, k.Secure = httpOnly != 0, secure != 0
	switch site {
	case sameSiteLax:
		k.SameSite = http.SameSiteLaxMode
	case sameSiteSt:
		k.SameSite = http.SameSiteStrictMode
	default:
		k.SameSite = http.SameSiteNoneMode
	}
	return k
}

// comString reads a string property, which the callee allocates.
func comString(o *comObject, method int) string {
	var p *uint16
	if failed(o.call(method, uintptr(unsafe.Pointer(&p)))) || p == nil {
		return ""
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// findWebView2 finds the installed Evergreen runtime's
// EmbeddedBrowserWebView.dll as WebView2Loader does: the machine's install,
// 32-bit registry view first where EdgeUpdate writes, then the user's.
func findWebView2() (string, error) {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x86"}[runtime.GOARCH]
	roots := []struct {
		key  syscall.Handle
		view uint32
	}{
		{syscall.HKEY_LOCAL_MACHINE, syscall.KEY_WOW64_32KEY},
		{syscall.HKEY_LOCAL_MACHINE, syscall.KEY_WOW64_64KEY},
		{syscall.HKEY_CURRENT_USER, 0},
	}
	for _, root := range roots {
		client := `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + evergreenClient
		pv := regString(root.key, root.view, client, "pv")
		if pv == "" || pv == "0.0.0.0" || arch == "" {
			continue
		}
		var dirs []string
		if location := regString(root.key, root.view, client, "location"); location != "" {
			dirs = append(dirs, filepath.Join(location, pv))
		}
		state := `SOFTWARE\Microsoft\EdgeUpdate\ClientState\` + evergreenClient
		if dir := regString(root.key, root.view, state, "EBWebView"); dir != "" {
			dirs = append(dirs, dir)
		}
		for _, dir := range dirs {
			dll := filepath.Join(dir, "EBWebView", arch, "EmbeddedBrowserWebView.dll")
			if _, err := os.Stat(dll); err == nil {
				return dll, nil
			}
		}
	}
	return "", errors.New("webcookie: the WebView2 runtime is not installed")
}

func regString(root syscall.Handle, view uint32, path, name string) string {
	p, _ := syscall.UTF16PtrFromString(path)
	n, _ := syscall.UTF16PtrFromString(name)
	var key syscall.Handle
	if syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ|view, &key) != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)
	var kind, size uint32
	if syscall.RegQueryValueEx(key, n, nil, &kind, nil, &size) != nil ||
		(kind != syscall.REG_SZ && kind != syscall.REG_EXPAND_SZ) || size < 2 {
		return ""
	}
	buf := make([]uint16, size/2)
	if syscall.RegQueryValueEx(key, n, nil, &kind, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// COM through syscall: an interface pointer points to its vtable, and a method
// is the vtable's entry called with the pointer first.

type guid struct {
	a    uint32
	b, c uint16
	d    [8]byte
}

func parseGUID(s string) guid {
	hex := strings.ReplaceAll(s, "-", "")
	word := func(from, to int) uint64 {
		n, err := strconv.ParseUint(hex[from:to], 16, 64)
		if err != nil {
			panic("webcookie: bad GUID " + s)
		}
		return n
	}
	g := guid{a: uint32(word(0, 8)), b: uint16(word(8, 12)), c: uint16(word(12, 16))}
	for i := range g.d {
		g.d[i] = byte(word(16+2*i, 18+2*i))
	}
	return g
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func hresultError(what string, hr uintptr) error {
	return errors.New(what + ": HRESULT 0x" + strconv.FormatUint(uint64(uint32(hr)), 16))
}

type comObject struct{ vtbl *[128]uintptr }

// call keeps Go pointers passed as uintptr alive for the call.
//
//go:uintptrescapes
func (o *comObject) call(method int, args ...uintptr) uintptr {
	var buf [8]uintptr
	all := append(buf[:0], uintptr(unsafe.Pointer(o)))
	r, _, _ := syscall.SyscallN(o.vtbl[method], append(all, args...)...)
	return r
}

func (o *comObject) addRef() { o.call(comAddRef) }

func (o *comObject) release() {
	if o != nil {
		o.call(comRelease)
	}
}

func (o *comObject) query(iid *guid) *comObject {
	var out *comObject
	if failed(o.call(comQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))) {
		return nil
	}
	return out
}

// comHandler is a completion handler: IUnknown and an Invoke taking an HRESULT
// and an object. handlers keeps each alive while WebView2 holds a reference.
type comHandler struct {
	vtbl   *[4]uintptr
	refs   int32
	iid    *guid
	invoke func(hr uintptr, result unsafe.Pointer)
}

var (
	handlerVtbl = [4]uintptr{
		syscall.NewCallback(handlerQueryInterface),
		syscall.NewCallback(handlerAddRef),
		syscall.NewCallback(handlerRelease),
		syscall.NewCallback(handlerInvoke),
	}
	handlersMu sync.Mutex
	handlers   = map[*comHandler]struct{}{}
)

func newHandler(iid *guid, invoke func(hr uintptr, result unsafe.Pointer)) *comHandler {
	h := &comHandler{vtbl: &handlerVtbl, refs: 1, iid: iid, invoke: invoke}
	handlersMu.Lock()
	handlers[h] = struct{}{}
	handlersMu.Unlock()
	return h
}

func (h *comHandler) ptr() uintptr { return uintptr(unsafe.Pointer(h)) }

func (h *comHandler) release() { handlerRelease(h) }

func handlerQueryInterface(h *comHandler, iid *guid, out *unsafe.Pointer) uintptr {
	if *iid == iidUnknown || *iid == *h.iid {
		*out = unsafe.Pointer(h)
		handlerAddRef(h)
		return sOK
	}
	*out = nil
	return eNoInterface
}

func handlerAddRef(h *comHandler) uintptr {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	h.refs++
	return uintptr(h.refs)
}

func handlerRelease(h *comHandler) uintptr {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if h.refs--; h.refs == 0 {
		delete(handlers, h)
	}
	return uintptr(h.refs)
}

func handlerInvoke(h *comHandler, hr uintptr, result unsafe.Pointer) uintptr {
	if h.invoke != nil {
		h.invoke(hr, result)
	}
	return sOK
}
