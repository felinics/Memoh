// Derived from @tencent-weixin/openclaw-weixin (MIT License, Copyright (c) 2026 Tencent Inc.)
// See LICENSE in this directory for the full license text.

package weixin

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/media"
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

// resolveCDNURL picks the server-built URL when iLink sent one, else builds it
// from the configured CDN base. Server URLs must be https: they arrive inside
// message payloads and are fetched server-side.
func resolveCDNURL(fullURL string, build func() string) (string, error) {
	fullURL = strings.TrimSpace(fullURL)
	if fullURL == "" {
		return build(), nil
	}
	parsed, err := url.Parse(fullURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("cdn: refusing non-https full url")
	}
	return fullURL, nil
}

// downloadAndDecrypt fetches encrypted bytes from the CDN and decrypts with AES-128-ECB.
func downloadAndDecrypt(cdnBaseURL, encryptedQueryParam, fullURL, aesKeyBase64 string) ([]byte, error) {
	key, err := parseAESKey(aesKeyBase64)
	if err != nil {
		return nil, err
	}
	u, err := resolveCDNURL(fullURL, func() string { return buildCDNDownloadURL(encryptedQueryParam, cdnBaseURL) })
	if err != nil {
		return nil, err
	}
	encrypted, err := fetchURL(u)
	if err != nil {
		return nil, fmt.Errorf("cdn download: %w", err)
	}
	return decryptAESECB(encrypted, key)
}

// downloadPlain fetches unencrypted bytes from the CDN.
func downloadPlain(cdnBaseURL, encryptedQueryParam, fullURL string) ([]byte, error) {
	u, err := resolveCDNURL(fullURL, func() string { return buildCDNDownloadURL(encryptedQueryParam, cdnBaseURL) })
	if err != nil {
		return nil, err
	}
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
// upload is the getuploadurl response; its UploadFullURL wins over UploadParam.
func uploadToCDN(cdnBaseURL string, upload *GetUploadURLResponse, filekey string, plaintext, aesKey []byte) (string, error) {
	return uploadToCDNReader(context.Background(), cdnBaseURL, upload, filekey, bytes.NewReader(plaintext), int64(len(plaintext)), aesKey)
}

func uploadToCDNReader(ctx context.Context, cdnBaseURL string, upload *GetUploadURLResponse, filekey string, plaintext io.Reader, plaintextSize int64, aesKey []byte) (string, error) {
	if plaintextSize < 0 || plaintextSize > media.MaxAssetBytes {
		return "", media.ErrAssetTooLarge
	}
	u, err := resolveCDNURL(upload.UploadFullURL, func() string { return buildCDNUploadURL(cdnBaseURL, upload.UploadParam, filekey) })
	if err != nil {
		return "", err
	}
	pipeReader, pipeWriter := io.Pipe()
	producerDone := make(chan error, 1)
	go func() {
		err := encryptAESECBStream(pipeWriter, plaintext, aesKey)
		_ = pipeWriter.CloseWithError(err)
		producerDone <- err
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, pipeReader)
	if err != nil {
		_ = pipeReader.CloseWithError(err)
		<-producerDone
		return "", err
	}
	req.ContentLength = int64(aesECBPaddedSize(int(plaintextSize)))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req) //nolint:mnd,gosec // CDN URL from admin config
	if err != nil {
		_ = pipeReader.CloseWithError(err)
	}
	producerErr := <-producerDone
	if err != nil {
		if producerErr != nil {
			return "", fmt.Errorf("cdn upload: %w", errors.Join(err, producerErr))
		}
		return "", fmt.Errorf("cdn upload: %w", err)
	}
	if producerErr != nil {
		_ = resp.Body.Close()
		return "", fmt.Errorf("cdn encrypt: %w", producerErr)
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

func encryptAESECBStream(dst io.Writer, src io.Reader, key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	blockSize := block.BlockSize()
	plain := make([]byte, 64*1024)
	ciphertext := make([]byte, len(plain)+blockSize)
	for {
		n, readErr := io.ReadFull(src, plain)
		fullLen := n - n%blockSize
		for offset := 0; offset < fullLen; offset += blockSize {
			block.Encrypt(ciphertext[offset:offset+blockSize], plain[offset:offset+blockSize])
		}
		if fullLen > 0 {
			if err := writeAll(dst, ciphertext[:fullLen]); err != nil {
				return err
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		padded := pkcs7Pad(plain[fullLen:n], blockSize)
		for offset := 0; offset < len(padded); offset += blockSize {
			block.Encrypt(ciphertext[offset:offset+blockSize], padded[offset:offset+blockSize])
		}
		return writeAll(dst, ciphertext[:len(padded)])
	}
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := dst.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
