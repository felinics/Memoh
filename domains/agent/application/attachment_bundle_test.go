package application

import (
	"reflect"
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/media/attachment"
)

func TestAttachmentBundleConversions(t *testing.T) {
	raw := attachment.Bundle{
		Type: " IMAGE ", Base64: " raw ", Path: " path ", URL: " url ",
		PlatformKey: " platform ", ContentHash: " hash ", Name: " name ",
		Mime: " IMAGE/PNG ", Size: 42, Metadata: map[string]any{"key": "value"},
	}
	gotAttachment := attachmentFromBundle(raw)
	wantAttachment := agentdomain.Attachment{
		Type: " IMAGE ", Base64: " raw ", Path: " path ", URL: " url ",
		PlatformKey: " platform ", ContentHash: " hash ", Name: " name ",
		Mime: " IMAGE/PNG ", Size: 42, Metadata: map[string]any{"key": "value"},
	}
	if !reflect.DeepEqual(gotAttachment, wantAttachment) {
		t.Fatalf("attachmentFromBundle() = %#v, want %#v", gotAttachment, wantAttachment)
	}

	if got, want := bundleFromAttachment(gotAttachment), raw.Normalize(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bundleFromAttachment() = %#v, want %#v", got, want)
	}

	normalized := attachment.Bundle{
		Type:        "image",
		URL:         "https://example.test/a.png",
		ContentHash: "hash",
		Name:        "a.png",
		Mime:        "image/png",
		Metadata:    map[string]any{"source": "test"},
	}.Normalize()
	if got := bundleFromAttachment(attachmentFromBundle(normalized)); !reflect.DeepEqual(got, normalized) {
		t.Fatalf("normalized bundle round trip = %#v, want %#v", got, normalized)
	}
}
