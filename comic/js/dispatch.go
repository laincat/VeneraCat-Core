package js

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/MoeForge/moejs"
	"github.com/laincat/veneracat-core/comic/hostapi/convert"
	"github.com/laincat/veneracat-core/comic/html"
	"github.com/laincat/veneracat-core/comic/image"
)

// sendMessage is the host function scripts call. Every message is handled
// for this engine's own source: a "key" field in the message is ignored. It
// recovers panics so a bug in a host method becomes a thrown Error instead
// of killing the loop.
func (e *engine) sendMessage(realm *moejs.Realm, _ moejs.Value, args []moejs.Value) (res moejs.Value, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = moejs.Undefined(), fmt.Errorf("host panic: %v", p)
		}
	}()
	raw, err := realm.ToGoStrict(moejs.Arg(args, 0))
	if err != nil {
		return moejs.Undefined(), err
	}
	msg, ok := raw.(map[string]any)
	if !ok {
		return moejs.Undefined(), errors.New("sendMessage expects an object")
	}
	method := str(msg["method"])
	if hook := testHook; hook != nil {
		hook(method)
	}
	svc := &e.mgr.svc
	switch method {
	case "uuid":
		return moejs.String(uuid4()), nil
	case "random":
		return random(msg), nil
	case "log":
		svc.Log(str(msg["level"]), str(msg["title"]), str(msg["content"]))
		return moejs.Undefined(), nil
	case "convert":
		return e.convert(realm, msg)
	case "html":
		return e.htmlMsg(realm, msg)
	case "image":
		return e.imageMsg(realm, msg)
	case "load_data", "save_data", "delete_data", "load_setting", "isLogged":
		return e.storageMsg(method, msg, moejs.Arg(args, 0))
	case "getLocale":
		return moejs.String(svc.Env.Locale), nil
	case "getPlatform":
		return moejs.String(svc.Env.Platform), nil
	case "delay":
		return e.delay(msg)
	case "http":
		return e.http(msg)
	case "cookie":
		return e.cookie(realm, msg)
	case "compute":
		return e.compute(msg)
	case "UI":
		return e.ui(msg)
	case "setClipboard", "getClipboard":
		return e.clipboard(method, msg)
	}
	return moejs.Undefined(), fmt.Errorf("No JavaScript message handler provided for method: %s", method)
}

// testHook lets tests observe or break dispatch. Nil in production.
var testHook func(method string)

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func random(msg map[string]any) moejs.Value {
	lo, hi := num(msg["min"], 0), num(msg["max"], 1)
	var b [8]byte
	_, _ = rand.Read(b[:])
	f := float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
	v := lo + (hi-lo)*f
	if str(msg["type"]) == "double" {
		return moejs.Number(v)
	}
	return moejs.Number(math.Trunc(v))
}

func (e *engine) convert(realm *moejs.Realm, msg map[string]any) (moejs.Value, error) {
	m := convert.Message{
		Type:      str(msg["type"]),
		Value:     msg["value"],
		Key:       msg["key"],
		IV:        msg["iv"],
		Hash:      str(msg["hash"]),
		BlockSize: int(num(msg["blockSize"], 0)),
		IsEncode:  msg["isEncode"] == true,
		IsString:  msg["isString"] == true,
	}
	out, err := convert.Do(m)
	if err != nil {
		// Official behaviour: conversion failures are logged and yield null.
		e.mgr.svc.Log("error", "JS Engine", "Failed to convert "+m.Type+": "+err.Error())
		return moejs.Null(), nil
	}
	return toValue(realm, out)
}

func (e *engine) htmlMsg(realm *moejs.Realm, msg map[string]any) (moejs.Value, error) {
	out, err := e.html.Do(html.Message{
		Function: str(msg["function"]),
		Key:      int64(num(msg["key"], 0)),
		Doc:      int64(num(msg["doc"], 0)),
		Query:    str(msg["query"]),
		ID:       str(msg["id"]),
		Data:     str(msg["data"]),
	})
	if err != nil {
		return moejs.Undefined(), err
	}
	return toValue(realm, out)
}

func (e *engine) imageMsg(realm *moejs.Realm, msg map[string]any) (moejs.Value, error) {
	i := func(k string) int { return int(num(msg[k], 0)) }
	out, err := e.image.Do(image.Message{
		Function: str(msg["function"]),
		Key:      i("key"), Image: i("image"),
		X: i("x"), Y: i("y"), Width: i("width"), Height: i("height"),
		SrcX: i("srcX"), SrcY: i("srcY"),
	})
	if err != nil {
		return moejs.Undefined(), err
	}
	return toValue(realm, out)
}

// storageMsg serves the storage messages for the engine's own key. Engines
// without a source (the global engine and a source being loaded) have no
// storage: reads return null and writes are kept in memory only, so a script
// cannot touch installed sources' data before it is registered.
func (e *engine) storageMsg(method string, msg map[string]any, arg moejs.Value) (moejs.Value, error) {
	st := e.storage
	if st == nil {
		if method == "isLogged" {
			return moejs.Bool(false), nil
		}
		return moejs.Undefined(), errors.New("source storage is not available")
	}
	key := e.key
	switch method {
	case "isLogged":
		return moejs.Bool(st.IsLogged(key)), nil
	case "load_data":
		raw, ok, err := st.LoadData(key, str(msg["data_key"]))
		if err != nil || !ok {
			return moejs.Null(), err
		}
		return e.rt.ParseJSON(raw)
	case "save_data":
		dk := str(msg["data_key"])
		if dk == "setting" {
			return moejs.Undefined(), errors.New("setting is not allowed to be saved")
		}
		// Serialise the JavaScript value itself: it keeps the script's key
		// order and writes BigInts as exact integers. A Go map would sort
		// the keys.
		raw := json.RawMessage("null")
		if data, err := e.rt.Get(arg, "data"); err == nil && !data.IsUndefined() {
			if raw, err = toJSON(e.rt, data); err != nil {
				return moejs.Undefined(), err
			}
		}
		return moejs.Undefined(), st.SaveData(key, dk, raw)
	case "delete_data":
		return moejs.Undefined(), st.DeleteData(key, str(msg["data_key"]))
	case "load_setting":
		sk := str(msg["setting_key"])
		raw, ok, err := st.LoadSetting(key, sk)
		if err != nil {
			return moejs.Undefined(), err
		}
		if ok {
			return e.rt.ParseJSON(raw)
		}
		if v, found := e.settingDefault(sk); found {
			return v, nil
		}
		return moejs.Undefined(), fmt.Errorf("Setting not found: %s", sk)
	}
	return moejs.Undefined(), nil
}

// settingDefault reads the declared default of a setting from the source
// object registered in this engine (or the instance being loaded).
func (e *engine) settingDefault(sk string) (moejs.Value, bool) {
	src := "(function(s){var o=globalThis.__vcPending||" + e.kind.registry() + "[" + strconv.Quote(e.key) + "];" +
		"if(!o)return undefined;var st=o.settings;if(!st||!st[s])return undefined;return st[s]['default'];})(" +
		strconv.Quote(sk) + ")"
	script, err := moejs.CompileScript("<setting>", src)
	if err != nil {
		return moejs.Undefined(), false
	}
	v, err := e.rt.RunScript(script)
	if err != nil || v.IsUndefined() {
		return moejs.Undefined(), false
	}
	return v, true
}

// toValue converts host results. []byte becomes an ArrayBuffer.
func toValue(realm *moejs.Realm, v any) (moejs.Value, error) {
	switch x := v.(type) {
	case nil:
		return moejs.Null(), nil
	case json.RawMessage:
		return moejs.Undefined(), errors.New("internal: raw JSON must be parsed on the runtime")
	case []int:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return realm.FromGo(out)
	}
	return realm.FromGo(v)
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

func num(v any, def float64) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case float64:
		return x
	case json.Number:
		f, err := x.Float64()
		if err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f
		}
	}
	return def
}
