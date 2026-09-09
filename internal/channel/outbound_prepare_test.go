package channel

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel/channeltest"
	"github.com/felinics/memoh/internal/media"
)

func TestPrepareOutboundMessage_ContainerPathFallsBackToIngestContainerFile(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	const sourcePath = "/data/.memoh/media/26da/missing.png"
	store.SeedContainerFile("bot-1", sourcePath, []byte("image-bytes"), "image/png", "missing.png")

	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelType("qq"),
	}, OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				Path: sourcePath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	if len(prepared.Message.Attachments) != 1 {
		t.Fatalf("expected 1 prepared attachment, got %d", len(prepared.Message.Attachments))
	}
	if len(prepared.Message.Message.Attachments) != 1 {
		t.Fatalf("expected 1 logical attachment, got %d", len(prepared.Message.Message.Attachments))
	}
	logical := prepared.Message.Message.Attachments[0]
	if logical.ContentHash == "" {
		t.Fatal("expected content hash after container fallback ingest")
	}
	if logical.Path == sourcePath {
		t.Fatalf("expected prepared access path, got original path %q", logical.Path)
	}
	if logical.Metadata["source_path"] != sourcePath {
		t.Fatalf("expected source_path metadata, got %#v", logical.Metadata["source_path"])
	}

	reader, err := prepared.Message.Attachments[0].Open(context.Background())
	if err != nil {
		t.Fatalf("open prepared attachment: %v", err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read prepared attachment: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Fatalf("unexpected prepared attachment bytes: %q", string(data))
	}
}

func TestPrepareStreamEvent_FailsFastOnAttachmentPreparationError(t *testing.T) {
	t.Parallel()

	_, err := PrepareStreamEvent(context.Background(), nil, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelType("qq"),
	}, StreamEvent{
		Type: StreamEventAttachment,
		Attachments: []Attachment{{
			Type: AttachmentImage,
			URL:  "https://example.com/image.png",
		}},
	})
	if err == nil {
		t.Fatal("expected attachment preparation error")
	}
	if !strings.Contains(err.Error(), "attachment store is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareStreamEvent_FailsFastOnFinalAttachmentPreparationError(t *testing.T) {
	t.Parallel()

	_, err := PrepareStreamEvent(context.Background(), nil, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelType("qq"),
	}, StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Attachments: []Attachment{{
					Type: AttachmentImage,
					URL:  "https://example.com/image.png",
				}},
			},
		},
	})
	if err == nil {
		t.Fatal("expected final attachment preparation error")
	}
	if !strings.Contains(err.Error(), "attachment store is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- NativeRef ---

func TestPrepareOutboundMessage_TelegramNativeRef(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeTelegram,
	}, OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Attachments: []Attachment{{
				Type:           AttachmentImage,
				PlatformKey:    "AgACAgIAAxkB",
				SourcePlatform: "telegram",
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentNativeRef {
		t.Fatalf("expected native_ref, got %s", att.Kind)
	}
	if att.NativeRef != "AgACAgIAAxkB" {
		t.Fatalf("unexpected native ref: %q", att.NativeRef)
	}
}

func TestPrepareOutboundMessage_DingtalkNativeRefSkipsImageAndGIF(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	_, seedErr := store.SeedAsset("bot-1", []byte("gif-data"), "image/gif", ".gif")
	if seedErr != nil {
		t.Fatalf("seed asset: %v", seedErr)
	}

	for _, attType := range []AttachmentType{AttachmentImage, AttachmentGIF} {
		attType := attType
		t.Run(string(attType), func(t *testing.T) {
			t.Parallel()

			asset, err := store.SeedAsset("bot-1", []byte("data-"+string(attType)), "image/png", ".png")
			if err != nil {
				t.Fatalf("seed: %v", err)
			}

			prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
				BotID:       "bot-1",
				ChannelType: ChannelTypeDingtalk,
			}, OutboundMessage{
				Target: "chat-1",
				Message: Message{
					Attachments: []Attachment{{
						Type:           attType,
						ContentHash:    asset.ContentHash,
						PlatformKey:    "dingtalk-key",
						SourcePlatform: "dingtalk",
					}},
				},
			})
			if err != nil {
				t.Fatalf("PrepareOutboundMessage failed: %v", err)
			}
			att := prepared.Message.Attachments[0]
			if att.Kind == PreparedAttachmentNativeRef {
				t.Fatalf("DingTalk image/gif should NOT use native ref, got native_ref")
			}
		})
	}
}

func TestPrepareOutboundMessage_MatrixNativeRefFromMXCURL(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeMatrix,
	}, OutboundMessage{
		Target: "!room:matrix.org",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  "mxc://matrix.org/AbcDef",
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentNativeRef {
		t.Fatalf("expected native_ref for mxc:// URL, got %s", att.Kind)
	}
	if att.NativeRef != "mxc://matrix.org/AbcDef" {
		t.Fatalf("unexpected native ref: %q", att.NativeRef)
	}
}

// --- PublicURL ---

func TestPrepareOutboundMessage_TelegramPublicURLPassthrough(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	const rawURL = "https://example.com/photo.jpg"
	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeTelegram,
	}, OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  rawURL,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentPublicURL {
		t.Fatalf("expected public_url, got %s", att.Kind)
	}
	if att.PublicURL != rawURL {
		t.Fatalf("unexpected public URL: %q", att.PublicURL)
	}
}

func TestPrepareOutboundMessage_LINEPublicURLPassthroughOnlyForDirectlySendableURL(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	const rawURL = "https://example.com/photo.jpg"
	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeLine,
	}, OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  rawURL,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentPublicURL {
		t.Fatalf("expected public_url, got %s", att.Kind)
	}
	if att.PublicURL != rawURL {
		t.Fatalf("unexpected public URL: %q", att.PublicURL)
	}
}

func TestPrepareOutboundMessage_LINEHTTPURLFallsBackToDownloadAndUpload(t *testing.T) {
	t.Parallel()

	imgData := []byte("fake-image-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Disposition", `attachment; filename="photo.jpg"`)
		_, _ = w.Write(imgData)
	}))
	defer srv.Close()

	store := channeltest.NewMemoryAttachmentStore()
	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeLine,
	}, OutboundMessage{
		Target: "chat-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  srv.URL + "/photo.jpg",
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload after HTTP download, got %s", att.Kind)
	}
	if att.Mime != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", att.Mime)
	}
}

func TestPrepareOutboundMessage_DingtalkPublicURLOnlyForImageAndGIF(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()

	t.Run("image allowed", func(t *testing.T) {
		t.Parallel()
		prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
			BotID:       "bot-1",
			ChannelType: ChannelTypeDingtalk,
		}, OutboundMessage{
			Target: "chat-1",
			Message: Message{
				Attachments: []Attachment{{
					Type: AttachmentImage,
					URL:  "https://example.com/img.png",
				}},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if prepared.Message.Attachments[0].Kind != PreparedAttachmentPublicURL {
			t.Fatalf("expected public_url for DingTalk image, got %s", prepared.Message.Attachments[0].Kind)
		}
	})

	t.Run("file not allowed", func(t *testing.T) {
		t.Parallel()
		_, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
			BotID:       "bot-1",
			ChannelType: ChannelTypeDingtalk,
		}, OutboundMessage{
			Target: "chat-1",
			Message: Message{
				Attachments: []Attachment{{
					Type: AttachmentFile,
					URL:  "https://example.com/doc.pdf",
				}},
			},
		})
		// File with HTTP URL on DingTalk without store support should fail.
		if err == nil {
			t.Fatal("expected error for DingTalk file with public URL")
		}
	})
}

// --- Base64 ---

func TestPrepareOutboundMessage_Base64Attachment(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	raw := []byte("png-data")
	encoded := base64.StdEncoding.EncodeToString(raw)
	dataURL := "data:image/png;base64," + encoded

	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  dataURL,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload, got %s", att.Kind)
	}
	if att.Mime != "image/png" {
		t.Fatalf("expected image/png mime, got %q", att.Mime)
	}
	reader, err := att.Open(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("unexpected bytes: %q", got)
	}
}

func TestPrepareOutboundMessage_Base64FieldAttachment(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	raw := []byte("audio-data")
	encoded := base64.StdEncoding.EncodeToString(raw)

	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type:   AttachmentAudio,
				Base64: encoded,
				Mime:   "audio/mpeg",
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload, got %s", att.Kind)
	}
}

// --- HTTP download ---

func TestPrepareOutboundMessage_HTTPAttachmentDownload(t *testing.T) {
	t.Parallel()

	imgData := []byte("fake-image-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Disposition", `attachment; filename="photo.jpg"`)
		_, _ = w.Write(imgData)
	}))
	defer srv.Close()

	store := channeltest.NewMemoryAttachmentStore()
	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  srv.URL + "/photo.jpg",
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload after HTTP download, got %s", att.Kind)
	}
	if att.Mime != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", att.Mime)
	}
	if att.Name != "photo.jpg" {
		t.Fatalf("expected filename from Content-Disposition, got %q", att.Name)
	}

	reader, err := att.Open(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(imgData) {
		t.Fatalf("unexpected bytes: %q", got)
	}
}

func TestPrepareOutboundMessage_HTTPAttachmentBadStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	store := channeltest.NewMemoryAttachmentStore()
	_, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				URL:  srv.URL + "/missing.jpg",
			}},
		},
	})
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- ContentHash (persisted asset) ---

func TestPrepareOutboundMessage_PersistedContentHash(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	asset, err := store.SeedAsset("bot-1", []byte("video-data"), "video/mp4", ".mp4")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type:        AttachmentVideo,
				ContentHash: asset.ContentHash,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	att := prepared.Message.Attachments[0]
	if att.Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload for persisted hash, got %s", att.Kind)
	}
	logical := prepared.Message.Message.Attachments[0]
	if logical.ContentHash != asset.ContentHash {
		t.Fatalf("content hash mismatch: got %q, want %q", logical.ContentHash, asset.ContentHash)
	}
}

// --- Container path (already stored) ---

func TestPrepareOutboundMessage_ContainerPathHitStorageKey(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	asset, err := store.SeedAsset("bot-1", []byte("already-ingested"), "image/png", ".png")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	accessPath := store.AccessPath(context.Background(), asset)

	prepared, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				Path: accessPath,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PrepareOutboundMessage failed: %v", err)
	}
	if prepared.Message.Attachments[0].Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload, got %s", prepared.Message.Attachments[0].Kind)
	}
	if prepared.Message.Message.Attachments[0].ContentHash != asset.ContentHash {
		t.Fatal("content hash should match the already-ingested asset")
	}
}

// --- GIF type inference ---

func TestPreparedAttachmentTypeFromMime_GIFIsDistinctFromImage(t *testing.T) {
	t.Parallel()

	got := preparedAttachmentTypeFromMime("image/gif")
	if got != AttachmentGIF {
		t.Fatalf("expected AttachmentGIF for image/gif, got %s", got)
	}
	got = preparedAttachmentTypeFromMime("image/png")
	if got != AttachmentImage {
		t.Fatalf("expected AttachmentImage for image/png, got %s", got)
	}
}

// --- Helper utilities ---

func TestExtractPreparedStorageKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want string
	}{
		{"/data/.memoh/media/ab/abcdef.png", "ab/abcdef.png"},
		{"/other/media/ab/abcdef.png", ""},
		{"/data/.memoh/media/", ""},
		{"/data/.memoh/media", ""},
		{"ab/abcdef.png", ""},
	}
	for _, tc := range cases {
		got := extractPreparedStorageKey(tc.path)
		if got != tc.want {
			t.Errorf("extractPreparedStorageKey(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestIsDataURL(t *testing.T) {
	t.Parallel()

	if !IsDataURL("data:image/png;base64,abc") {
		t.Error("expected true for data: URL")
	}
	if !IsDataURL("DATA:image/png;base64,abc") {
		t.Error("expected true for uppercase DATA: URL")
	}
	if IsDataURL("https://example.com") {
		t.Error("expected false for https URL")
	}
	if IsDataURL("") {
		t.Error("expected false for empty string")
	}
}

func TestIsHTTPURL(t *testing.T) {
	t.Parallel()

	if !IsHTTPURL("http://example.com") {
		t.Error("expected true for http://")
	}
	if !IsHTTPURL("HTTPS://example.com") {
		t.Error("expected true for HTTPS://")
	}
	if IsHTTPURL("data:image/png;base64,abc") {
		t.Error("expected false for data: URL")
	}
	if IsHTTPURL("/data/.memoh/media/file.png") {
		t.Error("expected false for local path")
	}
}

func TestIsDataPath(t *testing.T) {
	t.Parallel()

	if !IsDataPath("/data/.memoh/media/file.png") {
		t.Error("expected true for /data/ path")
	}
	if IsDataPath("/tmp/file.png") {
		t.Error("expected false for /tmp/ path")
	}
}

// --- PrepareStreamEvent happy path ---

func TestPrepareStreamEvent_FinalWithAttachment(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	asset, err := store.SeedAsset("bot-1", []byte("img"), "image/png", ".png")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ev, err := PrepareStreamEvent(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, StreamEvent{
		Type: StreamEventFinal,
		Final: &StreamFinalizePayload{
			Message: Message{
				Text: "done",
				Attachments: []Attachment{{
					Type:        AttachmentImage,
					ContentHash: asset.ContentHash,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("PrepareStreamEvent failed: %v", err)
	}
	if ev.Final == nil {
		t.Fatal("expected non-nil Final")
	}
	if len(ev.Final.Message.Attachments) != 1 {
		t.Fatalf("expected 1 prepared attachment in Final, got %d", len(ev.Final.Message.Attachments))
	}
	if ev.Final.Message.Attachments[0].Kind != PreparedAttachmentUpload {
		t.Fatalf("expected upload kind, got %s", ev.Final.Message.Attachments[0].Kind)
	}
}

func TestPrepareStreamEvent_AttachmentEventHappyPath(t *testing.T) {
	t.Parallel()

	store := channeltest.NewMemoryAttachmentStore()
	asset, err := store.SeedAsset("bot-1", []byte("audio"), "audio/mpeg", ".mp3")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ev, err := PrepareStreamEvent(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, StreamEvent{
		Type: StreamEventAttachment,
		Attachments: []Attachment{{
			Type:        AttachmentAudio,
			ContentHash: asset.ContentHash,
		}},
	})
	if err != nil {
		t.Fatalf("PrepareStreamEvent failed: %v", err)
	}
	if len(ev.Attachments) != 1 {
		t.Fatalf("expected 1 prepared attachment, got %d", len(ev.Attachments))
	}
}

// --- ContainerAttachmentIngester optional interface ---

// noContainerIngesterStore satisfies OutboundAttachmentStore but deliberately
// does NOT implement ContainerAttachmentIngester. GetByStorageKey always
// returns not-found so prepareContainerAttachment reaches the type assertion.
type noContainerIngesterStore struct{}

func (noContainerIngesterStore) Stat(_ context.Context, _, _ string) (media.Asset, error) {
	return media.Asset{}, media.ErrAssetNotFound
}

func (noContainerIngesterStore) Open(_ context.Context, _, _ string) (io.ReadCloser, media.Asset, error) {
	return nil, media.Asset{}, media.ErrAssetNotFound
}

func (noContainerIngesterStore) Ingest(_ context.Context, _ media.IngestInput) (media.Asset, error) {
	return media.Asset{}, nil
}

func (noContainerIngesterStore) GetByStorageKey(_ context.Context, _, _ string) (media.Asset, error) {
	return media.Asset{}, media.ErrAssetNotFound
}
func (noContainerIngesterStore) AccessPath(_ context.Context, _ media.Asset) string { return "" }

func TestPrepareContainerAttachment_StoreWithoutIngesterFails(t *testing.T) {
	t.Parallel()

	var store OutboundAttachmentStore = noContainerIngesterStore{}

	_, err := PrepareOutboundMessage(context.Background(), store, ChannelConfig{
		BotID:       "bot-1",
		ChannelType: ChannelTypeDiscord,
	}, OutboundMessage{
		Target: "chan-1",
		Message: Message{
			Attachments: []Attachment{{
				Type: AttachmentImage,
				// Path that won't match any storage key so it falls through to IngestContainerFile.
				Path: "/data/nonexistent/unique/file.png",
			}},
		},
	})
	if err == nil {
		t.Fatal("expected error when store does not implement ContainerAttachmentIngester")
	}
	if !strings.Contains(err.Error(), "does not support workspace file ingestion") {
		t.Fatalf("unexpected error: %v", err)
	}
}
