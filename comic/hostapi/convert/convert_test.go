package convert

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"strings"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustBytes(t testing.TB, m Message) []byte {
	t.Helper()
	r, err := Do(m)
	if err != nil {
		t.Fatalf("Do(%s): %v", m.Type, err)
	}
	b, ok := r.([]byte)
	if !ok {
		t.Fatalf("Do(%s) returned %T, want []byte", m.Type, r)
	}
	return b
}

const (
	nistKey = "2b7e151628aed2a6abf7158809cf4f3c"
	nistIV  = "000102030405060708090a0b0c0d0e0f"
	nistPT  = "6bc1bee22e409f96e93d7e117393172a" +
		"ae2d8a571e03ac9c9eb76fac45af8e51" +
		"30c81c46a35ce411e5fbc1191a0a52ef" +
		"f69f2445df4f9b17ad2b417be66c3710"
)

func TestAESNIST(t *testing.T) {
	cases := []struct {
		name, typ string
		blockSize int
		iv        bool
		pt, ct    string
	}{
		{"F.1.1 ECB", "aes-ecb", 0, false, nistPT,
			"3ad77bb40d7a3660a89ecaf32466ef97f5d3d58503b9699de785895a96fdbaaf" +
				"43b1cd7f598ece23881b00e3ed0306887b0c785e27e8ad3f8223207104725dd4"},
		{"F.2.1 CBC", "aes-cbc", 0, true, nistPT,
			"7649abac8119b246cee98e9b12e9197d5086cb9b507219ee95db113a917678b2" +
				"73bed6b8e3c1743b7116e69e222295163ff1caa1681fac09120eca307586e1a7"},
		{"F.3.13 CFB128", "aes-cfb", 16, true, nistPT,
			"3b3fd92eb72dad20333449f8e83cfb4ac8a64537a0b3a93fcde3cdad9f1ce58b" +
				"26751f67a3cbb140b1808cf187a4f4dfc04b05357c5d1c0eeac4c66f9ff7f2e6"},
		{"F.3.7 CFB8", "aes-cfb", 1, true, "6bc1bee22e409f96e93d7e117393172aae2d",
			"3b79424c9c0dd436bace9e0ed4586a4f32b9"},
		{"F.4.1 OFB128", "aes-ofb", 16, true, nistPT,
			"3b3fd92eb72dad20333449f8e83cfb4a7789508d16918f03f53c52dac54ed825" +
				"9740051e9c5fecf64344f7a82260edcc304c6528f659c77866a510d9c1d6ae5e"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Message{Type: c.typ, Key: unhex(t, nistKey), BlockSize: c.blockSize}
			if c.iv {
				m.IV = unhex(t, nistIV)
			}
			m.IsEncode, m.Value = true, unhex(t, c.pt)
			if got := mustBytes(t, m); !bytes.Equal(got, unhex(t, c.ct)) {
				t.Errorf("encrypt = %x, want %s", got, c.ct)
			}
			m.IsEncode, m.Value = false, unhex(t, c.ct)
			if got := mustBytes(t, m); !bytes.Equal(got, unhex(t, c.pt)) {
				t.Errorf("decrypt = %x, want %s", got, c.pt)
			}
		})
	}
}

func TestHashes(t *testing.T) {
	cases := map[string]string{
		"md5":    "900150983cd24fb0d6963f7d28e17f72",
		"sha1":   "a9993e364706816aba3e25717850c26c9cd0d89d",
		"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"sha512": "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a" +
			"2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f",
	}
	for typ, want := range cases {
		got := mustBytes(t, Message{Type: typ, Value: "abc", IsEncode: true})
		if hex.EncodeToString(got) != want {
			t.Errorf("%s(abc) = %x, want %s", typ, got, want)
		}
	}
	if _, err := Do(Message{Type: "md5", Value: "abc"}); err == nil {
		t.Error("md5 decode: want error")
	}
}

func TestHMAC(t *testing.T) {
	key20 := bytes.Repeat([]byte{0x0b}, 20)
	cases := []struct {
		name, hash string
		key, value any
		want       string
	}{
		{"RFC2202 1 sha1", "sha1", key20, "Hi There", "b617318655057264e28bc0b6fb378c8ef146be00"},
		{"RFC2202 2 sha1", "sha1", "Jefe", "what do ya want for nothing?", "effcdf6ae5eb2fa2d27416d5f184df9c259a7c79"},
		{"RFC4231 1 sha256", "sha256", key20, "Hi There", "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"},
		{"RFC4231 2 sha256", "sha256", "Jefe", "what do ya want for nothing?", "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"},
		{"RFC2202 1 md5", "md5", bytes.Repeat([]byte{0x0b}, 16), "Hi There", "9294727a3638bb1c13f48ef8158bfc9d"},
		{"RFC4231 1 sha512", "sha512", key20, "Hi There",
			"87aa7cdea5ef619d4ff0b4241a1d6cb02379f4e2ce4ec2787ad0b30545e17cde" +
				"daa833b7d6b8a702038b274eaea3f4e4be9d914eeb61f1702e696c203a126854"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Message{Type: "hmac", Hash: c.hash, Key: c.key, Value: c.value, IsEncode: true}
			if got := mustBytes(t, m); hex.EncodeToString(got) != c.want {
				t.Errorf("bytes = %x, want %s", got, c.want)
			}
			m.IsString = true
			r, err := Do(m)
			if err != nil || r != c.want {
				t.Errorf("hex = %v, %v; want %s", r, err, c.want)
			}
		})
	}
	if _, err := Do(Message{Type: "hmac", Hash: "sha3", Key: "k", Value: "v", IsEncode: true}); err == nil {
		t.Error("unknown hash: want error")
	}
	if _, err := Do(Message{Type: "hmac", Key: "k", Value: "v", IsEncode: true}); err == nil {
		t.Error("missing hash: want error")
	}
}

func TestBase64(t *testing.T) {
	enc := func(v any) any {
		r, err := Do(Message{Type: "base64", Value: v, IsEncode: true})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := enc("a"); r != "YQ==" {
		t.Errorf("encode a = %v", r)
	}
	if r := enc([]byte{0xfb, 0xff}); r != "+/8=" {
		t.Errorf("encode = %v", r)
	}
	for _, s := range []string{"YQ==", "YQ"} {
		got := mustBytes(t, Message{Type: "base64", Value: s})
		if string(got) != "a" {
			t.Errorf("decode %q = %q", s, got)
		}
	}
	if _, err := Do(Message{Type: "base64", Value: "!!!"}); err == nil {
		t.Error("bad base64: want error")
	}
	if _, err := Do(Message{Type: "base64", Value: 1.5}); err == nil {
		t.Error("non-string value: want error")
	}
}

func TestUTF8(t *testing.T) {
	got := mustBytes(t, Message{Type: "utf8", Value: "中", IsEncode: true})
	if !bytes.Equal(got, []byte{0xe4, 0xb8, 0xad}) {
		t.Errorf("encode = %x", got)
	}
	r, err := Do(Message{Type: "utf8", Value: []byte{0xe4, 0xb8, 0xad, 'a'}})
	if err != nil || r != "中a" {
		t.Errorf("decode = %v, %v", r, err)
	}
	r, _ = Do(Message{Type: "utf8", Value: []byte{'a', 0xff, 'b'}})
	if r != "a\uFFFDb" {
		t.Errorf("invalid utf8 = %q", r)
	}
}

func TestGBK(t *testing.T) {
	enc := mustBytes(t, Message{Type: "gbk", Value: "中文", IsEncode: true})
	if !bytes.Equal(enc, []byte{0xd6, 0xd0, 0xce, 0xc4}) {
		t.Fatalf("encode = %x", enc)
	}
	r, err := Do(Message{Type: "gbk", Value: enc})
	if err != nil || r != "中文" {
		t.Errorf("decode = %v, %v", r, err)
	}
	r, err = Do(Message{Type: "gbk", Value: []any{json.Number("97"), json.Number("255"), json.Number("98")}})
	if err != nil || r != "a\uFFFDb" {
		t.Errorf("invalid byte = %q, %v", r, err)
	}
	r, err = Do(Message{Type: "gbk", Value: []byte{0xd6}})
	if err != nil || r != "\uFFFD" {
		t.Errorf("truncated = %q, %v", r, err)
	}
}

func TestByteInputs(t *testing.T) {
	want := []byte{1, 2, 255}
	ok := []any{
		[]byte{1, 2, 255},
		[]int{1, 2, 255},
		[]any{json.Number("1"), json.Number("2"), json.Number("255")},
		[]any{1, 2, 255},
		[]any{1.0, 2.0, 255.0},
	}
	for _, v := range ok {
		got, err := toBytes(v)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("toBytes(%#v) = %v, %v", v, got, err)
		}
	}
	if got, err := toBytes("é"); err != nil || !bytes.Equal(got, []byte{0xc3, 0xa9}) {
		t.Errorf("string = %v, %v", got, err)
	}
	bad := []any{nil, 3.5, true, map[string]any{}, []int{256}, []int{-1},
		[]any{json.Number("256")}, []any{json.Number("1.5")}, []any{"x"}, []any{nil}, []any{1.5}}
	for _, v := range bad {
		if _, err := toBytes(v); err == nil {
			t.Errorf("toBytes(%#v): want error", v)
		}
	}
	if _, err := Do(Message{Type: "md5", Value: 12.5, IsEncode: true}); err == nil {
		t.Error("bad value type: want error")
	}
}

func TestUnknownType(t *testing.T) {
	r, err := Do(Message{Type: "nope", Value: "same"})
	if err != nil || r != "same" {
		t.Errorf("got %v, %v", r, err)
	}
	r, err = Do(Message{Type: "", Value: nil})
	if err != nil || r != nil {
		t.Errorf("got %v, %v", r, err)
	}
}

func TestRSA(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(der)
	k := priv.Size()
	chunk := k - 11

	for _, n := range []int{1, chunk, chunk + 1, 3*chunk - 5, 3 * chunk} {
		plain := make([]byte, n)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		var ct []byte
		for i := 0; i < n; i += chunk {
			end := min(i+chunk, n)
			c, err := rsa.EncryptPKCS1v15(rand.Reader, &priv.PublicKey, plain[i:end])
			if err != nil {
				t.Fatal(err)
			}
			ct = append(ct, c...)
		}
		got := mustBytes(t, Message{Type: "rsa", Value: ct, Key: key})
		if !bytes.Equal(got, plain) {
			t.Errorf("n=%d: round trip mismatch", n)
		}
	}

	if r, err := Do(Message{Type: "rsa", Value: []byte{1}, Key: key, IsEncode: true}); r != nil || err != nil {
		t.Errorf("encode = %v, %v; want nil, nil", r, err)
	}
	if _, err := Do(Message{Type: "rsa", Value: make([]byte, k-1), Key: key}); err == nil {
		t.Error("unaligned input: want error")
	}
	if _, err := Do(Message{Type: "rsa", Value: make([]byte, k), Key: key}); err == nil {
		t.Error("garbage block: want error")
	}
	if _, err := Do(Message{Type: "rsa", Value: make([]byte, k), Key: "not base64!"}); err == nil {
		t.Error("bad key encoding: want error")
	}
	if _, err := Do(Message{Type: "rsa", Value: make([]byte, k), Key: "AAAA"}); err == nil {
		t.Error("bad key DER: want error")
	}
	if _, err := Do(Message{Type: "rsa", Value: make([]byte, k)}); err == nil {
		t.Error("missing key: want error")
	}
}

func TestAESErrors(t *testing.T) {
	key := unhex(t, nistKey)
	iv := unhex(t, nistIV)
	blk := make([]byte, 16)
	cases := []struct {
		name string
		m    Message
	}{
		{"key length 15", Message{Type: "aes-ecb", Key: key[:15], Value: blk}},
		{"key length 0", Message{Type: "aes-ecb", Key: []byte{}, Value: blk}},
		{"missing key", Message{Type: "aes-ecb", Value: blk}},
		{"ecb unaligned", Message{Type: "aes-ecb", Key: key, Value: blk[:15]}},
		{"cbc unaligned", Message{Type: "aes-cbc", Key: key, IV: iv, Value: make([]byte, 17)}},
		{"cbc missing iv", Message{Type: "aes-cbc", Key: key, Value: blk}},
		{"cfb missing iv", Message{Type: "aes-cfb", Key: key, BlockSize: 16, Value: blk}},
		{"ofb missing iv", Message{Type: "aes-ofb", Key: key, BlockSize: 16, Value: blk}},
		{"short iv", Message{Type: "aes-cbc", Key: key, IV: iv[:8], Value: blk}},
		{"cfb blockSize 0", Message{Type: "aes-cfb", Key: key, IV: iv, Value: blk}},
		{"cfb blockSize 17", Message{Type: "aes-cfb", Key: key, IV: iv, BlockSize: 17, Value: blk}},
		{"ofb blockSize -1", Message{Type: "aes-ofb", Key: key, IV: iv, BlockSize: -1, Value: blk}},
		{"cfb unaligned to blockSize", Message{Type: "aes-cfb", Key: key, IV: iv, BlockSize: 4, Value: make([]byte, 6)}},
		{"ofb unaligned to blockSize", Message{Type: "aes-ofb", Key: key, IV: iv, BlockSize: 16, Value: make([]byte, 8)}},
		{"bad value", Message{Type: "aes-ecb", Key: key, Value: 1.5}},
	}
	for _, c := range cases {
		for _, enc := range []bool{true, false} {
			c.m.IsEncode = enc
			if r, err := Do(c.m); err == nil {
				t.Errorf("%s (encode=%v): want error, got %v", c.name, enc, r)
			}
		}
	}
	// 24 and 32 byte keys work; empty input is allowed.
	for _, n := range []int{24, 32} {
		m := Message{Type: "aes-cbc", Key: make([]byte, n), IV: iv, Value: blk, IsEncode: true}
		if got := mustBytes(t, m); len(got) != 16 {
			t.Errorf("key %d: len %d", n, len(got))
		}
	}
	for _, typ := range []string{"aes-ecb", "aes-cbc", "aes-cfb", "aes-ofb"} {
		got := mustBytes(t, Message{Type: typ, Key: key, IV: iv, BlockSize: 16, Value: []byte{}, IsEncode: true})
		if len(got) != 0 {
			t.Errorf("%s empty: %x", typ, got)
		}
	}
}

func TestAESRoundTrip(t *testing.T) {
	rng := mrand.New(mrand.NewSource(1))
	rnd := func(n int) []byte {
		b := make([]byte, n)
		rng.Read(b)
		return b
	}
	for _, typ := range []string{"aes-ecb", "aes-cbc", "aes-cfb", "aes-ofb"} {
		for _, keyLen := range []int{16, 24, 32} {
			sizes := []int{16}
			if typ == "aes-cfb" || typ == "aes-ofb" {
				sizes = nil
				for s := 1; s <= 16; s++ {
					sizes = append(sizes, s)
				}
			}
			for _, bs := range sizes {
				t.Run(fmt.Sprintf("%s/key%d/bs%d", typ, keyLen, bs), func(t *testing.T) {
					for i := 0; i < 20; i++ {
						m := Message{Type: typ, Key: rnd(keyLen), IV: rnd(16), BlockSize: bs}
						plain := rnd(bs * rng.Intn(20))
						m.IsEncode, m.Value = true, plain
						ct := mustBytes(t, m)
						if len(ct) != len(plain) {
							t.Fatalf("len %d != %d", len(ct), len(plain))
						}
						if len(plain) >= 16 && bytes.Equal(ct, plain) {
							t.Fatal("ciphertext equals plaintext")
						}
						m.IsEncode, m.Value = false, ct
						if got := mustBytes(t, m); !bytes.Equal(got, plain) {
							t.Fatalf("round trip mismatch: %x vs %x", got, plain)
						}
					}
				})
			}
		}
	}
}

// Segment size 16 must agree with the standard library; this guards the
// hand-written segment loops against the crypto/cipher reference.
func TestSegmentModesMatchStdlib(t *testing.T) {
	rng := mrand.New(mrand.NewSource(2))
	key, iv, plain := make([]byte, 16), make([]byte, 16), make([]byte, 160)
	rng.Read(key)
	rng.Read(iv)
	rng.Read(plain)
	for _, typ := range []string{"aes-cfb", "aes-ofb"} {
		got := mustBytes(t, Message{Type: typ, Key: key, IV: iv, BlockSize: 16, Value: plain, IsEncode: true})
		want := stdlib(t, typ, key, iv, plain)
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from crypto/cipher", typ)
		}
	}
}
