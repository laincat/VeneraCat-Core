package convert

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func stdlib(t testing.TB, typ string, key, iv, plain []byte) []byte {
	t.Helper()
	b, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	switch typ {
	case "aes-cfb":
		cipher.NewCFBEncrypter(b, iv).XORKeyStream(out, plain)
	case "aes-ofb":
		cipher.NewOFB(b, iv).XORKeyStream(out, plain)
	}
	return out
}
