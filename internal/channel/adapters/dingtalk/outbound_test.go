package dingtalk

import (
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestBuildAPIPayloadTextAndMarkdown(t *testing.T) {
	msgKey, msgParam, err := buildAPIPayload(channel.Message{
		Format: channel.MessageFormatPlain,
		Text:   " hello ",
	}, nil)
	if err != nil {
		t.Fatalf("buildAPIPayload text error = %v", err)
	}
	if msgKey != "sampleText" {
		t.Fatalf("text msgKey = %q, want sampleText", msgKey)
	}
	assertJSONField(t, msgParam, "content", "hello")

	msgKey, msgParam, err = buildAPIPayload(channel.Message{
		Format: channel.MessageFormatMarkdown,
		Text:   "# Release Notes\n\nShip it",
	}, nil)
	if err != nil {
		t.Fatalf("buildAPIPayload markdown error = %v", err)
	}
	if msgKey != "sampleMarkdown" {
		t.Fatalf("markdown msgKey = %q, want sampleMarkdown", msgKey)
	}
	assertJSONField(t, msgParam, "title", "Release Notes")
	assertJSONField(t, msgParam, "text", "# Release Notes\n\nShip it")
}

func TestBuildAPIPayloadRejectsEmptyMessage(t *testing.T) {
	if _, _, err := buildAPIPayload(channel.Message{}, nil); err == nil {
		t.Fatal("expected empty message error")
	}
}

func TestBuildAttachmentPayloadImageURLAndFileRef(t *testing.T) {
	msgKey, msgParam, err := buildAttachmentPayload(channel.PreparedAttachment{
		Kind:      channel.PreparedAttachmentPublicURL,
		PublicURL: "https://example.com/image.png",
		Logical:   channel.Attachment{Type: channel.AttachmentImage, Name: "image.png"},
	})
	if err != nil {
		t.Fatalf("buildAttachmentPayload image error = %v", err)
	}
	if msgKey != "sampleImageMsg" {
		t.Fatalf("image msgKey = %q, want sampleImageMsg", msgKey)
	}
	assertJSONField(t, msgParam, "photoURL", "https://example.com/image.png")

	msgKey, msgParam, err = buildAttachmentPayload(channel.PreparedAttachment{
		Kind:      channel.PreparedAttachmentNativeRef,
		NativeRef: "media-1",
		Logical: channel.Attachment{
			Type: channel.AttachmentFile,
			Name: "report.PDF",
		},
	})
	if err != nil {
		t.Fatalf("buildAttachmentPayload file error = %v", err)
	}
	if msgKey != "sampleFile" {
		t.Fatalf("file msgKey = %q, want sampleFile", msgKey)
	}
	assertJSONField(t, msgParam, "mediaId", "media-1")
	assertJSONField(t, msgParam, "fileName", "report.PDF")
	assertJSONField(t, msgParam, "fileType", "pdf")
}

func TestBuildAttachmentPayloadRequiresImageReference(t *testing.T) {
	if _, _, err := buildAttachmentPayload(channel.PreparedAttachment{
		Logical: channel.Attachment{Type: channel.AttachmentImage},
	}); err == nil {
		t.Fatal("expected missing image reference error")
	}
}

func TestBuildWebhookBody(t *testing.T) {
	body, err := buildWebhookBody(channel.Message{
		Format: channel.MessageFormatMarkdown,
		Text:   "## Status\nAll green",
	})
	if err != nil {
		t.Fatalf("buildWebhookBody error = %v", err)
	}
	if body["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v, want markdown", body["msgtype"])
	}
	markdown, ok := body["markdown"].(map[string]string)
	if !ok {
		t.Fatalf("markdown body has unexpected shape: %#v", body["markdown"])
	}
	if markdown["title"] != "Status" || markdown["text"] != "## Status\nAll green" {
		t.Fatalf("unexpected markdown payload: %#v", markdown)
	}

	body, err = buildWebhookBody(channel.Message{
		Attachments: []channel.Attachment{{Type: channel.AttachmentFile, Name: "report.pdf"}},
	})
	if err != nil {
		t.Fatalf("buildWebhookBody attachment fallback error = %v", err)
	}
	text, ok := body["text"].(map[string]string)
	if !ok || text["content"] != "[attachment]" {
		t.Fatalf("unexpected attachment fallback body: %#v", body)
	}
}

func TestExtractMarkdownTitleAndFileExt(t *testing.T) {
	if got := extractMarkdownTitle("# 1234567890123456789012345"); got != "12345678901234567890" {
		t.Fatalf("extractMarkdownTitle = %q, want first 20 runes", got)
	}
	if got := fileExtFromName("Report.PDF"); got != "pdf" {
		t.Fatalf("fileExtFromName = %q, want pdf", got)
	}
	if got := resolveFileType(channel.Attachment{Name: "archive.tar.gz"}); got != "gz" {
		t.Fatalf("resolveFileType = %q, want gz", got)
	}
}

func assertJSONField(t *testing.T, raw, key string, want string) {
	t.Helper()
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", raw, err)
	}
	if values[key] != want {
		t.Fatalf("json field %q = %q, want %q (raw %s)", key, values[key], want, raw)
	}
}
