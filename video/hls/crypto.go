package hls

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
)

// DecryptAES128 decrypts a segment encrypted with EXT-X-KEY METHOD=AES-128
// (AES-128-CBC, PKCS#7 padding). key and iv must be 16 bytes each. The input
// is not modified. Fetching the key is the client's job.
func DecryptAES128(segment []byte, key, iv []byte) ([]byte, error) {
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("hls: AES-128 key must be 16 bytes, got %d", len(key))
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("hls: AES-128 IV must be 16 bytes, got %d", len(iv))
	}
	if len(segment) == 0 || len(segment)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("hls: ciphertext length %d is not a positive multiple of 16", len(segment))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(segment))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, segment)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, errors.New("hls: invalid PKCS#7 padding")
	}
	for _, c := range out[len(out)-pad:] {
		if int(c) != pad {
			return nil, errors.New("hls: invalid PKCS#7 padding")
		}
	}
	return out[:len(out)-pad], nil
}

// SegmentIV returns the 16-byte IV of a segment (RFC 8216 section 5.2).
//
// With an explicit EXT-X-KEY IV attribute ("0x" followed by up to 32 hex
// digits, left-padded with zeros) that value is used. Otherwise the IV is the
// segment's media sequence number, mediaSequence+segIndex, as a 128-bit
// big-endian integer.
func SegmentIV(mediaSequence int64, segIndex int, explicitIV string) ([]byte, error) {
	iv := make([]byte, 16)
	if s := strings.TrimSpace(explicitIV); s != "" {
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			s = s[2:]
		}
		if len(s) == 0 || len(s) > 32 {
			return nil, fmt.Errorf("hls: IV %q must have 1 to 32 hex digits", explicitIV)
		}
		if len(s)%2 == 1 {
			s = "0" + s
		}
		raw, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("hls: invalid IV %q: %w", explicitIV, err)
		}
		copy(iv[16-len(raw):], raw)
		return iv, nil
	}
	if mediaSequence < 0 || segIndex < 0 {
		return nil, errors.New("hls: media sequence and segment index must not be negative")
	}
	if mediaSequence > math.MaxInt64-int64(segIndex) {
		return nil, errors.New("hls: media sequence overflow")
	}
	binary.BigEndian.PutUint64(iv[8:], uint64(mediaSequence)+uint64(segIndex))
	return iv, nil
}
