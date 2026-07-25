// Package publicmedia defines signed public image media paths shared by channel
// adapters and HTTP handlers.
package publicmedia

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	OriginalMaxBytes    int64 = 10 << 20
	PreviewMaxBytes     int64 = 1 << 20
	PreviewMaxDimension       = 8192
	PreviewMaxPixels    int64 = 40_000_000

	PathRoot = "/channels/"

	SignedURLTTL = 7 * 24 * time.Hour

	QueryExpires   = "exp"
	QuerySignature = "sig"
)

var hashPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func IsContentHash(value string) bool {
	return hashPattern.MatchString(strings.ToLower(strings.TrimSpace(value)))
}

func OriginalPath(channelType, botID, contentHash, name string) string {
	return PathPrefix(channelType) + neturl.PathEscape(botID) + "/" + neturl.PathEscape(strings.ToLower(contentHash)) + "/original/" + neturl.PathEscape(name)
}

func PreviewPath(channelType, botID, contentHash string) string {
	return PathPrefix(channelType) + neturl.PathEscape(botID) + "/" + neturl.PathEscape(strings.ToLower(contentHash)) + "/preview.jpg"
}

func PathPrefix(channelType string) string {
	return PathRoot + neturl.PathEscape(strings.TrimSpace(channelType)) + "/public/media/"
}

type Signer struct {
	secret []byte
	ttl    time.Duration
}

func NewSigner(secret string, ttl time.Duration) *Signer {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = SignedURLTTL
	}
	return &Signer{secret: []byte(secret), ttl: ttl}
}

func (s *Signer) SignPath(path string, now time.Time) (string, bool) {
	if s == nil || len(s.secret) == 0 || !IsPath(path) {
		return "", false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	expires := now.UTC().Add(s.ttl).Unix()
	values := neturl.Values{}
	values.Set(QueryExpires, strconv.FormatInt(expires, 10))
	values.Set(QuerySignature, s.signature(path, expires))
	return path + "?" + values.Encode(), true
}

func (s *Signer) Validate(path string, query neturl.Values, now time.Time) bool {
	if s == nil || len(s.secret) == 0 || !IsPath(path) {
		return false
	}
	rawExpires := strings.TrimSpace(query.Get(QueryExpires))
	rawSignature := strings.TrimSpace(query.Get(QuerySignature))
	if rawExpires == "" || rawSignature == "" {
		return false
	}
	expires, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil || expires <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.UTC().Unix() > expires {
		return false
	}
	expected := s.signature(path, expires)
	return hmac.Equal([]byte(rawSignature), []byte(expected))
}

func (s *Signer) signature(path string, expires int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(path))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strconv.FormatInt(expires, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func IsPath(path string) bool {
	if !strings.HasPrefix(path, PathRoot) {
		return false
	}
	rest := strings.TrimPrefix(path, PathRoot)
	parts := strings.Split(rest, "/")
	if len(parts) == 6 && parts[1] == "public" && parts[2] == "media" && parts[5] == "preview.jpg" {
		return validPathIDs(parts[0], parts[3], parts[4])
	}
	if len(parts) == 7 && parts[1] == "public" && parts[2] == "media" && parts[5] == "original" && parts[6] != "" {
		return validPathIDs(parts[0], parts[3], parts[4])
	}
	return false
}

func validPathIDs(escapedChannelType, escapedBotID, escapedHash string) bool {
	channelType, err := neturl.PathUnescape(escapedChannelType)
	if err != nil || strings.TrimSpace(channelType) == "" || strings.ContainsAny(channelType, `/\`) || channelType == "." || channelType == ".." {
		return false
	}
	if !validPathBotID(escapedBotID) {
		return false
	}
	return validContentHash(escapedHash)
}

func validContentHash(escapedHash string) bool {
	contentHash, err := neturl.PathUnescape(escapedHash)
	return err == nil && IsContentHash(contentHash)
}

func IsBotID(value string) bool {
	botID := strings.TrimSpace(value)
	return botID != "" && !strings.ContainsAny(botID, `/\`) && botID != "." && botID != ".."
}

func IsChannelType(value string) bool {
	channelType := strings.TrimSpace(value)
	return channelType != "" && !strings.ContainsAny(channelType, `/\`) && channelType != "." && channelType != ".."
}

func validPathBotID(escapedBotID string) bool {
	botID, err := neturl.PathUnescape(escapedBotID)
	if err != nil {
		return false
	}
	return IsBotID(botID)
}
