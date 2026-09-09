package application

import (
	"strings"

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/models"
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

const (
	// nativeAttachmentMaxBinaryBytes caps a single natively-routed attachment.
	// nativeRequestMediaBudgetBytes caps the binary total per request: base64
	// inflates by 4/3, so 14MB binary ≈ 18.7MB on the wire — inside Gemini's
	// ~20MB inline request ceiling and well inside Anthropic's 32MB.
	nativeAttachmentMaxBinaryBytes = 12 * 1024 * 1024
	nativeRequestMediaBudgetBytes  = 14 * 1024 * 1024
	// inlineTextAttachmentMaxBytes bounds the plain-text inline channel. Text
	// is every model's native tongue, so it needs no capability bit — but big
	// text belongs in the workspace where tools read it selectively.
	inlineTextAttachmentMaxBytes = 64 * 1024
)

// routeAttachmentsByCapability splits attachments based on model compatibilities.
// Images route natively with CompatVision, PDFs with CompatFileInput, and small
// plain-text files unconditionally; everything else goes through fallback. The
// native set is then trimmed to the per-request media budget, demoting the
// largest attachments first so one oversized file cannot sink the whole turn.
func routeAttachmentsByCapability(compatibilities []string, attachments []gatewayAttachment) capabilityRouteResult {
	hasVision := false
	hasFileInput := false
	for _, c := range compatibilities {
		switch c {
		case models.CompatVision:
			hasVision = true
		case models.CompatFileInput:
			hasFileInput = true
		}
	}

	result := capabilityRouteResult{
		Native:   make([]gatewayAttachment, 0, len(attachments)),
		Fallback: make([]gatewayAttachment, 0),
	}
	for _, att := range attachments {
		att.Type = strings.ToLower(strings.TrimSpace(att.Type))
		att.Transport = strings.ToLower(strings.TrimSpace(att.Transport))

		native := false
		switch {
		case att.Type == "image" && hasVision && isNativeImageAttachment(att):
			native = isGatewayNativeAttachment(att)
		case att.Type == "file" && isNativeDocumentMime(att.Mime) && hasFileInput:
			native = isGatewayNativeAttachment(att)
		case att.Type == "file" && isInlineTextMime(att.Mime) &&
			attachmentBinarySize(att) <= inlineTextAttachmentMaxBytes:
			native = isGatewayNativeAttachment(att)
		}
		if native && attachmentBinarySize(att) > nativeAttachmentMaxBinaryBytes {
			native = false
		}
		if native {
			result.Native = append(result.Native, att)
		} else {
			result.Fallback = append(result.Fallback, att)
		}
	}

	// Enforce the per-request budget: demote the largest native attachments
	// until the remaining set fits.
	for totalNativeBinarySize(result.Native) > nativeRequestMediaBudgetBytes && len(result.Native) > 0 {
		largest := 0
		for i := 1; i < len(result.Native); i++ {
			if attachmentBinarySize(result.Native[i]) > attachmentBinarySize(result.Native[largest]) {
				largest = i
			}
		}
		result.Fallback = append(result.Fallback, result.Native[largest])
		result.Native = append(result.Native[:largest], result.Native[largest+1:]...)
	}
	return result
}

func totalNativeBinarySize(atts []gatewayAttachment) int64 {
	var total int64
	for _, att := range atts {
		total += attachmentBinarySize(att)
	}
	return total
}

// attachmentBinarySize estimates the attachment's decoded size: the declared
// Size when present, else 3/4 of the base64 payload length.
func attachmentBinarySize(att gatewayAttachment) int64 {
	if att.Size > 0 {
		return att.Size
	}
	payload := strings.TrimSpace(att.Payload)
	if idx := strings.Index(payload, ","); idx >= 0 && strings.HasPrefix(strings.ToLower(payload), "data:") {
		payload = payload[idx+1:]
	}
	return int64(len(payload)) * 3 / 4
}

// isNativeDocumentMime reports whether the mime is a provider-native document
// format. PDF only for now; docx/pptx join once the pipeline supports them.
func isNativeDocumentMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	return mime == "application/pdf"
}

// isNativeImageMime is the common raster set accepted by the native provider
// adapters. UI type=image is only a presentation hint; it must not admit SVG,
// BMP, HEIC, or arbitrary image/* bytes into a provider ImagePart.
func isNativeImageMime(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func isNativeImageAttachment(att gatewayAttachment) bool {
	mime := attachmentpkg.NormalizeMime(att.Mime)
	if mime == "" && strings.EqualFold(strings.TrimSpace(att.Transport), gatewayTransportInlineDataURL) {
		mime = attachmentpkg.MimeFromDataURL(att.Payload)
	}
	// Preserve URL-only image input: its bytes are remote and cannot be sniffed
	// here. A supplied MIME still has to pass the common-raster allowlist.
	if mime == "" && strings.EqualFold(strings.TrimSpace(att.Transport), gatewayTransportPublicURL) {
		return true
	}
	return isNativeImageMime(mime)
}

// isInlineTextMime reports whether the attachment is plain text that can be
// inlined directly as a message text part.
func isInlineTextMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	return strings.HasPrefix(mime, "text/") || mime == "application/json"
}

func isGatewayNativeAttachment(att gatewayAttachment) bool {
	transport := strings.ToLower(strings.TrimSpace(att.Transport))
	switch att.Type {
	case "image":
		if transport != gatewayTransportInlineDataURL && transport != gatewayTransportPublicURL {
			return false
		}
		return strings.TrimSpace(att.Payload) != ""
	case "file":
		// Files travel inline only: a presigned/public URL re-signs on every
		// generation, which both breaks prompt-cache prefixes and may expire
		// out from under history replay.
		if transport != gatewayTransportInlineDataURL {
			return false
		}
		return strings.TrimSpace(att.Payload) != ""
	default:
		return false
	}
}

// attachmentsToAny converts typed gateway attachments to []any for JSON serialization.
func attachmentsToAny(atts []gatewayAttachment) []any {
	out := make([]any, 0, len(atts))
	for _, a := range atts {
		out = append(out, a)
	}
	return out
}
