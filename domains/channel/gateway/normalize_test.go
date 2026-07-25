package gateway

import "testing"

func TestInferAttachmentType(t *testing.T) {
	cases := []struct {
		name   string
		inType AttachmentType
		mime   string
		file   string
		want   AttachmentType
	}{
		{
			name:   "keep explicit image",
			inType: AttachmentImage,
			mime:   "",
			file:   "",
			want:   AttachmentImage,
		},
		{
			name:   "infer from image mime",
			inType: AttachmentFile,
			mime:   "image/jpeg",
			file:   "a.bin",
			want:   AttachmentImage,
		},
		{
			name:   "infer gif from mime",
			inType: AttachmentFile,
			mime:   "image/gif",
			file:   "a.bin",
			want:   AttachmentGIF,
		},
		{
			name:   "infer from audio extension",
			inType: AttachmentFile,
			mime:   "",
			file:   "a.mp3",
			want:   AttachmentAudio,
		},
		{
			name:   "infer from video extension",
			inType: AttachmentFile,
			mime:   "",
			file:   "a.mp4",
			want:   AttachmentVideo,
		},
		{
			name:   "fallback file",
			inType: AttachmentFile,
			mime:   "",
			file:   "a.unknown",
			want:   AttachmentFile,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InferAttachmentType(tc.inType, tc.mime, tc.file)
			if got != tc.want {
				t.Fatalf("InferAttachmentType got %q want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeInboundChannelAttachment(t *testing.T) {
	normalized := NormalizeInboundChannelAttachment(Attachment{
		Type:           AttachmentFile,
		Mime:           "IMAGE/JPEG; charset=utf-8",
		Name:           " photo.jpg ",
		URL:            " https://example.com/x ",
		PlatformKey:    " file_key_1 ",
		SourcePlatform: " feishu ",
		Caption:        " hello ",
	})
	if normalized.Type != AttachmentImage {
		t.Fatalf("expected inferred image type, got %q", normalized.Type)
	}
	if normalized.Mime != "image/jpeg" {
		t.Fatalf("expected normalized mime image/jpeg, got %q", normalized.Mime)
	}
	if normalized.Name != "photo.jpg" {
		t.Fatalf("expected trimmed name, got %q", normalized.Name)
	}
	if normalized.URL != "https://example.com/x" {
		t.Fatalf("expected trimmed url, got %q", normalized.URL)
	}
	if normalized.PlatformKey != "file_key_1" {
		t.Fatalf("expected trimmed platform key, got %q", normalized.PlatformKey)
	}
	if normalized.SourcePlatform != "feishu" {
		t.Fatalf("expected trimmed source platform, got %q", normalized.SourcePlatform)
	}
	if normalized.Caption != "hello" {
		t.Fatalf("expected trimmed caption, got %q", normalized.Caption)
	}
}

func TestNormalizeInboundChannelAttachment_DataURLMovesToBase64(t *testing.T) {
	normalized := NormalizeInboundChannelAttachment(Attachment{
		Type: AttachmentFile,
		URL:  "data:image/png;base64,AAAA",
	})

	if normalized.URL != "" {
		t.Fatalf("expected data url moved out of URL, got %q", normalized.URL)
	}
	if normalized.Base64 != "data:image/png;base64,AAAA" {
		t.Fatalf("expected normalized base64 data url, got %q", normalized.Base64)
	}
	if normalized.Mime != "image/png" {
		t.Fatalf("expected inferred mime image/png, got %q", normalized.Mime)
	}
	if normalized.Type != AttachmentImage {
		t.Fatalf("expected inferred image type, got %q", normalized.Type)
	}
}

func TestNormalizeInboundChannelAttachment_PreservesExplicitVoiceType(t *testing.T) {
	normalized := NormalizeInboundChannelAttachment(Attachment{
		Type: AttachmentVoice,
		URL:  "https://example.com/audio.ogg",
		Mime: "audio/ogg",
	})

	if normalized.Type != AttachmentVoice {
		t.Fatalf("expected explicit voice type preserved, got %q", normalized.Type)
	}
	if normalized.URL != "https://example.com/audio.ogg" {
		t.Fatalf("expected URL preserved, got %q", normalized.URL)
	}
	if normalized.Mime != "audio/ogg" {
		t.Fatalf("expected normalized mime audio/ogg, got %q", normalized.Mime)
	}
}
