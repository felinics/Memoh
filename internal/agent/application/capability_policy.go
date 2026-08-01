package application

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	attachmentpkg "github.com/memohai/memoh/internal/attachment"
	"github.com/memohai/memoh/internal/models"
)

const (
	gatewayTransportInlineDataURL = "inline_data_url"
	gatewayTransportPublicURL     = "public_url"
	gatewayTransportToolFileRef   = "tool_file_ref"
)

// gatewayAttachment is the strict server-to-gateway attachment contract.
// ContentHash is the content reference (replaces legacy assetId).
type gatewayAttachment struct {
	ContentHash string         `json:"contentHash,omitempty"`
	Type        string         `json:"type"`
	Mime        string         `json:"mime,omitempty"`
	Size        int64          `json:"size,omitempty"`
	Name        string         `json:"name,omitempty"`
	Transport   string         `json:"transport"`
	Payload     string         `json:"payload"`
	Metadata    map[string]any `json:"metadata,omitempty"`

	// FallbackPath is an internal helper only used by server-side routing.
	FallbackPath string `json:"-"`
}

// capabilityRouteResult holds the outcome of splitting attachments by model capability.
type capabilityRouteResult struct {
	// Native are attachments the model can consume directly as multimodal input.
	Native []gatewayAttachment
	// Fallback are attachments whose modality is unsupported; they are converted
	// to container file path references for the LLM to access via tools.
	Fallback []gatewayAttachment
}

// routeAttachmentsByCapability splits attachments based on model compatibilities.
// Only images are routed natively when the model has CompatVision; everything
// else goes through fallback.
func routeAttachmentsByCapability(compatibilities []string, attachments []gatewayAttachment) capabilityRouteResult {
	hasVision := false
	for _, c := range compatibilities {
		if c == models.CompatVision {
			hasVision = true
			break
		}
	}

	result := capabilityRouteResult{
		Native:   make([]gatewayAttachment, 0, len(attachments)),
		Fallback: make([]gatewayAttachment, 0),
	}
	for _, att := range attachments {
		att.Type = strings.ToLower(strings.TrimSpace(att.Type))
		att.Transport = strings.ToLower(strings.TrimSpace(att.Transport))
		if att.Type == "image" && hasVision && isGatewayNativeAttachment(att) {
			result.Native = append(result.Native, att)
		} else {
			result.Fallback = append(result.Fallback, att)
		}
	}
	return result
}

func isGatewayNativeAttachment(att gatewayAttachment) bool {
	switch att.Type {
	case "image":
		transport := strings.ToLower(strings.TrimSpace(att.Transport))
		if transport != gatewayTransportInlineDataURL && transport != gatewayTransportPublicURL {
			return false
		}
		payload := strings.TrimSpace(att.Payload)
		if payload == "" {
			return false
		}
		// Telegram animated stickers can retain the logical "image" type while
		// their actual payload is video/webm. Sending those to an image-only API
		// produces a deterministic HTTP 400, so trust the data URL MIME first and
		// reject any known non-image payload. Public image URLs with no MIME are
		// still allowed because the provider can inspect the remote response.
		mime := attachmentpkg.NormalizeMime(att.Mime)
		if transport == gatewayTransportInlineDataURL {
			if dataURLMime := attachmentpkg.MimeFromDataURL(payload); dataURLMime != "" {
				mime = dataURLMime
			}
		}
		return mime == "" || strings.HasPrefix(mime, "image/")
	default:
		return false
	}
}

// filterVisionImageParts is the final safety boundary before sdk.ImagePart
// values reach any vision model. Some channel paths (notably Discuss) resolve
// images directly and therefore do not pass through gatewayAttachment routing.
func filterVisionImageParts(parts []sdk.ImagePart) []sdk.ImagePart {
	filtered := make([]sdk.ImagePart, 0, len(parts))
	for _, part := range parts {
		payload := strings.TrimSpace(part.Image)
		if payload == "" {
			continue
		}
		mime := attachmentpkg.NormalizeMime(part.MediaType)
		if dataURLMime := attachmentpkg.MimeFromDataURL(payload); dataURLMime != "" {
			mime = dataURLMime
		}
		if mime != "" && !strings.HasPrefix(mime, "image/") {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

// attachmentsToAny converts typed gateway attachments to []any for JSON serialization.
func attachmentsToAny(atts []gatewayAttachment) []any {
	out := make([]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, a)
	}
	return out
}
