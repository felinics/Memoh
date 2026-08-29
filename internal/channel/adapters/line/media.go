package line

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/media"
)

func (a *Adapter) ResolveAttachment(ctx context.Context, cfg channel.ChannelConfig, att channel.Attachment) (channel.AttachmentPayload, error) {
	creds, err := parseConfigForUse(cfg.Credentials)
	if err != nil {
		return channel.AttachmentPayload{}, err
	}
	messageID := strings.TrimSpace(att.PlatformKey)
	if messageID == "" {
		return channel.AttachmentPayload{}, errors.New("line attachment platform_key(message id) is required")
	}
	if strings.TrimSpace(att.SourcePlatform) != "" && !strings.EqualFold(strings.TrimSpace(att.SourcePlatform), Type.String()) {
		return channel.AttachmentPayload{}, errors.New("line attachment source platform mismatch")
	}
	if att.Size > lineBlobMaxBytes {
		return channel.AttachmentPayload{}, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, lineBlobMaxBytes)
	}

	callCtx, cancel := context.WithTimeout(ctx, lineBlobTimeout)
	client, err := a.client.NewBlobClient(callCtx, creds.ChannelAccessToken)
	if err != nil {
		cancel()
		return channel.AttachmentPayload{}, sanitizeLineError("line create blob client failed", err)
	}
	resp, err := client.GetMessageContent(messageID)
	if err != nil {
		cancel()
		a.logWarn("line blob download failed",
			slog.String("config_id", cfg.ID),
			slog.String("bot_id", cfg.BotID),
			slog.String("message_id_hash", hashValue(messageID)),
			slog.String("reason", "blob_api_failed"),
		)
		return channel.AttachmentPayload{}, sanitizeLineError("line blob download failed", err)
	}
	if resp == nil || resp.Body == nil {
		cancel()
		return channel.AttachmentPayload{}, errors.New("line blob download failed: empty_response")
	}
	if resp.ContentLength > lineBlobMaxBytes {
		_ = resp.Body.Close()
		cancel()
		return channel.AttachmentPayload{}, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, lineBlobMaxBytes)
	}
	return channel.AttachmentPayload{
		Reader: &limitedReadCloser{
			body:   resp.Body,
			max:    lineBlobMaxBytes,
			cancel: cancel,
		},
		Mime: attachment.NormalizeMime(resp.Header.Get("Content-Type")),
		Name: strings.TrimSpace(att.Name),
		Size: resp.ContentLength,
	}, nil
}

type limitedReadCloser struct {
	body       io.ReadCloser
	max        int64
	read       int64
	cancel     context.CancelFunc
	cancelOnce sync.Once
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r == nil || r.body == nil {
		return 0, io.EOF
	}
	if r.max > 0 && r.read >= r.max {
		var one [1]byte
		n, err := r.body.Read(one[:])
		if n > 0 {
			return 0, media.ErrAssetTooLarge
		}
		return 0, err
	}
	if r.max > 0 {
		remaining := r.max - r.read
		if int64(len(p)) > remaining {
			p = p[:remaining]
		}
	}
	n, err := r.body.Read(p)
	r.read += int64(n)
	return n, err
}

func (r *limitedReadCloser) Close() error {
	if r == nil || r.body == nil {
		return nil
	}
	err := r.body.Close()
	r.cancelOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})
	return err
}
