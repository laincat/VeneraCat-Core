package js

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

// Convert, called the way the official sources call it, must give the same
// bytes as an independent implementation. The expected values were produced by
// Node v26.12.1's crypto (OpenSSL 3.5.9) for these exact inputs
// (E:/Temp/core-convert-diff/gen_fixed.mjs), not by Core, so a change in Core's
// hashing, HMAC, base64 or AES modes shows up here as a mismatch. AES decrypt
// returns the raw blocks with the PKCS#7 padding still on, as Venera does; the
// sources strip it themselves.
func TestConvertMatchesAnIndependentImplementation(t *testing.T) {
	r, err := New(Options{}, Services{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	for _, c := range []struct{ script, want string }{
		{`Convert.hexEncode(Convert.md5(Convert.encodeUtf8("173105000018comicAPP")))`, "8a15cfde42c7d7e7a6359c1f883771ff"},
		{`Convert.hexEncode(Convert.md5(Convert.encodeUtf8("jm验证🐾")))`, "11fbf8eb9754ff5f1e6f6665aa941f54"},
		{`Convert.hexEncode(Convert.sha256(Convert.encodeUtf8("海贼王 one piece"))).slice(0, 4)`, "8747"},
		{`Convert.hexEncode(Convert.sha512(Convert.encodeUtf8("tok-🐾-123")))`, "863f6748a4d1466d4e5540ff9b7652251c31da31077b725deaf49b6332c8eaf34ed7c3db24df24b7b48636107a850eebd2c1d439cfcb66e0fa836606bf965087"},
		{`Convert.hexEncode(Convert.sha1(Convert.encodeUtf8("")))`, "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{`Convert.hmacString(Convert.encodeUtf8("picacg-key"), Convert.encodeUtf8("comics?page=1post"), 'sha256')`, "f66fd5ae5d9bac72117ac508d5d771dacb38fc0a1d2739150604911b90d78a1b"},
		{`Convert.hmacString(Convert.encodeUtf8("KKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKKK"), Convert.encodeUtf8("v"), 'sha512')`, "bceaa45f39bc7f4e47abf116867b576109c9f8326d1b9136efdbe8dec851df245c97a9d9f28b9b4f4c0ed95f073081950c0fa0b3c1f5979838e53a864a8d8684"},
		{`Convert.hexEncode(Convert.hmac(Convert.encodeUtf8("k"), Convert.encodeUtf8("中"), 'sha1'))`, "21fc221f1c7b445446451e8cc1d69ccb062365bb"},
		{`Convert.encodeBase64(new Uint8Array([255,0,254,16,128]))`, "/wD+EIA="},
		{`Convert.hexEncode(Convert.decodeBase64("/wD+EIA"))`, "ff00fe1080"},
		{`Convert.hexEncode(Convert.decryptAesEcb(Convert.decodeBase64("FCAX19A8BKl+lLAveEXQvtyWO1RW2boMdchzDZOTNV9Hg5uwwt7ONPpKrrHNDjGn"), new Uint8Array([204,38,11,160,197,129,7,51,151,35,54,230,55,30,95,188,55,249,38,34,135,194,186,248,87,225,29,200,209,210,17,30])))`, "7b22636f6465223a3230302c2264617461223a22e4b8ade69687f09f90be227d10101010101010101010101010101010"},
		{`Convert.hexEncode(Convert.decryptAesCbc(new Uint8Array([5,56,225,174,39,0,254,72,189,51,94,189,27,175,240,59,106,170,17,87,3,216,35,124,236,226,27,104,89,162,174,79,126,16,138,143,2,151,182,159,184,148,112,119,247,214,155,169,121,3,92,29,38,139,201,244,163,9,245,204,48,211,184,153,232,149,137,39,208,82,80,215,13,142,245,67,195,123,182,173,230,83,30,123,12,14,85,227,208,235,1,95,254,205,254,144,168,25,51,110,39,154,208,149,57,171,133,244,64,73,197,74]), new Uint8Array([197,27,102,103,175,32,111,181,66,155,166,158,38,65,188,139]), new Uint8Array([211,48,36,6,52,194,253,58,89,213,145,208,159,50,63,187])))`, "48a17229e810bf377c3f984bd8ba1da05f621fb10a6acb1de81d49d11d2eaf4fde821eb0feb63c0f9a5bec6487ba9a23bcf94b12082cad07a7b2381a6bc62cb006fec9a4b5d05a4c1add83ec2ee5a6331f9387f717d2d1e7f886778e948fe58835e0dafa0c0c0c0c0c0c0c0c0c0c0c0c"},
		{`Convert.hexEncode(Convert.encryptAesCfb(new Uint8Array([87,182,189,126,159,196,244,139,141,6,10,110,249,224,169,214,111,237,80,173,64,101,58,91,25,203,160,152,180,178,210,135,5,189,217,234,230]), new Uint8Array([197,27,102,103,175,32,111,181,66,155,166,158,38,65,188,139]), new Uint8Array([211,48,36,6,52,194,253,58,89,213,145,208,159,50,63,187]), 1))`, "4f49c7e0ef3306cab9a3666012be1795b0665179939e0d046e3b4c092b74600b4588379683"},
		{`Convert.hexEncode(Convert.encryptAesOfb(new Uint8Array([94,192,156,76,160,21,116,36,114,69,122,79,13,198,162,135,121,181,161,38,30,128,230,218,58,227,80,119,183,50,216,220,104,92,143,12,145,140,161,232,163,202,240,198,28,140,163,87]), new Uint8Array([197,27,102,103,175,32,111,181,66,155,166,158,38,65,188,139]), 16, new Uint8Array([211,48,36,6,52,194,253,58,89,213,145,208,159,50,63,187])))`, "465a8fb6ef444575ec01fff19e7ab60b582bd0bfa9723b6046eb7333586ca699266737af809aea4e1593298159a9e51f"},
	} {
		got, err := r.Eval(ctx, "String("+c.script+")")
		var s string
		if err == nil {
			err = json.Unmarshal(got, &s)
		}
		if err != nil || s != c.want {
			t.Errorf("%.80s: got %q, want %q, err %v", c.script, s, c.want, err)
		}
	}
}

// decryptRsa takes the key the way the Venera host contract defines it:
// base64 PKCS#8 DER. A PEM block - what openssl and most key tools print - is not
// accepted and the call yields null with a logged error, as any failed
// conversion does. No official source calls decryptRsa, so this pins the
// contract rather than widening it; a source holding a PEM key strips the
// armour first, as the third case shows.
func TestDecryptRsaTakesBase64PKCS8(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := rsa.EncryptPKCS1v15(rand.Reader, &priv.PublicKey, []byte("rsa-\u4e2d\u6587"))
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(der)
	pkcs8PEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pkcs1PEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	// []int, not []byte: encoding/json writes a []byte as a base64 string, and
	// the script needs a numeric array to build the Uint8Array from.
	nums := make([]int, len(ct))
	for i, b := range ct {
		nums[i] = int(b)
	}
	cipherBytes, _ := json.Marshal(nums)

	var logged []string
	r, err := New(Options{}, Services{Log: func(level, title, content string) {
		logged = append(logged, level+"|"+content)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	decrypt := func(key string) string {
		k, _ := json.Marshal(key)
		out, err := r.Eval(ctx, "(() => { const p = Convert.decryptRsa(new Uint8Array("+string(cipherBytes)+"), "+string(k)+"); return p === null ? 'null' : Convert.decodeUtf8(p) })()")
		if err != nil {
			t.Fatal(err)
		}
		var s string
		if err := json.Unmarshal(out, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := decrypt(b64); got != "rsa-\u4e2d\u6587" {
		t.Fatalf("base64 PKCS#8: got %q", got)
	}
	for name, key := range map[string]string{"PKCS#8 PEM": pkcs8PEM, "PKCS#1 PEM": pkcs1PEM} {
		logged = nil
		if got := decrypt(key); got != "null" {
			t.Fatalf("%s: got %q, want null under the base64 PKCS#8 contract", name, got)
		}
		if !strings.Contains(strings.Join(logged, "\n"), "rsa key") {
			t.Fatalf("%s: the refusal was not logged: %q", name, logged)
		}
	}
	// What a source does with a PEM key in hand: drop the armour, keep the body.
	block, _ := pem.Decode([]byte(pkcs8PEM))
	if block == nil {
		t.Fatal("test key did not round-trip through PEM")
	}
	stripped := base64.StdEncoding.EncodeToString(block.Bytes)
	if got := decrypt(stripped); got != "rsa-\u4e2d\u6587" {
		t.Fatalf("PEM armour stripped by the source: got %q", got)
	}
}
