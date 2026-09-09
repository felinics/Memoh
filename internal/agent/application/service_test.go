package application

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/models"
)

type fakeGatewayAssetLoader struct {
	openFn       func(ctx context.Context, botID, contentHash string) (io.ReadCloser, string, error)
	accessPathFn func(ctx context.Context, botID, contentHash string) (string, error)
}

func (f *fakeGatewayAssetLoader) AccessPathForGateway(ctx context.Context, botID, contentHash string) (string, error) {
	if f == nil || f.accessPathFn == nil {
		return "", io.EOF
	}
	return f.accessPathFn(ctx, botID, contentHash)
}

func (f *fakeGatewayAssetLoader) OpenForGateway(ctx context.Context, botID, contentHash string) (io.ReadCloser, string, error) {
	if f == nil || f.openFn == nil {
		return nil, "", io.EOF
	}
	return f.openFn(ctx, botID, contentHash)
}

func TestPrepareGatewayAttachments_InlineAssetToBase64(t *testing.T) {
	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(_ context.Context, _, contentHash string) (io.ReadCloser, string, error) {
				if contentHash != "asset-1" {
					t.Fatalf("unexpected content hash: %s", contentHash)
				}
				return io.NopCloser(strings.NewReader("image-binary")), "image/png", nil
			},
		},
	}
	req := ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{
			{
				Type:        "image",
				ContentHash: "asset-1",
			},
		},
	}

	prepared := resolver.prepareGatewayAttachments(context.Background(), req)
	if len(prepared) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(prepared))
	}
	if prepared[0].Transport != gatewayTransportInlineDataURL {
		t.Fatalf("expected inline transport, got %q", prepared[0].Transport)
	}
	if !strings.HasPrefix(prepared[0].Payload, "data:image/png;base64,") {
		t.Fatalf("expected data url image attachment, got %q", prepared[0].Payload)
	}
	if prepared[0].Mime != "image/png" {
		t.Fatalf("expected mime image/png, got %q", prepared[0].Mime)
	}
}

func TestPrepareRuntimeImagesInlineStoredAsset(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(_ context.Context, botID, contentHash string) (io.ReadCloser, string, error) {
				if botID != "bot-1" || contentHash != "asset-1" {
					t.Fatalf("unexpected asset lookup: bot=%q hash=%q", botID, contentHash)
				}
				return io.NopCloser(strings.NewReader("image-binary")), "image/png", nil
			},
		},
	}
	prepared, err := resolver.prepareRuntimeAttachments(context.Background(), ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{{
			Type:        "image",
			ContentHash: "asset-1",
			Name:        "screenshot.png",
		}},
	})
	if err != nil {
		t.Fatalf("prepareRuntimeAttachments() error = %v", err)
	}
	images := prepared.Images
	if len(images) != 1 {
		t.Fatalf("prepareRuntimeAttachments().Images = %#v, want one image", images)
	}
	if !bytes.Equal(images[0].Data, []byte("image-binary")) || images[0].MimeType != "image/png" {
		t.Fatalf("prepared image = %#v, want inline PNG", images[0])
	}
}

func TestPrepareGatewayAttachments_DataURLFromURLFieldIsNativeInline(t *testing.T) {
	resolver := &Service{logger: slog.Default()}
	req := ChatRequest{
		Attachments: []ChatAttachment{
			{
				Type: "image",
				URL:  "data:image/png;base64,AAAA",
			},
		},
	}

	prepared := resolver.prepareGatewayAttachments(context.Background(), req)
	if len(prepared) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(prepared))
	}
	if prepared[0].Transport != gatewayTransportInlineDataURL {
		t.Fatalf("expected inline transport, got %q", prepared[0].Transport)
	}
	if prepared[0].Payload != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected payload: %q", prepared[0].Payload)
	}
	if prepared[0].FallbackPath != "" {
		t.Fatalf("expected empty fallback path, got %q", prepared[0].FallbackPath)
	}
}

func TestPrepareGatewayAttachments_PublicURLFromURLFieldIsNativePublic(t *testing.T) {
	resolver := &Service{logger: slog.Default()}
	req := ChatRequest{
		Attachments: []ChatAttachment{
			{
				Type: "image",
				URL:  "https://example.com/demo.png",
			},
		},
	}

	prepared := resolver.prepareGatewayAttachments(context.Background(), req)
	if len(prepared) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(prepared))
	}
	if prepared[0].Transport != gatewayTransportPublicURL {
		t.Fatalf("expected public transport, got %q", prepared[0].Transport)
	}
	if prepared[0].Payload != "https://example.com/demo.png" {
		t.Fatalf("unexpected payload: %q", prepared[0].Payload)
	}
	if prepared[0].FallbackPath != "" {
		t.Fatalf("expected empty fallback path, got %q", prepared[0].FallbackPath)
	}
}

func TestRouteAndMergeAttachments_ImagePathOnlyFallsBackToFile(t *testing.T) {
	resolver := &Service{logger: slog.Default()}
	model := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				Compatibilities: []string{models.CompatVision},
			},
		},
	}
	req := ChatRequest{
		Attachments: []ChatAttachment{
			{
				Type: "image",
				Path: "/data/.memoh/media/image/demo.png",
			},
		},
	}

	merged := resolver.routeAndMergeAttachments(context.Background(), model, req)
	if len(merged) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(merged))
	}
	item, ok := merged[0].(gatewayAttachment)
	if !ok {
		t.Fatalf("expected gatewayAttachment type")
	}
	if item.Type != "file" {
		t.Fatalf("expected fallback type file, got %q", item.Type)
	}
	if item.Transport != gatewayTransportToolFileRef {
		t.Fatalf("expected tool_file_ref transport, got %q", item.Transport)
	}
	if item.Payload != "/data/.memoh/media/image/demo.png" {
		t.Fatalf("unexpected fallback payload: %q", item.Payload)
	}
}

func TestPrepareGatewayAttachments_IncludesReplyAttachments(t *testing.T) {
	resolver := &Service{logger: slog.Default()}
	req := ChatRequest{
		Attachments: []ChatAttachment{
			{Type: "image", URL: "https://example.com/current.png"},
		},
		ReplyAttachments: []ChatAttachment{
			{Type: "image", URL: "https://example.com/reply.png"},
		},
	}

	prepared := resolver.prepareGatewayAttachments(context.Background(), req)
	if len(prepared) != 2 {
		t.Fatalf("expected current and reply attachments, got %d", len(prepared))
	}
	if prepared[0].Payload != "https://example.com/current.png" || prepared[1].Payload != "https://example.com/reply.png" {
		t.Fatalf("unexpected prepared attachments: %#v", prepared)
	}
}

func TestPrepareGatewayAttachments_ResolvesStoredFileAccessPath(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			accessPathFn: func(_ context.Context, botID, contentHash string) (string, error) {
				if botID != "bot-1" || contentHash != "asset-pdf" {
					t.Fatalf("unexpected asset lookup: bot=%q hash=%q", botID, contentHash)
				}
				return "/data/.memoh/media/aa/asset.pdf", nil
			},
		},
	}
	req := ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{{
			Type:        "file",
			Mime:        "application/pdf",
			ContentHash: "asset-pdf",
		}},
	}

	prepared := resolver.prepareGatewayAttachments(context.Background(), req)
	if len(prepared) != 1 || prepared[0].FallbackPath != "/data/.memoh/media/aa/asset.pdf" {
		t.Fatalf("prepared attachments = %#v, want reachable PDF path", prepared)
	}
	merged := resolver.routeAndMergeAttachments(context.Background(), models.GetResponse{}, req)
	if len(merged) != 1 {
		t.Fatalf("routeAndMergeAttachments() length = %d, want 1", len(merged))
	}
	item, ok := merged[0].(gatewayAttachment)
	if !ok || item.Transport != gatewayTransportToolFileRef || item.Payload != "/data/.memoh/media/aa/asset.pdf" {
		t.Fatalf("merged attachment = %#v, want tool file reference", merged[0])
	}
}

func TestPrepareACPAttachments_UsesFileAndReplyReferences(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			accessPathFn: func(_ context.Context, _, contentHash string) (string, error) {
				if contentHash != "asset-pdf" {
					t.Fatalf("unexpected content hash: %s", contentHash)
				}
				return "/data/.memoh/media/aa/asset.pdf", nil
			},
		},
	}
	prepared, err := resolver.prepareRuntimeAttachments(context.Background(), ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{{
			Type:        "file",
			Name:        "spec.pdf",
			Mime:        "application/pdf",
			ContentHash: "asset-pdf",
		}},
		ReplyAttachments: []ChatAttachment{{
			Type: "image",
			Name: "old.png",
			URL:  "https://example.com/old.png",
		}},
	})
	if err != nil {
		t.Fatalf("prepareRuntimeAttachments() error = %v", err)
	}
	if len(prepared.Images) != 0 || len(prepared.Context) != 2 || len(prepared.References) != 2 {
		t.Fatalf("prepared attachments = %#v, want two file references", prepared)
	}
	if prepared.Context[0].Path != "/data/.memoh/media/aa/asset.pdf" || prepared.Context[1].URL != "https://example.com/old.png" {
		t.Fatalf("context attachments = %#v, want PDF path and reply URL", prepared.Context)
	}
}

func TestPrepareACPAttachments_PreservesLongPasteFile(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			accessPathFn: func(_ context.Context, _, contentHash string) (string, error) {
				if contentHash != "pasted-text-hash" {
					t.Fatalf("unexpected content hash: %s", contentHash)
				}
				return "/data/.memoh/media/aa/pasted-text.txt", nil
			},
		},
	}
	prepared, err := resolver.prepareRuntimeAttachments(context.Background(), ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{{
			Type:        "file",
			Name:        "pasted-text.txt",
			Mime:        "text/plain",
			ContentHash: "pasted-text-hash",
		}},
	})
	if err != nil {
		t.Fatalf("prepareRuntimeAttachments() error = %v", err)
	}
	if len(prepared.References) != 1 || prepared.Context[0].Path != "/data/.memoh/media/aa/pasted-text.txt" {
		t.Fatalf("prepared attachments = %#v, want pasted text path", prepared)
	}
}

func TestPrepareACPAttachments_FallsBackWhenStoredImageCannotInline(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(context.Context, string, string) (io.ReadCloser, string, error) {
				return nil, "", errors.New("asset too large")
			},
			accessPathFn: func(context.Context, string, string) (string, error) {
				return "/data/.memoh/media/aa/large.png", nil
			},
		},
	}
	prepared, err := resolver.prepareRuntimeAttachments(context.Background(), ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{{
			Type:        "image",
			Name:        "large.png",
			ContentHash: "asset-image",
		}},
	})
	if err != nil {
		t.Fatalf("prepareRuntimeAttachments() error = %v", err)
	}
	if len(prepared.Images) != 0 || len(prepared.References) != 1 || prepared.Context[0].Path != "/data/.memoh/media/aa/large.png" {
		t.Fatalf("prepared attachments = %#v, want image file fallback", prepared)
	}
}

func TestPrepareACPAttachments_RejectsInvalidOrUnreachableData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolver *Service
		input    ChatAttachment
		wantCode string
	}{
		{
			name:     "invalid image base64",
			resolver: &Service{logger: slog.Default()},
			input: ChatAttachment{
				Type:   "image",
				Name:   "broken.png",
				Base64: "data:image/png;base64,not-valid***",
			},
			wantCode: agentfeedback.CodeAttachmentInvalid,
		},
		{
			name: "stored file without reachable path",
			resolver: &Service{
				logger: slog.Default(),
				assetLoader: &fakeGatewayAssetLoader{
					accessPathFn: func(context.Context, string, string) (string, error) {
						return "", errors.New("missing")
					},
				},
			},
			input: ChatAttachment{
				Type:        "file",
				Name:        "missing.pdf",
				ContentHash: "missing",
			},
			wantCode: agentfeedback.CodeAttachmentUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.resolver.prepareRuntimeAttachments(context.Background(), ChatRequest{
				BotID:       "bot-1",
				Attachments: []ChatAttachment{tt.input},
			})
			var feedback *agentfeedback.Error
			if !errors.As(err, &feedback) || feedback.Code != tt.wantCode || feedback.HTTPStatus != 400 {
				t.Fatalf("error = %#v, want feedback code %q with status 400", err, tt.wantCode)
			}
		})
	}
}

func TestPrepareGatewayAttachments_DetectsImageMimeWhenOctetStream(t *testing.T) {
	jpegBytes := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46,
		0x49, 0x46, 0x00, 0x01, 0xFF, 0xD9,
	}
	resolver := &Service{
		logger: slog.Default(),
		assetLoader: &fakeGatewayAssetLoader{
			openFn: func(_ context.Context, _, _ string) (io.ReadCloser, string, error) {
				return io.NopCloser(bytes.NewReader(jpegBytes)), "application/octet-stream", nil
			},
		},
	}
	req := ChatRequest{
		BotID: "bot-1",
		Attachments: []ChatAttachment{
			{
				Type:        "image",
				ContentHash: "asset-2",
			},
		},
	}

	prepared := resolver.prepareGatewayAttachments(context.Background(), req)
	if len(prepared) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(prepared))
	}
	if prepared[0].Transport != gatewayTransportInlineDataURL {
		t.Fatalf("expected inline transport, got %q", prepared[0].Transport)
	}
	if !strings.HasPrefix(prepared[0].Payload, "data:image/jpeg;base64,") {
		t.Fatalf("expected detected image/jpeg data url, got %q", prepared[0].Payload)
	}
	if prepared[0].Mime != "image/jpeg" {
		t.Fatalf("expected mime image/jpeg, got %q", prepared[0].Mime)
	}
}

func TestRouteAndMergeAttachments_DropsUnsupportedInlineWithoutFallbackPath(t *testing.T) {
	resolver := &Service{logger: slog.Default()}
	model := models.GetResponse{
		Model: models.Model{
			Config: models.ModelConfig{
				Compatibilities: []string{},
			},
		},
	}
	req := ChatRequest{
		Attachments: []ChatAttachment{
			{
				Type:   "video",
				Base64: "AAAA",
			},
		},
	}

	merged := resolver.routeAndMergeAttachments(context.Background(), model, req)
	if len(merged) != 0 {
		t.Fatalf("expected unsupported inline attachment to be dropped, got %d", len(merged))
	}
}

func TestEncodeReaderAsDataURL_DetectsImageMime(t *testing.T) {
	jpegBytes := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46,
		0x49, 0x46, 0x00, 0x01, 0xFF, 0xD9,
	}

	dataURL, mime, err := encodeReaderAsDataURL(
		bytes.NewReader(jpegBytes),
		int64(len(jpegBytes)),
		"image",
		"application/octet-stream",
	)
	if err != nil {
		t.Fatalf("encodeReaderAsDataURL returned error: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("expected image/jpeg mime, got %q", mime)
	}
	expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
	if dataURL != expected {
		t.Fatalf("unexpected data URL")
	}
}

func TestEncodeReaderAsDataURL_RejectsOversizedPayload(t *testing.T) {
	_, _, err := encodeReaderAsDataURL(strings.NewReader("12345"), 4, "image", "image/png")
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
	if !strings.Contains(err.Error(), "asset too large to inline") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutboundAssetRefsToMessageRefs(t *testing.T) {
	t.Parallel()
	refs := []OutboundAssetRef{
		{ContentHash: "a1", Role: "attachment", Ordinal: 0},
		{ContentHash: "", Role: "attachment", Ordinal: 1},
		{ContentHash: "a2", Ordinal: 2},
	}
	result := outboundAssetRefsToMessageRefs(refs)
	if len(result) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(result))
	}
	if result[0].ContentHash != "a1" || result[0].Role != "attachment" {
		t.Fatalf("unexpected ref[0]: %+v", result[0])
	}
	if result[1].ContentHash != "a2" || result[1].Role != "attachment" {
		t.Fatalf("unexpected ref[1]: %+v", result[1])
	}
}

func TestOutboundAssetRefsToMessageRefs_Empty(t *testing.T) {
	t.Parallel()
	result := outboundAssetRefsToMessageRefs(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestSanitizeMessagesNormalizesUserMultipartImageBytes(t *testing.T) {
	t.Parallel()
	content, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "> quoted reply\n\nWhere is Antelope Canyon?"},
		{"type": "image", "image": map[string]any{"0": 137, "1": 80}, "mediaType": "image/png"},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	cleaned := sanitizeMessages([]ModelMessage{{
		Role:    "user",
		Content: content,
	}})
	if len(cleaned) != 1 {
		t.Fatalf("expected 1 message, got %d", len(cleaned))
	}
	if bytes.Equal(cleaned[0].Content, content) {
		t.Fatalf("expected user multipart content to be normalized")
	}
	var parts []map[string]any
	if err := json.Unmarshal(cleaned[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal normalized content: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts after normalization, got %d", len(parts))
	}
	if got := parts[0]["text"]; got != "> quoted reply\n\nWhere is Antelope Canyon?" {
		t.Fatalf("unexpected text part: %#v", got)
	}
	image, _ := parts[1]["image"].(string)
	if !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatalf("expected data URL image payload, got %#v", parts[1]["image"])
	}
}

func TestSanitizeMessagesKeepsAssistantMultipartMessages(t *testing.T) {
	t.Parallel()
	content, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "answer"},
		{"type": "image", "image": "data:image/png;base64,aGVsbG8="},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	cleaned := sanitizeMessages([]ModelMessage{{
		Role:    "assistant",
		Content: content,
	}})
	if len(cleaned) != 1 {
		t.Fatalf("expected 1 message, got %d", len(cleaned))
	}
	if !bytes.Equal(cleaned[0].Content, content) {
		t.Fatalf("assistant multipart content should remain unchanged")
	}
}

func TestNormalizeImagePartsToDataURL_ConvertsIndexedObject(t *testing.T) {
	msg := ModelMessage{
		Role: "user",
		Content: json.RawMessage(`[
			{"type":"text","text":"hello"},
			{"type":"image","image":{"0":82,"1":73,"2":70,"3":70},"mediaType":"image/webp"}
		]`),
	}

	normalized, changed := normalizeImagePartsToDataURL(msg)
	if !changed {
		t.Fatal("expected message to be normalized")
	}

	var parts []map[string]any
	if err := json.Unmarshal(normalized.Content, &parts); err != nil {
		t.Fatalf("failed to unmarshal normalized content: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	image, ok := parts[1]["image"].(string)
	if !ok {
		t.Fatalf("expected image to be string data url, got %T", parts[1]["image"])
	}
	expected := "data:image/webp;base64," + base64.StdEncoding.EncodeToString([]byte{82, 73, 70, 70})
	if image != expected {
		t.Fatalf("unexpected data url, got %q", image)
	}
}

func TestNormalizeImagePartsToDataURL_LeavesStringImageUntouched(t *testing.T) {
	original := `[
		{"type":"image","image":"data:image/png;base64,AAAA","mediaType":"image/png"}
	]`
	msg := ModelMessage{
		Role:    "user",
		Content: json.RawMessage(original),
	}

	normalized, changed := normalizeImagePartsToDataURL(msg)
	if changed {
		t.Fatal("expected no normalization for string image")
	}
	if string(normalized.Content) != original {
		t.Fatalf("expected content unchanged, got %s", string(normalized.Content))
	}
}
