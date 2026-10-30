package hls

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

func TestResolve(t *testing.T) {
	const base = "https://cdn.example.com/a/b/index.m3u8?token=abc#frag"
	tests := []struct{ name, base, ref, want string }{
		{"same dir", base, "seg1.ts", "https://cdn.example.com/a/b/seg1.ts"},
		{"dot slash", base, "./seg1.ts", "https://cdn.example.com/a/b/seg1.ts"},
		{"parent", base, "../seg1.ts", "https://cdn.example.com/a/seg1.ts"},
		{"two parents", base, "../../x/seg1.ts", "https://cdn.example.com/x/seg1.ts"},
		{"parent above root", base, "../../../../seg1.ts", "https://cdn.example.com/seg1.ts"},
		{"root relative", base, "/root/seg.ts", "https://cdn.example.com/root/seg.ts"},
		{"root relative with dots", base, "/r/../s/./seg.ts", "https://cdn.example.com/s/seg.ts"},
		{"absolute", base, "http://other.example.org/p/q.ts", "http://other.example.org/p/q.ts"},
		{"absolute with dots", base, "http://o.example.org/p/../q.ts", "http://o.example.org/q.ts"},
		{"protocol relative", base, "//img.example.net/x/y.ts", "https://img.example.net/x/y.ts"},
		{"protocol relative http base", "http://a.example/p/i.m3u8", "//b.example/y.ts", "http://b.example/y.ts"},
		{"query kept", base, "seg1.ts?sig=a%2Fb&x=1", "https://cdn.example.com/a/b/seg1.ts?sig=a%2Fb&x=1"},
		{"query only", base, "?page=2", "https://cdn.example.com/a/b/index.m3u8?page=2"},
		{"empty ref keeps base query", base, "", "https://cdn.example.com/a/b/index.m3u8?token=abc"},
		{"fragment only", base, "#x", "https://cdn.example.com/a/b/index.m3u8?token=abc#x"},
		{"base query not inherited", base, "seg.ts", "https://cdn.example.com/a/b/seg.ts"},
		{"base without path", "https://h.example", "seg.ts", "https://h.example/seg.ts"},
		{"base with port and userinfo", "https://u:p@h.example:8443/d/i.m3u8", "s.ts", "https://u:p@h.example:8443/d/s.ts"},
		{"trailing slash base", "https://h.example/dir/", "s.ts", "https://h.example/dir/s.ts"},
		{"dot only", base, ".", "https://cdn.example.com/a/b/"},
		{"dotdot only", base, "..", "https://cdn.example.com/a/"},
		{"whitespace trimmed", base, "  seg.ts \t", "https://cdn.example.com/a/b/seg.ts"},
		{"colon in later segment is not a scheme", base, "a/b:c.ts", "https://cdn.example.com/a/b/a/b:c.ts"},
		{"scheme-like with digits", base, "data:text/plain;base64,QQ==", "data:text/plain;base64,QQ=="},
		{"absolute needs no base", "", "https://x.example/a/../b.ts", "https://x.example/b.ts"},
		{"RFC 3986 g;x?y#s", "http://a/b/c/d;p?q", "g;x?y#s", "http://a/b/c/g;x?y#s"},
		{"RFC 3986 ../../../g", "http://a/b/c/d;p?q", "../../../g", "http://a/g"},
		{"RFC 3986 /./g", "http://a/b/c/d;p?q", "/./g", "http://a/g"},
		{"RFC 3986 g.", "http://a/b/c/d;p?q", "g.", "http://a/b/c/g."},
		{"RFC 3986 ..g", "http://a/b/c/d;p?q", "..g", "http://a/b/c/..g"},
		{"RFC 3986 g/./h", "http://a/b/c/d;p?q", "g/./h", "http://a/b/c/g/h"},
		{"RFC 3986 g?y/./x", "http://a/b/c/d;p?q", "g?y/./x", "http://a/b/c/g?y/./x"},
		{"RFC 3986 ./g/.", "http://a/b/c/d;p?q", "./g/.", "http://a/b/c/g/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.base, tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q", tc.base, tc.ref, got, tc.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	for _, base := range []string{"", "/just/a/path", "relative/path"} {
		if _, err := Resolve(base, "seg.ts"); err != ErrBaseNotAbsolute {
			t.Errorf("Resolve(%q) error = %v", base, err)
		}
	}
}

// buildTS returns n 188-byte packets that all start with the sync byte.
func buildTS(n int) []byte {
	b := make([]byte, 0, n*188)
	for i := 0; i < n; i++ {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = byte(i)
		for j := 4; j < 188; j++ {
			pkt[j] = byte(j + i)
		}
		b = append(b, pkt...)
	}
	return b
}

func TestStripSegmentPrefix(t *testing.T) {
	ts := buildTS(8)
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0x47, 0x00}, 40)...) // includes stray 0x47 bytes
	png = append(png, []byte("IEND\xaeB`\x82")...)

	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"fake PNG prefix", append(append([]byte{}, png...), ts...), ts},
		{"one junk byte", append([]byte{0x00}, ts...), ts},
		{"already clean", ts, ts},
		{"prefix is only 0x47 look-alikes", append(bytes.Repeat([]byte{0x47}, 300), ts...), tsFrom(bytes.Repeat([]byte{0x47}, 300), ts)},
		{"no valid sync", bytes.Repeat([]byte("abc"), 1000), bytes.Repeat([]byte("abc"), 1000)},
		{"only four packets", append([]byte("junk"), buildTS(4)...), append([]byte("junk"), buildTS(4)...)},
		{"five packets is enough", append([]byte("junk"), buildTS(5)...), buildTS(5)},
		{"empty", nil, nil},
		{"short", []byte{0x47}, []byte{0x47}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StripSegmentPrefix(tc.in)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("len(got)=%d len(want)=%d, first bytes got % x", len(got), len(tc.want), got[:min(8, len(got))])
			}
		})
	}
}

// tsFrom computes the expected result for a prefix made only of 0x47 bytes:
// the first p whose p+188k are all 0x47 lies inside the prefix, so the
// expected output starts there.
func tsFrom(prefix, ts []byte) []byte {
	all := append(append([]byte{}, prefix...), ts...)
	for p := 0; p < len(all); p++ {
		ok := p+188*4 < len(all)
		for k := 0; k < 5 && ok; k++ {
			ok = all[p+188*k] == 0x47
		}
		if ok {
			return all[p:]
		}
	}
	return all
}

func TestSegmentIV(t *testing.T) {
	tests := []struct {
		name     string
		seq      int64
		idx      int
		explicit string
		want     string
		wantErr  bool
	}{
		{"zero", 0, 0, "", "00000000000000000000000000000000", false},
		{"sequence plus index", 7, 3, "", "0000000000000000000000000000000a", false},
		{"big endian", 0x0102030405, 0, "", "00000000000000000000000102030405", false},
		{"max int64", 1<<63 - 1, 0, "", "00000000000000007fffffffffffffff", false},
		{"explicit full", 99, 99, "0x000102030405060708090a0b0c0d0e0f", "000102030405060708090a0b0c0d0e0f", false},
		{"explicit upper case prefix", 0, 0, "0XFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", "ffffffffffffffffffffffffffffffff", false},
		{"explicit short is left padded", 0, 0, "0x1", "00000000000000000000000000000001", false},
		{"explicit without prefix", 0, 0, "ab", "000000000000000000000000000000ab", false},
		{"explicit too long", 0, 0, "0x" + "00" + "000102030405060708090a0b0c0d0e0f", "", true},
		{"explicit not hex", 0, 0, "0xZZ", "", true},
		{"explicit prefix only", 0, 0, "0x", "", true},
		{"negative sequence", -1, 0, "", "", true},
		{"negative index", 0, -1, "", "", true},
		{"overflow", 1<<63 - 1, 1, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SegmentIV(tc.seq, tc.idx, tc.explicit)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && hex.EncodeToString(got) != tc.want {
				t.Errorf("IV = %x, want %s", got, tc.want)
			}
		})
	}
}

func pkcs7Encrypt(t *testing.T, plain, key, iv []byte) []byte {
	t.Helper()
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(buf, buf)
	return buf
}

func TestDecryptAES128(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv, err := SegmentIV(42, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 15, 16, 17, 188 * 10} {
		plain := buildTS(10)[:n]
		enc := pkcs7Encrypt(t, plain, key, iv)
		orig := append([]byte{}, enc...)
		got, err := DecryptAES128(enc, key, iv)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Errorf("n=%d: plaintext mismatch", n)
		}
		if !bytes.Equal(enc, orig) {
			t.Errorf("n=%d: input was modified", n)
		}
	}

	// Known vector: NIST SP 800-38A F.2.1 first block (CBC-AES128), no padding
	// block follows so we build the padded ciphertext ourselves and check the
	// first block of ciphertext equals the NIST value.
	nistKey, _ := hex.DecodeString("2b7e151628aed2a6abf7158809cf4f3c")
	nistIV, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	nistPlain, _ := hex.DecodeString("6bc1bee22e409f96e93d7e117393172a")
	enc := pkcs7Encrypt(t, nistPlain, nistKey, nistIV)
	if got := hex.EncodeToString(enc[:16]); got != "7649abac8119b246cee98e9b12e9197d" {
		t.Errorf("NIST first ciphertext block = %s", got)
	}
	if got, err := DecryptAES128(enc, nistKey, nistIV); err != nil || !bytes.Equal(got, nistPlain) {
		t.Errorf("NIST decrypt = %x, %v", got, err)
	}
}

func TestDecryptAES128Errors(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 16)
	iv := bytes.Repeat([]byte{2}, 16)
	good := pkcs7Encrypt(t, []byte("hello"), key, iv)
	badPad := append([]byte{}, good...)
	badPad[len(badPad)-1] ^= 0xff // corrupts the last plaintext byte after CBC

	tests := []struct {
		name         string
		seg, key, iv []byte
	}{
		{"short key", good, key[:15], iv},
		{"long key", good, append(key, 0), iv},
		{"short iv", good, key, iv[:8]},
		{"empty", nil, key, iv},
		{"not block aligned", good[:15], key, iv},
		{"wrong key", good, bytes.Repeat([]byte{9}, 16), iv},
		{"corrupt padding", badPad, key, iv},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := DecryptAES128(tc.seg, tc.key, tc.iv); err == nil {
				t.Errorf("expected an error, got %q", got)
			}
		})
	}
}
