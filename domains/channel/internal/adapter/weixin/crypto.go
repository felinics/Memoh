// Derived from @tencent-weixin/openclaw-weixin (MIT License, Copyright (c) 2026 Tencent Inc.)
// See LICENSE in this directory for the full license text.

package weixin

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// encryptAESECB encrypts plaintext with AES-128-ECB and PKCS7 padding.
func encryptAESECB(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	padded := pkcs7Pad(plaintext, bs)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return out, nil
}

// decryptAESECB decrypts ciphertext with AES-128-ECB and PKCS7 padding.
func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(ciphertext)%bs != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of block size %d", len(ciphertext), bs)
	}
	out := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += bs {
		block.Decrypt(out[i:i+bs], ciphertext[i:i+bs])
	}
	return pkcs7Unpad(out, bs)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padded := make([]byte, len(data)+padding)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padding) //nolint:gosec // padding is always 1..blockSize(16)
	}
	return padded
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	padding := int(data[len(data)-1])
	if padding > blockSize || padding == 0 {
		return nil, fmt.Errorf("invalid pkcs7 padding %d", padding)
	}
	for i := len(data) - padding; i < len(data); i++ {
		if data[i] != byte(padding) { //nolint:gosec // padding is always 1..blockSize(16)
			return nil, fmt.Errorf("invalid pkcs7 padding at byte %d", i)
		}
	}
	return data[:len(data)-padding], nil
}

// aesECBPaddedSize returns the ciphertext size after AES-128-ECB with PKCS7 padding.
// PKCS7 always adds at least 1 byte of padding, rounding up to a 16-byte boundary.
func aesECBPaddedSize(plaintextSize int) int {
	// ceil((n+1) / 16) * 16
	return ((plaintextSize + 1 + 15) / 16) * 16 //nolint:mnd
}

// parseAESKey parses a base64-encoded AES key. Handles two formats:
// - base64(raw 16 bytes)
// - base64(hex string of 16 bytes) -> 32 hex chars.
func parseAESKey(aesKeyBase64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("aes key base64 decode: %w", err)
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 {
		s := string(decoded)
		if isHexString(s) {
			key, err := hex.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("aes key hex decode: %w", err)
			}
			return key, nil
		}
	}
	return nil, fmt.Errorf("aes key must be 16 raw bytes or 32-char hex, got %d bytes", len(decoded))
}

func isHexString(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// CDN URL helpers.

func buildCDNDownloadURL(encryptedQueryParam, cdnBaseURL string) string {
	return cdnBaseURL + "/download?encrypted_query_param=" + url.QueryEscape(encryptedQueryParam)
}

func buildCDNUploadURL(cdnBaseURL, uploadParam, filekey string) string {
	return cdnBaseURL + "/upload?encrypted_query_param=" + url.QueryEscape(uploadParam) +
		"&filekey=" + url.QueryEscape(filekey)
}

// downloadAndDecrypt fetches encrypted bytes from the CDN and decrypts with AES-128-ECB.
func downloadAndDecrypt(cdnBaseURL, encryptedQueryParam, aesKeyBase64 string) ([]byte, error) {
	key, err := parseAESKey(aesKeyBase64)
	if err != nil {
		return nil, err
	}
	u := buildCDNDownloadURL(encryptedQueryParam, cdnBaseURL)
	encrypted, err := fetchURL(u)
	if err != nil {
		return nil, fmt.Errorf("cdn download: %w", err)
	}
	return decryptAESECB(encrypted, key)
}

// downloadPlain fetches unencrypted bytes from the CDN.
func downloadPlain(cdnBaseURL, encryptedQueryParam string) ([]byte, error) {
	u := buildCDNDownloadURL(encryptedQueryParam, cdnBaseURL)
	return fetchURL(u)
}

func fetchURL(u string) ([]byte, error) {
	resp, err := http.Get(u) //nolint:gosec,noctx
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cdn %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// uploadToCDN encrypts and uploads bytes to the WeChat CDN, returning the download param.
func uploadToCDN(cdnBaseURL, uploadParam, filekey string, plaintext, aesKey []byte) (string, error) {
	ciphertext, err := encryptAESECB(plaintext, aesKey)
	if err != nil {
		return "", fmt.Errorf("cdn encrypt: %w", err)
	}
	u := buildCDNUploadURL(cdnBaseURL, uploadParam, filekey)

	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(ciphertext)) //nolint:noctx
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req) //nolint:mnd,gosec // CDN URL from admin config
	if err != nil {
		return "", fmt.Errorf("cdn upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cdn upload %d: %s", resp.StatusCode, string(body))
	}
	downloadParam := resp.Header.Get("x-encrypted-param")
	if downloadParam == "" {
		return "", errors.New("cdn upload: missing x-encrypted-param header")
	}
	return downloadParam, nil
}
