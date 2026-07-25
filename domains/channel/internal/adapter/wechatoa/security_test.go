package wechatoa

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"math"
	"testing"
)

func TestSecurityVerifierSignature(t *testing.T) {
	v, err := newSecurityVerifier("token", "", "wx123")
	if err != nil {
		t.Fatalf("newSecurityVerifier error = %v", err)
	}
	sig := v.sign("1714037059", "486452656")
	if !v.verifyURLSignature(sig, "1714037059", "486452656") {
		t.Fatal("signature should be valid")
	}
}

func TestSecurityVerifierDecrypt(t *testing.T) {
	key := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	v, err := newSecurityVerifier("token", key, "wx123")
	if err != nil {
		t.Fatalf("newSecurityVerifier error = %v", err)
	}
	plainXML := "<xml><ToUserName><![CDATA[to]]></ToUserName></xml>"
	encrypted := encryptForTest(t, v.key, "wx123", plainXML)
	decoded, err := v.decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt error = %v", err)
	}
	if decoded != plainXML {
		t.Fatalf("decoded mismatch: %q", decoded)
	}
}

func encryptForTest(t *testing.T, key []byte, appID, msg string) string {
	t.Helper()
	random := bytes.Repeat([]byte{'a'}, 16)
	msgBytes := []byte(msg)
	appBytes := []byte(appID)
	if len(msgBytes) > math.MaxUint32 {
		t.Fatal("message too large")
	}
	raw := make([]byte, 0, 16+4+len(msgBytes)+len(appBytes))
	raw = append(raw, random...)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(msgBytes))) //nolint:gosec // Test payload length is bounded above.
	raw = append(raw, length...)
	raw = append(raw, msgBytes...)
	raw = append(raw, appBytes...)
	raw = pkcs7Pad(raw, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := key[:aes.BlockSize]
	out := make([]byte, len(raw))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, raw)
	return base64.StdEncoding.EncodeToString(out)
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	padLen := blockSize - (len(src) % blockSize)
	if padLen == 0 {
		padLen = blockSize
	}
	if padLen > math.MaxUint8 {
		panic("invalid padding size")
	}
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen) //nolint:gosec // Test padding size is bounded by blockSize.
	return append(src, padding...)
}
