package application

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// Frames holds every rendered frame of an animated attachment, Payload
	// being the first of them. It stays server-side: the attachment is still
	// one attachment for routing, fallbacks, attachment paths and the size
	// budget, and only fans out where images are handed to the model.
	Frames []string `json:"-"`
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

// modelAcceptsImages reports whether inline images are usable by this model at
// all. It is the same vision bit the router applies, read before any image is
// prepared rather than after.
func modelAcceptsImages(model models.GetResponse) bool {
	return model.Config.Accepts(models.ModalityImage)
}

// modelInputModalities returns what the model accepts as input.
func modelInputModalities(model models.GetResponse) []models.Modality {
	if model.Config.Modalities == nil {
		return nil
	}
	return model.Config.Modalities.Input
}

// routeAttachmentsByCapability splits attachments based on the model's input
// modalities. Images route natively with image input, PDFs with PDF input, and small
// plain-text files unconditionally; everything else goes through fallback. The
// native set is then trimmed to the per-request media budget, demoting the
// largest attachments first so one oversized file cannot sink the whole turn.
func routeAttachmentsByCapability(inputs []models.Modality, attachments []gatewayAttachment) capabilityRouteResult {
	hasVision := slices.Contains(inputs, models.ModalityImage)
	hasFileInput := slices.Contains(inputs, models.ModalityPDF)

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

// errUnsupportedImageBytes marks media whose bytes are not a raster image the
// provider adapters can parse, however the attachment happened to be labelled.
var errUnsupportedImageBytes = errors.New("attachment bytes are not a raster image the model can parse")

// modelImageMimeFromBytes resolves the MIME a vision request should declare for
// head, the leading bytes of an image attachment.
//
// A declared MIME is a label, not evidence. Telegram video and animated
// stickers are WebM and gzipped Lottie, platforms mislabel image subtypes, and
// the media store keeps whatever the channel reported. Handing those bytes to
// an image-only provider fails image parsing, and an attachment that stays in
// pending discussion context fails the same way on every later turn. So the
// bytes decide, and anything outside the common raster set is refused here
// instead of at the provider.
func modelImageMimeFromBytes(head []byte) (string, error) {
	detected := attachmentpkg.NormalizeMime(http.DetectContentType(head))
	if !isNativeImageMime(detected) {
		return "", fmt.Errorf("%w: detected %s", errUnsupportedImageBytes, detected)
	}
	return detected, nil
}

// normalizeInlineImageDataURL applies the same byte check to an attachment that
// already carries inline base64, and returns the data URL re-stamped with the
// MIME its bytes actually are.
//
// Payload and MIME have to be corrected together. A declared subtype that
// disagrees with the bytes is its own source of provider errors, and the two
// travel separately from here on — the data URL reaches the provider adapters
// and the External Agent prompt, while the MIME field drives capability
// routing. Fixing one and not the other just moves the disagreement.
func normalizeInlineImageDataURL(payload string) (string, string, error) {
	body, declared := splitInlineDataURL(payload)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", fmt.Errorf("%w: empty payload", errUnsupportedImageBytes)
	}
	head := body
	// base64 decodes in 4-character groups; keep the prefix aligned so the
	// sniff window stays valid for payloads far larger than it.
	const sniffChars = 512 / 3 * 4
	if len(head) > sniffChars {
		head = head[:sniffChars]
	}
	decoded, err := base64.StdEncoding.DecodeString(head)
	if err != nil {
		return "", "", fmt.Errorf("%w: payload is not valid base64", errUnsupportedImageBytes)
	}
	mime, err := modelImageMimeFromBytes(decoded)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(attachmentpkg.NormalizeMime(declared), mime) {
		return payload, mime, nil
	}
	return "data:" + mime + ";base64," + body, mime, nil
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
