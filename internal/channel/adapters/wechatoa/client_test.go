package wechatoa

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestBuildSendPayload_ImagePlatformKey(t *testing.T) {
	client := &apiClient{}
	payload, err := client.buildSendPayload(context.Background(), channel.PreparedMessage{
		Message: channel.Message{
			Attachments: []channel.Attachment{
				{Type: channel.AttachmentImage, PlatformKey: "mid_123"},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildSendPayload error = %v", err)
	}
	if payload["msgtype"] != "image" {
		t.Fatalf("unexpected msgtype: %v", payload["msgtype"])
	}
}

func TestBuildSendPayload_UnsupportedAttachment(t *testing.T) {
	client := &apiClient{}
	_, err := client.buildSendPayload(context.Background(), channel.PreparedMessage{
		Message: channel.Message{
			Attachments: []channel.Attachment{
				{Type: channel.AttachmentFile, PlatformKey: "mid_file"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
