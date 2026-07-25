package engine

import (
	"testing"

	"github.com/memohai/memoh/domains/agent/tool"
)

func TestFileAttachmentFromToolAttachment_PreservesInlineBase64(t *testing.T) {
	t.Parallel()

	att := fileAttachmentFromToolAttachment(tool.Attachment{
		Type:        "image",
		Base64:      "data:image/png;base64,AAAA",
		PlatformKey: "native-ref",
		Mime:        "image/png",
	})
	if att.Base64 != "data:image/png;base64,AAAA" {
		t.Fatalf("expected inline base64 preserved, got %q", att.Base64)
	}
	if att.PlatformKey != "native-ref" {
		t.Fatalf("expected platform key preserved, got %q", att.PlatformKey)
	}
	if att.URL != "" || att.Path != "" {
		t.Fatalf("expected no path/url for inline attachment, got path=%q url=%q", att.Path, att.URL)
	}
}
