// Package convert implements the "convert" sendMessage method of the legacy
// ComicSource API (docs/SENDMESSAGE-SPEC.md section 1.5) as a pure function.
package convert

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Message is the decoded argument dictionary of a convert call.
type Message struct {
	Type      string
	Value     any
	Key       any
	IV        any
	Hash      string
	BlockSize int
	IsEncode  bool
	IsString  bool
}

// Do runs the conversion. The result is a string, a []byte, or nil.
// An unknown Type returns m.Value unchanged. Any error means the caller
// should log it and hand null to the script.
func Do(m Message) (any, error) {
	switch m.Type {
	case "utf8":
		b, err := toBytes(m.Value)
		if err != nil {
			return nil, err
		}
		if m.IsEncode {
			return b, nil
		}
		return decodeUTF8(b), nil
	case "gbk":
		return doGBK(m)
	case "base64":
		return doBase64(m)
	case "md5", "sha1", "sha256", "sha512":
		if !m.IsEncode {
			return nil, fmt.Errorf("convert: %s has no decode direction", m.Type)
		}
		b, err := toBytes(m.Value)
		if err != nil {
			return nil, err
		}
		h, err := newHash(m.Type)
		if err != nil {
			return nil, err
		}
		hh := h()
		hh.Write(b)
		return hh.Sum(nil), nil
	case "hmac":
		return doHMAC(m)
	case "aes-ecb", "aes-cbc", "aes-cfb", "aes-ofb":
		return doAES(m)
	case "rsa":
		return doRSA(m)
	default:
		return m.Value, nil
	}
}

func decodeUTF8(b []byte) string {
	// One U+FFFD per invalid byte.
	return string([]rune(string(b)))
}

func doGBK(m Message) (any, error) {
	if m.IsEncode {
		s, err := toString(m.Value)
		if err != nil {
			return nil, err
		}
		out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
		if err != nil {
			return nil, fmt.Errorf("convert: gbk encode: %w", err)
		}
		return out, nil
	}
	b, err := toBytes(m.Value)
	if err != nil {
		return nil, err
	}
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return nil, fmt.Errorf("convert: gbk decode: %w", err)
	}
	return string(out), nil
}

func doBase64(m Message) (any, error) {
	if m.IsEncode {
		b, err := toBytes(m.Value)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString(b), nil
	}
	s, err := toString(m.Value)
	if err != nil {
		return nil, err
	}
	return decodeBase64(s)
}

// trimBase64Strip removes what a server can add around a base64 body and no
// base64 alphabet contains: a UTF-8 BOM and ASCII whitespace. It cannot change
// the meaning of valid input, and it turns a transport artefact into a working
// decode instead of an error the source cannot act on.
//
// One real case: a source's domain list came back with a BOM in front of the
// base64, which fails at byte 0 and leaves the source without its fallback list.
func trimBase64Strip(s string) string {
	s = strings.TrimPrefix(s, utf8BOM)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', ' ':
			return -1
		}
		return r
	}, s)
}

// utf8BOM is the byte order mark some servers put in front of a text body.
const utf8BOM = "\xef\xbb\xbf"

func decodeBase64(s string) ([]byte, error) {
	s = trimBase64Strip(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("convert: base64 decode: %w", err)
	}
	return b, nil
}

func newHash(name string) (func() hash.Hash, error) {
	switch name {
	case "md5":
		return md5.New, nil
	case "sha1":
		return sha1.New, nil
	case "sha256":
		return sha256.New, nil
	case "sha512":
		return sha512.New, nil
	}
	return nil, fmt.Errorf("convert: unknown hash %q", name)
}

func doHMAC(m Message) (any, error) {
	if !m.IsEncode {
		return nil, errors.New("convert: hmac has no decode direction")
	}
	h, err := newHash(m.Hash)
	if err != nil {
		return nil, err
	}
	key, err := toBytes(m.Key)
	if err != nil {
		return nil, fmt.Errorf("convert: hmac key: %w", err)
	}
	v, err := toBytes(m.Value)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(h, key)
	mac.Write(v)
	sum := mac.Sum(nil)
	if m.IsString {
		return hex.EncodeToString(sum), nil
	}
	return sum, nil
}

func doAES(m Message) (any, error) {
	v, err := toBytes(m.Value)
	if err != nil {
		return nil, err
	}
	key, err := toBytes(m.Key)
	if err != nil {
		return nil, fmt.Errorf("convert: aes key: %w", err)
	}
	if n := len(key); n != 16 && n != 24 && n != 32 {
		return nil, fmt.Errorf("convert: aes key length %d, want 16, 24 or 32", n)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("convert: aes: %w", err)
	}
	const bs = aes.BlockSize

	seg := bs
	if m.Type == "aes-cfb" || m.Type == "aes-ofb" {
		if m.BlockSize < 1 || m.BlockSize > bs {
			return nil, fmt.Errorf("convert: blockSize %d, want 1..16", m.BlockSize)
		}
		seg = m.BlockSize
	}
	if len(v)%seg != 0 {
		return nil, fmt.Errorf("convert: input length %d is not a multiple of %d", len(v), seg)
	}

	var iv []byte
	if m.Type != "aes-ecb" {
		if m.IV == nil {
			return nil, fmt.Errorf("convert: %s requires iv", m.Type)
		}
		iv, err = toBytes(m.IV)
		if err != nil {
			return nil, fmt.Errorf("convert: aes iv: %w", err)
		}
		if len(iv) != bs {
			return nil, fmt.Errorf("convert: aes iv length %d, want 16", len(iv))
		}
	}

	out := make([]byte, len(v))
	switch m.Type {
	case "aes-ecb":
		for i := 0; i < len(v); i += bs {
			if m.IsEncode {
				blk.Encrypt(out[i:i+bs], v[i:i+bs])
			} else {
				blk.Decrypt(out[i:i+bs], v[i:i+bs])
			}
		}
	case "aes-cbc":
		if len(v) == 0 {
			break
		}
		if m.IsEncode {
			cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out, v)
		} else {
			cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, v)
		}
	case "aes-cfb":
		segmentCFB(blk, iv, seg, v, out, m.IsEncode)
	case "aes-ofb":
		segmentOFB(blk, iv, seg, v, out)
	}
	return out, nil
}

// segmentCFB implements CFB with a segment size of seg bytes. The shift
// register moves left by seg bytes and takes in the ciphertext segment.
func segmentCFB(blk cipher.Block, iv []byte, seg int, in, out []byte, encrypt bool) {
	reg := bytes.Clone(iv)
	ks := make([]byte, len(reg))
	for i := 0; i < len(in); i += seg {
		blk.Encrypt(ks, reg)
		for j := 0; j < seg; j++ {
			out[i+j] = in[i+j] ^ ks[j]
		}
		ct := out[i : i+seg]
		if !encrypt {
			ct = in[i : i+seg]
		}
		copy(reg, reg[seg:])
		copy(reg[len(reg)-seg:], ct)
	}
}

// segmentOFB implements OFB with a segment size of seg bytes. The shift
// register moves left by seg bytes and takes in the keystream segment.
func segmentOFB(blk cipher.Block, iv []byte, seg int, in, out []byte) {
	reg := bytes.Clone(iv)
	ks := make([]byte, len(reg))
	for i := 0; i < len(in); i += seg {
		blk.Encrypt(ks, reg)
		for j := 0; j < seg; j++ {
			out[i+j] = in[i+j] ^ ks[j]
		}
		copy(reg, reg[seg:])
		copy(reg[len(reg)-seg:], ks[:seg])
	}
}

func doRSA(m Message) (any, error) {
	if m.IsEncode {
		return nil, nil
	}
	v, err := toBytes(m.Value)
	if err != nil {
		return nil, err
	}
	ks, err := toString(m.Key)
	if err != nil {
		return nil, fmt.Errorf("convert: rsa key: %w", err)
	}
	der, err := decodeBase64(strings.TrimSpace(ks))
	if err != nil {
		return nil, fmt.Errorf("convert: rsa key: %w", err)
	}
	pk, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("convert: rsa key: %w", err)
	}
	priv, ok := pk.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("convert: rsa key is not an RSA private key")
	}
	k := priv.Size()
	if len(v)%k != 0 {
		return nil, fmt.Errorf("convert: rsa input length %d is not a multiple of %d", len(v), k)
	}
	out := []byte{}
	for i := 0; i < len(v); i += k {
		p, err := rsa.DecryptPKCS1v15(nil, priv, v[i:i+k])
		if err != nil {
			return nil, fmt.Errorf("convert: rsa decrypt: %w", err)
		}
		out = append(out, p...)
	}
	return out, nil
}

func toString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	}
	return "", fmt.Errorf("convert: want string, got %T", v)
}

// toBytes accepts []byte, string (UTF-8 bytes), []any or []int of integers
// 0..255, and a single json.Number (one byte).
func toBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	case []int:
		out := make([]byte, len(x))
		for i, n := range x {
			if n < 0 || n > 255 {
				return nil, fmt.Errorf("convert: byte value %d out of range", n)
			}
			out[i] = byte(n)
		}
		return out, nil
	case []any:
		out := make([]byte, len(x))
		for i, e := range x {
			b, err := elemByte(e)
			if err != nil {
				return nil, err
			}
			out[i] = b
		}
		return out, nil
	case json.Number:
		b, err := elemByte(x)
		if err != nil {
			return nil, err
		}
		return []byte{b}, nil
	}
	return nil, fmt.Errorf("convert: cannot use %T as bytes", v)
}

func elemByte(e any) (byte, error) {
	var n int64
	switch x := e.(type) {
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			f, ferr := x.Float64()
			if ferr != nil || f != math.Trunc(f) {
				return 0, fmt.Errorf("convert: %q is not an integer byte", x.String())
			}
			i = int64(f)
		}
		n = i
	case int:
		n = int64(x)
	case int64:
		n = x
	case int32:
		n = int64(x)
	case uint8:
		n = int64(x)
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("convert: %v is not an integer byte", x)
		}
		if x < 0 || x > 255 {
			return 0, fmt.Errorf("convert: byte value %v out of range", x)
		}
		n = int64(x)
	default:
		return 0, fmt.Errorf("convert: cannot use %T as byte", e)
	}
	if n < 0 || n > 255 {
		return 0, fmt.Errorf("convert: byte value %d out of range", n)
	}
	return byte(n), nil
}
