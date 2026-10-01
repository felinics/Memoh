// Derived from @tencent-weixin/openclaw-weixin (MIT License, Copyright (c) 2026 Tencent Inc.)
// See LICENSE in this directory for the full license text.

package weixin

import (
	"context"
	"crypto/md5" //nolint:gosec
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/media"
)

type assetOpener interface {
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
}

// sendText sends a plain text message through the WeChat API.
func sendText(ctx context.Context, client *Client, cfg adapterConfig, target, text, contextToken string) error {
	if strings.TrimSpace(contextToken) == "" {
		return errors.New("weixin: context_token is required to send messages")
	}
	clientID := generateClientID()
	req := SendMessageRequest{
		Msg: WeixinMessage{
			ToUserID:     target,
			ClientID:     clientID,
			MessageType:  MessageTypeBot,
			MessageState: MessageStateFinish,
			ItemList: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: text}},
			},
			ContextToken: contextToken,
		},
	}
	return client.SendMessage(ctx, cfg, req)
}

// sendImageFromReader uploads an image and sends it.
func sendImageFromReader(ctx context.Context, client *Client, cfg adapterConfig, target, contextToken, text string, r io.Reader, size int64, rawMD5 string, logger *slog.Logger) error {
	return sendMediaFromReader(ctx, client, cfg, target, contextToken, text, "", r, size, rawMD5, UploadMediaImage, ItemTypeImage, logger)
}

// sendFileFromReader uploads a file and sends it.
func sendFileFromReader(ctx context.Context, client *Client, cfg adapterConfig, target, contextToken, text, fileName string, r io.Reader, size int64, rawMD5 string, logger *slog.Logger) error {
	return sendMediaFromReader(ctx, client, cfg, target, contextToken, text, fileName, r, size, rawMD5, UploadMediaFile, ItemTypeFile, logger)
}

func sendMediaFromReader(ctx context.Context, client *Client, cfg adapterConfig, target, contextToken, text, fileName string, plaintext io.Reader, rawSize int64, rawMD5Hex string, uploadType, itemType int, logger *slog.Logger) error {
	if strings.TrimSpace(contextToken) == "" {
		return errors.New("weixin: context_token is required for media send")
	}
	if rawSize < 0 || rawSize > media.MaxAssetBytes {
		return media.ErrAssetTooLarge
	}
	if decoded, err := hex.DecodeString(rawMD5Hex); err != nil || len(decoded) != md5.Size {
		return errors.New("weixin: valid raw MD5 is required for media send")
	}

	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return fmt.Errorf("weixin: gen aes key: %w", err)
	}
	filekey := make([]byte, 16)
	if _, err := rand.Read(filekey); err != nil {
		return fmt.Errorf("weixin: gen filekey: %w", err)
	}
	filekeyHex := hex.EncodeToString(filekey)
	fileSize := aesECBPaddedSize(int(rawSize))

	uploadResp, err := client.GetUploadURL(ctx, cfg, GetUploadURLRequest{
		FileKey:     filekeyHex,
		MediaType:   uploadType,
		ToUserID:    target,
		RawSize:     int(rawSize),
		RawFileMD5:  rawMD5Hex,
		FileSize:    fileSize,
		NoNeedThumb: true,
		AESKey:      hex.EncodeToString(aesKey),
	})
	if err != nil {
		return fmt.Errorf("weixin: get upload url: %w", err)
	}
	if strings.TrimSpace(uploadResp.UploadFullURL) == "" && strings.TrimSpace(uploadResp.UploadParam) == "" {
		return errors.New("weixin: getuploadurl returned neither upload_full_url nor upload_param")
	}

	downloadParam, err := uploadToCDNReader(ctx, cfg.CDNBaseURL, uploadResp, filekeyHex, plaintext, rawSize, aesKey)
	if err != nil {
		return fmt.Errorf("weixin: cdn upload: %w", err)
	}

	var mediaItem MessageItem
	switch itemType {
	case ItemTypeImage:
		mediaItem = MessageItem{
			Type: ItemTypeImage,
			ImageItem: &ImageItem{
				Media: &CDNMedia{
					EncryptQueryParam: downloadParam,
					AESKey:            encodeAESKeyForSend(aesKey),
					EncryptType:       1,
				},
				MidSize: fileSize,
			},
		}
	case ItemTypeFile:
		mediaItem = MessageItem{
			Type: ItemTypeFile,
			FileItem: &FileItem{
				Media: &CDNMedia{
					EncryptQueryParam: downloadParam,
					AESKey:            encodeAESKeyForSend(aesKey),
					EncryptType:       1,
				},
				FileName: fileName,
				Len:      strconv.FormatInt(rawSize, 10),
			},
		}
	case ItemTypeVideo:
		mediaItem = MessageItem{
			Type: ItemTypeVideo,
			VideoItem: &VideoItem{
				Media: &CDNMedia{
					EncryptQueryParam: downloadParam,
					AESKey:            encodeAESKeyForSend(aesKey),
					EncryptType:       1,
				},
				VideoSize: fileSize,
			},
		}
	default:
		return fmt.Errorf("weixin: unsupported media item type %d", itemType)
	}

	if logger != nil {
		logger.DebugContext(ctx, "weixin media uploaded",
			slog.String("filekey", filekeyHex),
			slog.Int64("raw_size", rawSize),
			slog.Int("cipher_size", fileSize),
		)
	}

	items := make([]MessageItem, 0, 2)
	if strings.TrimSpace(text) != "" {
		items = append(items, MessageItem{Type: ItemTypeText, TextItem: &TextItem{Text: text}})
	}
	items = append(items, mediaItem)

	for _, it := range items {
		req := SendMessageRequest{
			Msg: WeixinMessage{
				ToUserID:     target,
				ClientID:     generateClientID(),
				MessageType:  MessageTypeBot,
				MessageState: MessageStateFinish,
				ItemList:     []MessageItem{it},
				ContextToken: contextToken,
			},
		}
		if err := client.SendMessage(ctx, cfg, req); err != nil {
			return fmt.Errorf("weixin: send media item: %w", err)
		}
	}
	return nil
}

// encodeAESKeyForSend base64-encodes the hex representation of a raw AES key,
// matching the sendmessage wire format used by the upstream Weixin plugin.
func encodeAESKeyForSend(key []byte) string {
	hexStr := hex.EncodeToString(key)
	return base64.StdEncoding.EncodeToString([]byte(hexStr))
}

func generateClientID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "memoh-weixin-" + hex.EncodeToString(b)
}
