package weixin

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestWeixinAdapter_Type(t *testing.T) {
	adapter := NewWeixinAdapter(nil)
	if adapter.Type() != Type {
		t.Errorf("Type() = %v, want %v", adapter.Type(), Type)
	}
}

func TestWeixinAdapter_Descriptor(t *testing.T) {
	adapter := NewWeixinAdapter(nil)
	desc := adapter.Descriptor()

	if desc.Type != Type {
		t.Errorf("desc.Type = %v", desc.Type)
	}
	if desc.DisplayName != "WeChat" {
		t.Errorf("desc.DisplayName = %q", desc.DisplayName)
	}
	if !desc.Capabilities.Text {
		t.Error("should support text")
	}
	if !desc.Capabilities.Media {
		t.Error("should support media")
	}
	if !desc.Capabilities.Attachments {
		t.Error("should support attachments")
	}
	if len(desc.Capabilities.ChatTypes) != 1 || desc.Capabilities.ChatTypes[0] != channel.ConversationTypePrivate {
		t.Errorf("chat types = %v", desc.Capabilities.ChatTypes)
	}

	if _, ok := desc.ConfigSchema.Fields["token"]; !ok {
		t.Error("config schema should have 'token' field")
	}
	if desc.ConfigSchema.Fields["token"].Type != channel.FieldSecret {
		t.Error("token field should be secret")
	}
	if !desc.ConfigSchema.Fields["token"].Required {
		t.Error("token field should be required")
	}
}

func TestWeixinAdapter_Interfaces(_ *testing.T) {
	adapter := NewWeixinAdapter(nil)

	// Adapter
	var _ channel.Adapter = adapter
	// ConfigNormalizer
	var _ channel.ConfigNormalizer = adapter
	// TargetResolver
	var _ channel.TargetResolver = adapter
	// BindingMatcher
	var _ channel.BindingMatcher = adapter
	// Receiver
	var _ channel.Receiver = adapter
	// Sender
	var _ channel.Sender = adapter
	// AttachmentResolver
	var _ channel.AttachmentResolver = adapter
	// ProcessingStatusNotifier
	var _ channel.ProcessingStatusNotifier = adapter
}
