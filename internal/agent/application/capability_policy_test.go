package application

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRouteAttachmentsByCapability_VisionSupported(t *testing.T) {
	compatibilities := []string{"vision", "tool-call"}
	attachments := []gatewayAttachment{
		{Type: "image", Transport: gatewayTransportInlineDataURL, Payload: "data:image/png;base64,abc"},
		{Type: "audio", Transport: gatewayTransportToolFileRef, Payload: "/data/voice.wav"},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Len(t, result.Native, 1)
	assert.Len(t, result.Fallback, 1)
	assert.Equal(t, "image", result.Native[0].Type)
	assert.Equal(t, "audio", result.Fallback[0].Type)
}

func TestRouteAttachmentsByCapability_SVGFallsBack(t *testing.T) {
	attachments := []gatewayAttachment{{
		Type:         "image",
		Mime:         "image/svg+xml",
		Transport:    gatewayTransportInlineDataURL,
		Payload:      "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		FallbackPath: "/data/media/logo.svg",
	}}

	result := routeAttachmentsByCapability([]string{"vision"}, attachments)
	assert.Empty(t, result.Native)
	assert.Equal(t, attachments, result.Fallback)
}

func TestRouteAttachmentsByCapability_NoVision(t *testing.T) {
	compatibilities := []string{"tool-call"}
	attachments := []gatewayAttachment{
		{Type: "image", Transport: gatewayTransportInlineDataURL, Payload: "data:image/png;base64,abc"},
		{Type: "video", Transport: gatewayTransportToolFileRef, Payload: "/data/video.mp4"},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 2)
}

func TestRouteAttachmentsByCapability_ImagePathOnlyFallsBack(t *testing.T) {
	compatibilities := []string{"vision"}
	attachments := []gatewayAttachment{
		{Type: "image", Transport: gatewayTransportToolFileRef, Payload: "/data/image.png"},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 1)
	assert.Equal(t, "image", result.Fallback[0].Type)
}

func TestRouteAttachmentsByCapability_ImageURLIsNative(t *testing.T) {
	compatibilities := []string{"vision"}
	attachments := []gatewayAttachment{
		{Type: "image", Transport: gatewayTransportPublicURL, Payload: "https://example.com/image.png"},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Len(t, result.Native, 1)
	assert.Empty(t, result.Fallback)
}

func TestRouteAttachmentsByCapability_UnknownType(t *testing.T) {
	compatibilities := []string{"vision"}
	attachments := []gatewayAttachment{
		{Type: "hologram", Transport: gatewayTransportToolFileRef, Payload: "/data/holo.dat"},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 1)
}

func TestRouteAttachmentsByCapability_Empty(t *testing.T) {
	result := routeAttachmentsByCapability([]string{"vision"}, nil)
	assert.Empty(t, result.Native)
	assert.Empty(t, result.Fallback)
}

func TestAttachmentsToAny(t *testing.T) {
	atts := []gatewayAttachment{
		{Type: "image", Transport: gatewayTransportInlineDataURL, Payload: "data:image/png;base64,abc"},
		{Type: "file", Transport: gatewayTransportToolFileRef, Payload: "/data/doc.pdf"},
	}
	result := attachmentsToAny(atts)
	assert.Len(t, result, 2)
}

func TestRouteAttachmentsByCapability_PDFWithFileInput(t *testing.T) {
	compatibilities := []string{"vision", "file-input"}
	attachments := []gatewayAttachment{
		{Type: "file", Mime: "application/pdf", Size: 2 << 20, Transport: gatewayTransportInlineDataURL, Payload: "data:application/pdf;base64,JVBERi0xLjQ="},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Len(t, result.Native, 1)
	assert.Empty(t, result.Fallback)
}

func TestRouteAttachmentsByCapability_PDFWithoutFileInputFallsBack(t *testing.T) {
	compatibilities := []string{"vision"}
	attachments := []gatewayAttachment{
		{Type: "file", Mime: "application/pdf", Size: 2 << 20, Transport: gatewayTransportInlineDataURL, Payload: "data:application/pdf;base64,JVBERi0xLjQ="},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 1)
}

func TestRouteAttachmentsByCapability_PDFPublicURLFallsBack(t *testing.T) {
	// Files travel inline only: presigned URLs re-sign per generation, which
	// breaks prompt-cache prefixes and can expire under history replay.
	compatibilities := []string{"file-input"}
	attachments := []gatewayAttachment{
		{Type: "file", Mime: "application/pdf", Size: 1 << 20, Transport: gatewayTransportPublicURL, Payload: "https://example.com/report.pdf"},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 1)
}

func TestRouteAttachmentsByCapability_SmallTextInlinesWithoutCapability(t *testing.T) {
	compatibilities := []string{}
	attachments := []gatewayAttachment{
		{Type: "file", Mime: "text/plain", Size: 1024, Transport: gatewayTransportInlineDataURL, Payload: "data:text/plain;base64,aGVsbG8="},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Len(t, result.Native, 1)
	assert.Empty(t, result.Fallback)
}

func TestRouteAttachmentsByCapability_LargeTextFallsBack(t *testing.T) {
	compatibilities := []string{"vision", "file-input"}
	attachments := []gatewayAttachment{
		{Type: "file", Mime: "text/plain", Size: inlineTextAttachmentMaxBytes + 1, Transport: gatewayTransportInlineDataURL, Payload: "data:text/plain;base64,aGVsbG8="},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 1)
}

func TestRouteAttachmentsByCapability_OversizedSingleAttachmentFallsBack(t *testing.T) {
	compatibilities := []string{"file-input"}
	attachments := []gatewayAttachment{
		{Type: "file", Mime: "application/pdf", Size: nativeAttachmentMaxBinaryBytes + 1, Transport: gatewayTransportInlineDataURL, Payload: "data:application/pdf;base64,JVBERi0xLjQ="},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	assert.Empty(t, result.Native)
	assert.Len(t, result.Fallback, 1)
}

func TestRouteAttachmentsByCapability_BudgetDemotesLargestFirst(t *testing.T) {
	compatibilities := []string{"vision", "file-input"}
	attachments := []gatewayAttachment{
		{Type: "file", Name: "small.pdf", Mime: "application/pdf", Size: 4 << 20, Transport: gatewayTransportInlineDataURL, Payload: "data:application/pdf;base64,JVBERi0xLjQ="},
		{Type: "file", Name: "big.pdf", Mime: "application/pdf", Size: 11 << 20, Transport: gatewayTransportInlineDataURL, Payload: "data:application/pdf;base64,JVBERi0xLjQ="},
	}
	result := routeAttachmentsByCapability(compatibilities, attachments)
	// 4MB + 11MB > 14MB budget: the largest is demoted, the small one stays.
	assert.Len(t, result.Native, 1)
	assert.Equal(t, "small.pdf", result.Native[0].Name)
	assert.Len(t, result.Fallback, 1)
	assert.Equal(t, "big.pdf", result.Fallback[0].Name)
}
