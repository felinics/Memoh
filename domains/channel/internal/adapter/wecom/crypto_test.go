package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"testing"
)

func TestDecryptFileAES256CBC(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plain := []byte("hello-wecom-aibot")
	ciphertext := encryptPKCS7To32(t, key, plain)

	out, err := DecryptFileAES256CBC(ciphertext, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("DecryptFileAES256CBC error = %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("plaintext mismatch: got=%q want=%q", string(out), string(plain))
	}
}

func encryptPKCS7To32(t *testing.T, key []byte, plain []byte) []byte {
	t.Helper()
	padded := pkcs7PadTo32(plain)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	iv := key[:aes.BlockSize]
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func pkcs7PadTo32(data []byte) []byte {
	const blockSize = 32
	pad := blockSize - (len(data) % blockSize)
	if pad == 0 {
		pad = blockSize
	}
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}
