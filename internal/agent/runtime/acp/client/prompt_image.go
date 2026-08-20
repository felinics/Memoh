package client

import (
	"strings"

	attachmentpkg "github.com/memohai/memoh/internal/attachment"
)

// PromptImageFromDataURL converts an application attachment data URL into the
// ACP wire representation. The ACP client owns validation and image MIME
// normalization because PromptImage is its protocol type.
func PromptImageFromDataURL(payload, fallbackMime string) (PromptImage, error) {
	payload = strings.TrimSpace(payload)
	comma := strings.Index(payload, ",")
	if comma < 0 || !strings.HasPrefix(strings.ToLower(payload), "data:") ||
		!strings.Contains(strings.ToLower(payload[:comma]), ";base64") {
		return PromptImage{}, ErrInvalidPromptImage
	}
	mimeType := attachmentpkg.MimeFromDataURL(payload)
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = attachmentpkg.NormalizeMime(fallbackMime)
	}
	normalized, err := NormalizePromptImages([]PromptImage{{
		Data:     strings.TrimSpace(payload[comma+1:]),
		MimeType: mimeType,
	}})
	if err != nil {
		return PromptImage{}, err
	}
	return normalized[0], nil
}
