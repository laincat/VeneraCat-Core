package anibaka

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
)

func validCryptoAlgorithm(algo string) bool {
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "md5", "sha1", "sha256", "sha512",
		"hmac-md5", "hmac-sha1", "hmac-sha256", "hmac-sha512",
		"base64-encode", "base64-decode", "base64url-encode", "base64url-decode",
		"hex-encode", "hex-decode",
		"aes-cbc-decrypt", "aes-cbc-encrypt",
		"aes-ecb-decrypt", "aes-ecb-encrypt",
		"aes-ctr-decrypt", "aes-ctr-encrypt",
		"aes-gcm-decrypt",
		"xor":
		return true
	default:
		return false
	}
}

func runCrypto(algo string, input, keyStr, ivStr, inEnc, keyEnc, ivEnc, outEnc, padding string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(algo))
	switch a {
	case "base64-encode":
		raw, err := decodeBytes(input, defaultEnc(inEnc, "utf8"))
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(raw), nil
	case "base64url-encode":
		raw, err := decodeBytes(input, defaultEnc(inEnc, "utf8"))
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw), nil
	case "base64-decode", "base64url-decode":
		raw, err := decodeBase64Tolerant(input)
		if err != nil {
			return "", fmt.Errorf("anibaka: base64 decode: %w", err)
		}
		return encodeBytes(raw, defaultEnc(outEnc, "utf8"))
	case "hex-encode":
		raw, err := decodeBytes(input, defaultEnc(inEnc, "utf8"))
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(raw), nil
	case "hex-decode":
		raw, err := hex.DecodeString(strings.TrimSpace(input))
		if err != nil {
			return "", fmt.Errorf("anibaka: hex decode: %w", err)
		}
		return encodeBytes(raw, defaultEnc(outEnc, "utf8"))
	case "md5", "sha1", "sha256", "sha512":
		raw, err := decodeBytes(input, defaultEnc(inEnc, "utf8"))
		if err != nil {
			return "", err
		}
		h := newHash(a)
		h.Write(raw)
		return encodeBytes(h.Sum(nil), defaultEnc(outEnc, "hex"))
	case "hmac-md5", "hmac-sha1", "hmac-sha256", "hmac-sha512":
		raw, err := decodeBytes(input, defaultEnc(inEnc, "utf8"))
		if err != nil {
			return "", err
		}
		key, err := decodeBytes(keyStr, defaultEnc(keyEnc, "utf8"))
		if err != nil {
			return "", err
		}
		baseName := strings.TrimPrefix(a, "hmac-")
		mac := hmac.New(func() hash.Hash { return newHash(baseName) }, key)
		mac.Write(raw)
		return encodeBytes(mac.Sum(nil), defaultEnc(outEnc, "hex"))
	case "xor":
		raw, err := decodeBytes(input, defaultEnc(inEnc, "utf8"))
		if err != nil {
			return "", err
		}
		key, err := decodeBytes(keyStr, defaultEnc(keyEnc, "utf8"))
		if err != nil {
			return "", err
		}
		if len(key) == 0 {
			return "", errors.New("anibaka: xor key is empty")
		}
		out := make([]byte, len(raw))
		for i := range raw {
			out[i] = raw[i] ^ key[i%len(key)]
		}
		return encodeBytes(out, defaultEnc(outEnc, "utf8"))
	case "aes-cbc-decrypt", "aes-cbc-encrypt",
		"aes-ecb-decrypt", "aes-ecb-encrypt",
		"aes-ctr-decrypt", "aes-ctr-encrypt",
		"aes-gcm-decrypt":
		return runAES(a, input, keyStr, ivStr, inEnc, keyEnc, ivEnc, outEnc, padding)
	default:
		return "", fmt.Errorf("anibaka: unsupported crypto algorithm %q", algo)
	}
}

func runAES(algo, input, keyStr, ivStr, inEnc, keyEnc, ivEnc, outEnc, padding string) (string, error) {
	isDecrypt := strings.HasSuffix(algo, "-decrypt")
	defaultIn := "utf8"
	defaultOut := "base64"
	if isDecrypt {
		defaultIn = "base64"
		defaultOut = "utf8"
	}
	data, err := decodeBytes(input, defaultEnc(inEnc, defaultIn))
	if err != nil {
		return "", fmt.Errorf("anibaka: decode AES input: %w", err)
	}
	key, err := decodeBytes(keyStr, defaultEnc(keyEnc, "utf8"))
	if err != nil {
		return "", fmt.Errorf("anibaka: decode AES key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("anibaka: AES key: %w", err)
	}

	padMode := strings.ToLower(strings.TrimSpace(padding))
	if padMode == "" {
		padMode = "pkcs7"
	}

	switch algo {
	case "aes-cbc-decrypt":
		iv, err := decodeBytes(ivStr, defaultEnc(ivEnc, "utf8"))
		if err != nil || len(iv) != aes.BlockSize {
			return "", fmt.Errorf("anibaka: AES-CBC IV must be 16 bytes")
		}
		if len(data) == 0 || len(data)%aes.BlockSize != 0 {
			return "", errors.New("anibaka: AES-CBC ciphertext is not block-aligned")
		}
		out := make([]byte, len(data))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
		plain, err := unpadBytes(out, padMode)
		if err != nil {
			return "", err
		}
		return encodeBytes(plain, defaultEnc(outEnc, defaultOut))

	case "aes-cbc-encrypt":
		iv, err := decodeBytes(ivStr, defaultEnc(ivEnc, "utf8"))
		if err != nil || len(iv) != aes.BlockSize {
			return "", fmt.Errorf("anibaka: AES-CBC IV must be 16 bytes")
		}
		padded, err := padBytes(data, padMode)
		if err != nil {
			return "", err
		}
		out := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
		return encodeBytes(out, defaultEnc(outEnc, defaultOut))

	case "aes-ecb-decrypt":
		if len(data) == 0 || len(data)%aes.BlockSize != 0 {
			return "", errors.New("anibaka: AES-ECB ciphertext is not block-aligned")
		}
		out := make([]byte, len(data))
		for i := 0; i < len(data); i += aes.BlockSize {
			block.Decrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
		}
		plain, err := unpadBytes(out, padMode)
		if err != nil {
			return "", err
		}
		return encodeBytes(plain, defaultEnc(outEnc, defaultOut))

	case "aes-ecb-encrypt":
		padded, err := padBytes(data, padMode)
		if err != nil {
			return "", err
		}
		out := make([]byte, len(padded))
		for i := 0; i < len(padded); i += aes.BlockSize {
			block.Encrypt(out[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
		}
		return encodeBytes(out, defaultEnc(outEnc, defaultOut))

	case "aes-ctr-decrypt", "aes-ctr-encrypt":
		iv, err := decodeBytes(ivStr, defaultEnc(ivEnc, "utf8"))
		if err != nil || len(iv) != aes.BlockSize {
			return "", fmt.Errorf("anibaka: AES-CTR IV must be 16 bytes")
		}
		out := make([]byte, len(data))
		cipher.NewCTR(block, iv).XORKeyStream(out, data)
		return encodeBytes(out, defaultEnc(outEnc, defaultOut))

	case "aes-gcm-decrypt":
		iv, err := decodeBytes(ivStr, defaultEnc(ivEnc, "utf8"))
		if err != nil {
			return "", fmt.Errorf("anibaka: decode AES-GCM nonce: %w", err)
		}
		aead, err := cipher.NewGCMWithNonceSize(block, len(iv))
		if err != nil {
			return "", fmt.Errorf("anibaka: AES-GCM nonce: %w", err)
		}
		plain, err := aead.Open(nil, iv, data, nil)
		if err != nil {
			return "", fmt.Errorf("anibaka: AES-GCM decrypt: %w", err)
		}
		return encodeBytes(plain, defaultEnc(outEnc, defaultOut))
	}
	return "", fmt.Errorf("anibaka: unsupported AES mode %q", algo)
}

func newHash(name string) hash.Hash {
	switch name {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha512":
		return sha512.New()
	default:
		return sha256.New()
	}
}

func defaultEnc(v, fallback string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" {
		return fallback
	}
	return s
}

func decodeBytes(s, enc string) ([]byte, error) {
	switch enc {
	case "utf8", "utf-8", "raw", "":
		return []byte(s), nil
	case "hex":
		return hex.DecodeString(strings.TrimSpace(s))
	case "base64", "b64":
		return decodeBase64Tolerant(s)
	default:
		return nil, fmt.Errorf("anibaka: unsupported encoding %q", enc)
	}
}

func encodeBytes(b []byte, enc string) (string, error) {
	switch enc {
	case "utf8", "utf-8", "raw", "":
		return string(b), nil
	case "hex":
		return hex.EncodeToString(b), nil
	case "base64", "b64":
		return base64.StdEncoding.EncodeToString(b), nil
	default:
		return "", fmt.Errorf("anibaka: unsupported encoding %q", enc)
	}
}

func decodeBase64Tolerant(s string) ([]byte, error) {
	clean := strings.TrimSpace(s)
	clean = strings.NewReplacer("\r", "", "\n", "", " ", "").Replace(clean)
	if b, err := base64.StdEncoding.DecodeString(clean); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(clean); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(clean); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(clean)
}

func padBytes(data []byte, mode string) ([]byte, error) {
	switch mode {
	case "none":
		if len(data)%aes.BlockSize != 0 {
			return nil, errors.New("anibaka: unpadded AES input is not block-aligned")
		}
		return append([]byte(nil), data...), nil
	case "zero":
		rem := len(data) % aes.BlockSize
		if rem == 0 {
			return append([]byte(nil), data...), nil
		}
		return append(append([]byte(nil), data...), make([]byte, aes.BlockSize-rem)...), nil
	case "pkcs7":
		pad := aes.BlockSize - (len(data) % aes.BlockSize)
		return append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(pad)}, pad)...), nil
	default:
		return nil, fmt.Errorf("anibaka: unsupported padding %q", mode)
	}
}

func unpadBytes(data []byte, mode string) ([]byte, error) {
	switch mode {
	case "none":
		return data, nil
	case "zero":
		return bytes.TrimRight(data, "\x00"), nil
	case "pkcs7":
		if len(data) == 0 {
			return nil, errors.New("anibaka: invalid PKCS#7 padding")
		}
		pad := int(data[len(data)-1])
		if pad == 0 || pad > aes.BlockSize || pad > len(data) {
			return nil, errors.New("anibaka: invalid PKCS#7 padding")
		}
		for _, c := range data[len(data)-pad:] {
			if int(c) != pad {
				return nil, errors.New("anibaka: invalid PKCS#7 padding")
			}
		}
		return data[:len(data)-pad], nil
	default:
		return nil, fmt.Errorf("anibaka: unsupported padding %q", mode)
	}
}
