package js

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/MoeForge/moejs"
)

// ImageConfig is what comic.onImageLoad / comic.onThumbnailLoad answers: how to
// fetch one image.
//
// Besides the address and headers the official client also honours callbacks —
// onResponse (decrypt or rewrite the body), modifyImage (a pixel-buffer script)
// and onLoadFailed (another mirror to try). A function cannot cross into JSON,
// so when the source provided any of them this type reports only their names
// and a Handle the host can call back into. A host that does not know about
// handles keeps using URL/Method/Headers/Data and behaves exactly as before.
type ImageConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Data    string            `json:"data,omitempty"`
	// Handle names the config inside its source's runtime; empty when the
	// source provided no callback.
	Handle string `json:"handle,omitempty"`
	// Callbacks lists what the handle can do, in the order the official
	// client would use them.
	Callbacks []string `json:"callbacks,omitempty"`
	// ModifyImage is the source's pixel-buffer script, when it supplied one.
	// It is a string of JavaScript, not a function, so it travels here and the
	// host runs it with Runtime.ModifyImage.
	ModifyImage string `json:"modifyImage,omitempty"`
}

// ErrNoImageCallback reports a handle that carries no such callback. A caller
// may read it as "keep the body as it is" rather than as a failure.
var ErrNoImageCallback = errors.New("js: image config has no such callback")

var imageHandlePattern = regexp.MustCompile(`^i[0-9]{1,9}$`)

// imageMaxHandles bounds the callbacks one source keeps alive. A reader asks
// for one config per page, so this is generous; past it the whole set is
// dropped rather than growing without limit, and a dropped handle stops
// answering.
const imageMaxHandles = 512

// imageHelpers decodes and encodes base64 inside the source's own runtime, so
// the body makes one trip across the boundary instead of three.
const imageHelpers = `
function __vcB64d(s){var c='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';` +
	`var o=[],b=0,x=0;for(var i=0;i<s.length;i++){var v=c.indexOf(s[i]);if(v<0)continue;` +
	`b=(b<<6)|v;x+=6;if(x>=8){x-=8;o.push((b>>x)&255)}}return Uint8Array.from(o)}` +
	`function __vcB64e(u){var c='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';var o='';` +
	`for(var i=0;i<u.length;i+=3){var a=u[i],b=u[i+1],d=u[i+2];` +
	`o+=c[a>>2]+c[((a&3)<<4)|((b||0)>>4)]+(b===undefined?'=':c[((b&15)<<2)|((d||0)>>6)])+(d===undefined?'=':c[d&63])}` +
	`return o}` +
	`function __vcBytes(v){if(v instanceof ArrayBuffer)return new Uint8Array(v);` +
	`if(ArrayBuffer.isView(v))return new Uint8Array(v.buffer,v.byteOffset,v.byteLength);return null}`

// imageTake registers a config's callbacks and reduces it to what JSON carries.
// One string, so ImageConfig and ImageFallback always store the same shape.
func imageTake(max int) string {
	return `
function __vcTake(cfg){
  if(!cfg||typeof cfg!=='object')return null;
  var n=(globalThis.__vcImageSeq||0)+1;
  // The counter only ever grows, so a handle that was dropped cannot come
  // back to life as a different config's handle.
  globalThis.__vcImageSeq=n;
  var id='i'+n,cbs=[];
  var box=globalThis.__vcImageCB||(globalThis.__vcImageCB={});
  if((globalThis.__vcImageCount||0)>=` + strconv.Itoa(max) + `){box={};globalThis.__vcImageCB=box;globalThis.__vcImageCount=0}
  ['onResponse','onLoadFailed'].forEach(function(k){
    if(typeof cfg[k]==='function'){box[id+'.'+k]=cfg[k];cbs.push(k)}
  });
  globalThis.__vcImageCount=(globalThis.__vcImageCount||0)+cbs.length;
  return {url:cfg.url?String(cfg.url):'',method:cfg.method?String(cfg.method):'',
    headers:cfg.headers||null,data:(cfg.data===undefined||cfg.data===null)?null:cfg.data,
    handle:cbs.length?id:'',callbacks:cbs,
    modifyImage:typeof cfg.modifyImage==='string'?cfg.modifyImage:''};
}`
}

// runWithArgs evaluates src with `args` available as globalThis.__vcArgs, the
// same way callOn hands arguments to a source function.
func runWithArgs(ctx context.Context, e *engine, name string, args []any, src string) (json.RawMessage, error) {
	argJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("js: arguments: %w", err)
	}
	if args == nil {
		argJSON = []byte("[]")
	}
	return e.run(ctx, func(rt *moejs.Runtime) (moejs.Value, error) {
		v, err := rt.ParseJSON(argJSON)
		if err != nil {
			return moejs.Undefined(), err
		}
		if err := rt.SetGlobal("__vcArgs", v); err != nil {
			return moejs.Undefined(), err
		}
		script, err := moejs.CompileScript(name, src)
		if err != nil {
			return moejs.Undefined(), err
		}
		return rt.RunScript(script)
	})
}

func imageErr(err error) error {
	if err != nil && strings.Contains(err.Error(), errNoFn) {
		return ErrNoImageCallback
	}
	return err
}

// ImageConfig asks a source how to fetch one image.
//
// thumbnail asks comic.onThumbnailLoad instead of comic.onImageLoad, which is
// what a cover uses. A source with neither hook reports an empty URL, so the
// caller fetches the address it already had.
func (r *Runtime) ImageConfig(ctx context.Context, key, url, comicID, chapterID string, thumbnail bool) (ImageConfig, error) {
	e, err := r.engine(key)
	if err != nil {
		return ImageConfig{}, err
	}
	hook := "onImageLoad"
	if thumbnail {
		hook = "onThumbnailLoad"
	}
	var chapter any
	if chapterID != "" {
		chapter = chapterID
	}
	holder := e.kind.registry() + "[" + strconv.Quote(key) + "]"
	src := imageHelpers + imageTake(imageMaxHandles) + `(function(h){
      if(h===undefined||h===null)throw new Error('` + errNoFn + `');
      var c=h.comic;
      if(c===undefined||c===null)throw new Error('` + errNoFn + `');
      var f=c.` + hook + `;
      if(typeof f!=='function')throw new Error('` + errNoFn + `');
      return Promise.resolve(f.apply(c,globalThis.__vcArgs)).then(__vcTake);
    })(` + holder + `)`
	out, err := runWithArgs(ctx, e, "<image "+key+"."+hook+">", []any{url, comicID, chapter}, src)
	if err != nil {
		return ImageConfig{}, imageErr(err)
	}
	return decodeImageConfig(out)
}

// ApplyImageResponse runs the config's onResponse over a fetched body and
// returns what the source wants used instead.
func (r *Runtime) ApplyImageResponse(ctx context.Context, key, handle string, body []byte) ([]byte, error) {
	if !imageHandlePattern.MatchString(handle) {
		return nil, fmt.Errorf("js: bad image handle %q", handle)
	}
	e, err := r.engine(key)
	if err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(body)
	src := imageHelpers + `(function(){
      var box=globalThis.__vcImageCB||{};
      var f=box['` + handle + `.onResponse'];
      if(typeof f!=='function')throw new Error('` + errNoFn + `');
      var input=__vcB64d('` + enc + `');
      return Promise.resolve(f(input.buffer)).then(function(out){
        var u=__vcBytes(out);
        if(!u)throw new Error('image onResponse did not return bytes');
        return __vcB64e(u);
      });
    })()`
	out, err := evalOn(ctx, e, src)
	if err != nil {
		return nil, imageErr(err)
	}
	var text string
	if err := json.Unmarshal(out, &text); err != nil {
		return nil, fmt.Errorf("js: image onResponse result: %w", err)
	}
	got, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("js: image onResponse result: %w", err)
	}
	return got, nil
}

// ImageFallback runs the config's onLoadFailed and returns the next config the
// source wants tried, callbacks included.
func (r *Runtime) ImageFallback(ctx context.Context, key, handle string) (ImageConfig, error) {
	if !imageHandlePattern.MatchString(handle) {
		return ImageConfig{}, fmt.Errorf("js: bad image handle %q", handle)
	}
	e, err := r.engine(key)
	if err != nil {
		return ImageConfig{}, err
	}
	src := imageHelpers + imageTake(imageMaxHandles) + `(function(){
      var box=globalThis.__vcImageCB||{};
      var f=box['` + handle + `.onLoadFailed'];
      if(typeof f!=='function')throw new Error('` + errNoFn + `');
      return Promise.resolve(f()).then(__vcTake);
    })()`
	out, err := evalOn(ctx, e, src)
	if err != nil {
		return ImageConfig{}, imageErr(err)
	}
	return decodeImageConfig(out)
}

func decodeImageConfig(raw json.RawMessage) (ImageConfig, error) {
	if string(raw) == "null" || len(raw) == 0 {
		return ImageConfig{}, nil
	}
	var cfg ImageConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ImageConfig{}, fmt.Errorf("js: image config: %w", err)
	}
	return cfg, nil
}
