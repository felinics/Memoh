package application

import (
	"strings"

	modeldomain "github.com/memohai/memoh/domains/model"
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
		if c == modeldomain.CompatVision {
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
