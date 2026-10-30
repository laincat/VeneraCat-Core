package js

import (
	"encoding/json"
	"time"

	"github.com/laincat/veneracat-core/comic/hostapi"
	"github.com/laincat/veneracat-core/host"
)

// SourceStorage persists per-source data and settings. Values cross the
// boundary as JSON text so large integers and long decimals stay exact.
type SourceStorage interface {
	LoadData(key, dataKey string) (json.RawMessage, bool, error)
	SaveData(key, dataKey string, data json.RawMessage) error
	DeleteData(key, dataKey string) error
	// LoadSetting returns the stored value of a setting. The declared default
	// is resolved by the runtime from the source's settings object.
	LoadSetting(key, settingKey string) (json.RawMessage, bool, error)
	IsLogged(key string) bool
}

// Environment describes the client to scripts.
type Environment struct {
	Locale     string // e.g. "zh_CN"
	Platform   string // ios | macos | windows | linux | android
	AppVersion string // semantic version compared with minAppVersion
	DefaultUA  string
}

// Services is what the client provides. Each source gets its own HTML and
// image stores, so handles cannot cross sources.
type Services struct {
	Storage SourceStorage
	Env     Environment
	Log     func(level, title, content string)
	// Transport and Verifier serve the http method. When Verifier is set the
	// runtime wraps Transport with Cloudflare handling.
	Transport host.Transport
	Verifier  host.Verifier
	Cookies   hostapi.CookieStore
	UI        UIHandler
	Clipboard Clipboard
}

// Options tune the engine.
type Options struct {
	// MemoryLimit bounds what one runtime's JavaScript keeps and allocates.
	MemoryLimit int64
	// InitScript replaces the embedded init.js (tests only).
	InitScript string
	// CallTimeout bounds one Eval when its context has no deadline.
	CallTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.MemoryLimit <= 0 {
		o.MemoryLimit = 256 << 20
	}
	if o.InitScript == "" {
		o.InitScript = defaultInit
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 60 * time.Second
	}
	return o
}
