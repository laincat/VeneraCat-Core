package anibaka

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestCryptoOperations(t *testing.T) {
	// MD5 & SHA256.
	hMD5 := md5.Sum([]byte("veneracat"))
	got, err := runCrypto("md5", "veneracat", "", "", "", "", "", "hex", "")
	if err != nil || got != hex.EncodeToString(hMD5[:]) {
		t.Fatalf("md5 = %q err=%v", got, err)
	}
	h256 := sha256.Sum256([]byte("veneracat"))
	got, err = runCrypto("sha256", "veneracat", "", "", "", "", "", "hex", "")
	if err != nil || got != hex.EncodeToString(h256[:]) {
		t.Fatalf("sha256 = %q err=%v", got, err)
	}

	// Base64 & Hex round-trip.
	b64, err := runCrypto("base64-encode", "hello/world+1", "", "", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := runCrypto("base64-decode", b64, "", "", "", "", "", "", "")
	if err != nil || dec != "hello/world+1" {
		t.Fatalf("base64 round-trip = %q err=%v", dec, err)
	}
	hx, err := runCrypto("hex-encode", "abc", "", "", "", "", "", "", "")
	if err != nil || hx != "616263" {
		t.Fatalf("hex-encode = %q err=%v", hx, err)
	}
	hxDec, err := runCrypto("hex-decode", hx, "", "", "", "", "", "", "")
	if err != nil || hxDec != "abc" {
		t.Fatalf("hex-decode = %q err=%v", hxDec, err)
	}

	// AES-ECB encrypt -> decrypt.
	key := "0123456789abcdef"
	encECB, err := runCrypto("aes-ecb-encrypt", "secret-video-url", key, "", "", "", "", "base64", "pkcs7")
	if err != nil {
		t.Fatal(err)
	}
	decECB, err := runCrypto("aes-ecb-decrypt", encECB, key, "", "base64", "", "", "utf8", "pkcs7")
	if err != nil || decECB != "secret-video-url" {
		t.Fatalf("AES-ECB = %q err=%v", decECB, err)
	}

	// AES-CTR encrypt -> decrypt.
	iv := "fedcba9876543210"
	encCTR, err := runCrypto("aes-ctr-encrypt", "stream-token", key, iv, "", "", "", "hex", "")
	if err != nil {
		t.Fatal(err)
	}
	decCTR, err := runCrypto("aes-ctr-decrypt", encCTR, key, iv, "hex", "", "", "utf8", "")
	if err != nil || decCTR != "stream-token" {
		t.Fatalf("AES-CTR = %q err=%v", decCTR, err)
	}

	// AES-GCM decrypt.
	block, _ := aes.NewCipher([]byte(key))
	aead, _ := cipher.NewGCM(block)
	nonce := []byte("123456789012")
	sealed := aead.Seal(nil, nonce, []byte("gcm-payload"), nil)
	decGCM, err := runCrypto("aes-gcm-decrypt", base64.StdEncoding.EncodeToString(sealed), key, string(nonce), "base64", "utf8", "utf8", "utf8", "")
	if err != nil || decGCM != "gcm-payload" {
		t.Fatalf("AES-GCM = %q err=%v", decGCM, err)
	}

	// XOR round-trip.
	x1, err := runCrypto("xor", "hello", "k!", "", "utf8", "utf8", "", "hex", "")
	if err != nil {
		t.Fatal(err)
	}
	x2, err := runCrypto("xor", x1, "k!", "", "hex", "utf8", "", "utf8", "")
	if err != nil || x2 != "hello" {
		t.Fatalf("XOR = %q err=%v", x2, err)
	}
}

func TestBranchFilterGBKAndURLValidation(t *testing.T) {
	gbkBytes, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(`<html><body><h1>测试标题</h1><div class="url">/media/ok.m3u8</div></body></html>`))
	if err != nil {
		t.Fatal(err)
	}

	ruleJSON := `{
		"$schema": "anx-rule/2",
		"key": "anx_ops",
		"name": "Ops",
		"version": "1",
		"baseUrl": "https://ops.example.com",
		"search": {
			"steps": [
				{"op": "const", "value": "a,b,a,skip_me,c"},
				{"op": "split", "separator": ","},
				{"op": "dedupe"},
				{"op": "filter", "condition": "notContains", "target": "skip"},
				{"op": "reverse"},
				{"op": "join", "separator": "|", "to": "next"},
				{"op": "const", "value": [], "to": "items"}
			]
		},
		"detail": {
			"steps": [
				{"op": "fetch", "url": "/gbk", "charset": "gbk", "to": "doc"},
				{"op": "css", "selector": "h1", "to": "title"},
				{"op": "css", "from": "doc", "selector": ".url", "to": "ep"},
				{"op": "branch", "from": "ep", "condition": "startsWith", "target": "/media",
					"then": [{"op": "urlResolve", "from": "ep", "to": "ep"}],
					"else": [{"op": "const", "value": "", "to": "ep"}]
				},
				{"op": "const", "value": [{"id": "{{ep}}", "title": "E1"}], "to": "lines"}
			]
		},
		"play": {
			"steps": [
				{"op": "fetch", "url": "{{episodeId}}"},
				{"op": "const", "value": "https://ops.example.com/v.mp4", "to": "videoUrl"}
			]
		}
	}`
	rule, err := Parse([]byte(ruleJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr := &fakeTransport{
		handler: func(_ context.Context, req *host.Request) (*host.Response, error) {
			if req.URL == "https://ops.example.com/gbk" {
				return &host.Response{Status: 200, Body: gbkBytes, FinalURL: req.URL}, nil
			}
			return &host.Response{Status: 200, Body: []byte("ok"), FinalURL: req.URL}, nil
		},
	}
	p := New(rule, tr)
	page, err := p.Search(context.Background(), "q", "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "c|b|a" {
		t.Fatalf("next = %q, want c|b|a", page.Next)
	}
	det, err := p.Details(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if det.Title != "测试标题" {
		t.Fatalf("GBK title = %q", det.Title)
	}
	if len(det.Lines) != 1 || len(det.Lines[0].Episodes) != 1 || det.Lines[0].Episodes[0].ID != "https://ops.example.com/media/ok.m3u8" {
		t.Fatalf("resolved episode ID mismatch: %+v", det.Lines)
	}
	// Reject non-HTTP fetch URLs even when dynamically supplied via episodeId.
	if _, err := p.Play(context.Background(), "1", "l0", "file:///etc/passwd"); err == nil {
		t.Fatalf("expected file:// fetch to be rejected")
	}

	// Nesting depth protection at Parse time.
	nested := `{"op":"const"}`
	for i := 0; i < MaxDepth+2; i++ {
		nested = `{"op":"branch","condition":"notEmpty","then":[` + nested + `]}`
	}
	deepRule := `{"$schema":"anx-rule/2","name":"Deep","version":"1","baseUrl":"https://a.test","search":{"steps":[` + nested + `]},"detail":{"steps":[{"op":"const"}]},"play":{"steps":[{"op":"const"}]}}`
	if _, err := Parse([]byte(deepRule)); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("expected ErrDepthExceeded, got %v", err)
	}
	if strings.Contains(det.Title, "???") {
		t.Fatalf("corrupted GBK output: %q", det.Title)
	}
}
