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
