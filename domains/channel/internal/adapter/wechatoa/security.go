package wechatoa

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1" //nolint:gosec // WeChat Official Account signatures are SHA1 by protocol definition.
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type securityVerifier struct {
	token string
	appID string
	key   []byte
}

func newSecurityVerifier(token, encodingAESKey, appID string) (*securityVerifier, error) {
	v := &securityVerifier{
		token: strings.TrimSpace(token),
		appID: strings.TrimSpace(appID),
	}
	if strings.TrimSpace(encodingAESKey) != "" {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodingAESKey) + "=")
		if err != nil {
			return nil, fmt.Errorf("decode encodingAESKey: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("invalid encodingAESKey length: %d", len(key))
		}
		v.key = key
	}
	return v, nil
}

func (v *securityVerifier) sign(parts ...string) string {
	items := make([]string, 0, len(parts)+1)
	items = append(items, strings.TrimSpace(v.token))
	for _, part := range parts {
		items = append(items, strings.TrimSpace(part))
	}
	sort.Strings(items)
	sum := sha1.Sum([]byte(strings.Join(items, ""))) //nolint:gosec // WeChat signature algorithm requires SHA1.
	return hex.EncodeToString(sum[:])
}

func (v *securityVerifier) verifyURLSignature(signature, timestamp, nonce string) bool {
	return v.sign(timestamp, nonce) == strings.TrimSpace(signature)
}

func (v *securityVerifier) verifyMessageSignature(signature, timestamp, nonce, encrypt string) bool {
	return v.sign(timestamp, nonce, encrypt) == strings.TrimSpace(signature)
}

func (v *securityVerifier) decrypt(encrypted string) (string, error) {
	if len(v.key) == 0 {
		return "", errors.New("encodingAESKey is required")
	}
	cipherText, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return "", err
	}
	if len(cipherText)%aes.BlockSize != 0 {
		return "", errors.New("invalid cipher text size")
	}
	plain := make([]byte, len(cipherText))
	iv := v.key[:aes.BlockSize]
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, cipherText)
	plain, err = pkcs7Unpad(plain, 32)
	if err != nil {
		return "", err
	}
	if len(plain) < 20 {
		return "", errors.New("invalid decrypted payload")
	}
	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if msgLen < 0 || 20+msgLen > len(plain) {
		return "", errors.New("invalid decrypted message length")
	}
	msg := string(plain[20 : 20+msgLen])
	recvAppID := string(plain[20+msgLen:])
	if strings.TrimSpace(v.appID) != "" && strings.TrimSpace(recvAppID) != strings.TrimSpace(v.appID) {
		return "", errors.New("invalid app id in encrypted payload")
	}
	return msg, nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("invalid pkcs7 payload")
	}
	padByte := data[len(data)-1]
	padding := int(padByte)
	if padding <= 0 || padding > blockSize || padding > len(data) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	expected := bytes.Repeat([]byte{padByte}, padding)
	if !bytes.Equal(data[len(data)-padding:], expected) {
		return nil, errors.New("invalid pkcs7 bytes")
	}
	return data[:len(data)-padding], nil
}
