package channel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"path/filepath"
	"strings"
	"time"

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/media"
)

// sharedHTTPClient is reused across attachment downloads to benefit from connection pooling.
var sharedHTTPClient = &http.Client{Timeout: 20 * time.Second}

// OutboundAttachmentStore provides the storage operations required by the
// outbound attachment preparer.
type OutboundAttachmentStore interface {
	// Stat returns asset metadata by content hash without opening the file.
	Stat(ctx context.Context, botID, contentHash string) (media.Asset, error)
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
	Ingest(ctx context.Context, input media.IngestInput) (media.Asset, error)
	GetByStorageKey(ctx context.Context, botID, storageKey string) (media.Asset, error)
	AccessPath(ctx context.Context, asset media.Asset) string
}

// ContainerAttachmentIngester is an optional extension of OutboundAttachmentStore
// for stores that can read files directly from a bot's container filesystem.
// Implementations must be safe for concurrent use.
type ContainerAttachmentIngester interface {
	IngestContainerFile(ctx context.Context, botID, containerPath string) (media.Asset, error)
}

// PrepareOutboundMessage resolves the logical outbound message into the
// adapter-facing prepared model.
func PrepareOutboundMessage(
	ctx context.Context,
	store OutboundAttachmentStore,
	cfg ChannelConfig,
	msg OutboundMessage,
) (PreparedOutboundMessage, error) {
	preparedMessage, err := prepareMessage(ctx, store, cfg, msg.Message)
	if err != nil {
		return PreparedOutboundMessage{}, err
	}
	return PreparedOutboundMessage{
		Target:  strings.TrimSpace(msg.Target),
		Message: preparedMessage,
	}, nil
}

// PrepareStreamEvent resolves logical stream payloads before they enter an adapter.
func PrepareStreamEvent(
	ctx context.Context,
	store OutboundAttachmentStore,
	cfg ChannelConfig,
	event StreamEvent,
) (PreparedStreamEvent, error) {
	prepared := PreparedStreamEvent{
		Type:      event.Type,
		Status:    event.Status,
		Delta:     event.Delta,
		Error:     event.Error,
		ToolCall:  event.ToolCall,
		Phase:     event.Phase,
		Reactions: event.Reactions,
		Speeches:  event.Speeches,
		Metadata:  event.Metadata,
	}

	if len(event.Attachments) > 0 {
		_, attachments, err := prepareAttachments(ctx, store, cfg, event.Attachments)
		if err != nil {
			return PreparedStreamEvent{}, err
		}
		prepared.Attachments = attachments
	}

	if event.Final != nil {
		preparedMessage, err := prepareMessage(ctx, store, cfg, event.Final.Message)
		if err != nil {
			return PreparedStreamEvent{}, err
		}
		prepared.Final = &PreparedStreamFinalizePayload{Message: preparedMessage}
	}

	return prepared, nil
}

func prepareMessage(
	ctx context.Context,
	store OutboundAttachmentStore,
	cfg ChannelConfig,
	msg Message,
) (PreparedMessage, error) {
	prepared := PreparedMessage{Message: msg}
	if len(msg.Attachments) == 0 {
		return prepared, nil
	}
	logical, attachments, err := prepareAttachments(ctx, store, cfg, msg.Attachments)
	if err != nil {
		return PreparedMessage{}, err
	}
	prepared.Message.Attachments = logical
	prepared.Attachments = attachments
	return prepared, nil
}

func prepareAttachments(
	ctx context.Context,
	store OutboundAttachmentStore,
	cfg ChannelConfig,
	attachments []Attachment,
) ([]Attachment, []PreparedAttachment, error) {
	normalized, err := normalizeAttachmentRefs(attachments, cfg.ChannelType)
	if err != nil {
		return nil, nil, err
	}
	logical := make([]Attachment, 0, len(normalized))
	prepared := make([]PreparedAttachment, 0, len(normalized))
	for _, att := range normalized {
		item, preparedAtt, prepareErr := prepareAttachment(ctx, store, cfg, att)
		if prepareErr != nil {
			return nil, nil, prepareErr
		}
		logical = append(logical, item)
		prepared = append(prepared, preparedAtt)
	}
	return logical, prepared, nil
}

func prepareAttachment(
	ctx context.Context,
	store OutboundAttachmentStore,
	cfg ChannelConfig,
	att Attachment,
) (Attachment, PreparedAttachment, error) {
	item := att
	item.Name = strings.TrimSpace(item.Name)
	item.Mime = attachmentpkg.NormalizeMime(item.Mime)

	if ref, ok := resolvePreparedNativeRef(cfg.ChannelType, item); ok {
		item.SourcePlatform = preparedNativeSourcePlatform(cfg.ChannelType, item.SourcePlatform)
		return item, PreparedAttachment{
			Logical:   item,
			Kind:      PreparedAttachmentNativeRef,
			NativeRef: ref,
			Name:      preparedAttachmentName(item, ""),
			Mime:      preparedAttachmentMime(item, ""),
			Size:      item.Size,
		}, nil
	}

	if urlRef := strings.TrimSpace(item.URL); IsHTTPURL(urlRef) && allowsPreparedPublicURL(cfg.ChannelType, item) {
		return item, PreparedAttachment{
			Logical:   item,
			Kind:      PreparedAttachmentPublicURL,
			PublicURL: urlRef,
			Name:      preparedAttachmentName(item, urlRef),
			Mime:      preparedAttachmentMime(item, mimeFromPath(urlRef)),
			Size:      item.Size,
		}, nil
	}

	botID := preparedAttachmentBotID(cfg.BotID, item.Metadata)
	switch {
	case strings.TrimSpace(item.ContentHash) != "":
		return preparePersistedAttachment(ctx, store, botID, item, "")
	case strings.TrimSpace(item.Base64) != "" || IsDataURL(item.URL):
		return prepareBase64Attachment(ctx, store, botID, item)
	case IsHTTPURL(item.URL):
		return prepareHTTPAttachment(ctx, store, botID, item)
	case strings.TrimSpace(item.Path) != "":
		return prepareContainerAttachment(ctx, store, botID, item)
	default:
		return Attachment{}, PreparedAttachment{}, errors.New("attachment reference is required")
	}
}

func preparePersistedAttachment(
	ctx context.Context,
	store OutboundAttachmentStore,
	botID string,
	item Attachment,
	sourcePath string,
) (Attachment, PreparedAttachment, error) {
	if store == nil {
		return Attachment{}, PreparedAttachment{}, errors.New("attachment store is not configured")
	}
	if strings.TrimSpace(botID) == "" {
		return Attachment{}, PreparedAttachment{}, errors.New("bot id is required for persisted attachments")
	}
	asset, err := store.Stat(ctx, botID, strings.TrimSpace(item.ContentHash))
	if err != nil {
		return Attachment{}, PreparedAttachment{}, fmt.Errorf("stat content hash attachment: %w", err)
	}
	applyPreparedAsset(ctx, store, asset, botID, &item, sourcePath)
	return item, preparedUploadAttachment(store, botID, item), nil
}

func prepareBase64Attachment(
	ctx context.Context,
	store OutboundAttachmentStore,
	botID string,
	item Attachment,
) (Attachment, PreparedAttachment, error) {
	if store == nil {
		return Attachment{}, PreparedAttachment{}, errors.New("attachment store is not configured")
	}
	if strings.TrimSpace(botID) == "" {
		return Attachment{}, PreparedAttachment{}, errors.New("bot id is required for base64 attachments")
	}
	raw := strings.TrimSpace(item.Base64)
	if raw == "" {
		raw = strings.TrimSpace(item.URL)
	}
	reader, err := attachmentpkg.DecodeBase64(raw, media.MaxAssetBytes)
	if err != nil {
		return Attachment{}, PreparedAttachment{}, fmt.Errorf("decode base64 attachment: %w", err)
	}
	sourceMime := attachmentpkg.NormalizeMime(item.Mime)
	if sourceMime == "" {
		sourceMime = attachmentpkg.MimeFromDataURL(raw)
	}
	if item.Name == "" {
		item.Name = preparedAttachmentName(item, "")
	}
	return ingestPreparedAttachment(
		ctx,
		store,
		botID,
		item,
		io.NopCloser(reader),
		sourceMime,
		"",
	)
}

func prepareHTTPAttachment(
	ctx context.Context,
	store OutboundAttachmentStore,
	botID string,
	item Attachment,
) (Attachment, PreparedAttachment, error) {
	if store == nil {
		return Attachment{}, PreparedAttachment{}, errors.New("attachment store is not configured")
	}
	if strings.TrimSpace(botID) == "" {
		return Attachment{}, PreparedAttachment{}, errors.New("bot id is required for remote attachments")
	}
	payload, err := openPreparedAttachmentURL(ctx, strings.TrimSpace(item.URL))
	if err != nil {
		return Attachment{}, PreparedAttachment{}, err
	}
	sourceMime := preparedAttachmentMime(item, payload.mime)
	if item.Name == "" {
		item.Name = preparedAttachmentName(item, payload.name)
	}
	if item.Size == 0 && payload.size > 0 {
		item.Size = payload.size
	}
	return ingestPreparedAttachment(ctx, store, botID, item, payload.reader, sourceMime, "")
}

func prepareContainerAttachment(
	ctx context.Context,
	store OutboundAttachmentStore,
	botID string,
	item Attachment,
) (Attachment, PreparedAttachment, error) {
	if store == nil {
		return Attachment{}, PreparedAttachment{}, errors.New("attachment store is not configured")
	}
	if strings.TrimSpace(botID) == "" {
		return Attachment{}, PreparedAttachment{}, errors.New("bot id is required for workspace attachments")
	}
	sourcePath := strings.TrimSpace(item.Path)
	if item.Name == "" {
		item.Name = preparedAttachmentName(item, sourcePath)
	}
	var (
		asset media.Asset
		err   error
	)
	if storageKey := extractPreparedStorageKey(sourcePath); storageKey != "" {
		asset, err = store.GetByStorageKey(ctx, botID, storageKey)
		if err == nil {
			applyPreparedAsset(ctx, store, asset, botID, &item, sourcePath)
			return item, preparedUploadAttachment(store, botID, item), nil
		}
	}
	ingester, ok := store.(ContainerAttachmentIngester)
	if !ok {
		if err != nil {
			return Attachment{}, PreparedAttachment{}, fmt.Errorf("prepare workspace attachment: lookup asset: %w", err)
		}
		return Attachment{}, PreparedAttachment{}, errors.New("attachment store does not support workspace file ingestion")
	}
	asset, ingestErr := ingester.IngestContainerFile(ctx, botID, sourcePath)
	if ingestErr != nil {
		if err != nil {
			return Attachment{}, PreparedAttachment{}, fmt.Errorf("prepare workspace attachment: lookup asset: %w; ingest workspace file: %w", err, ingestErr)
		}
		return Attachment{}, PreparedAttachment{}, fmt.Errorf("prepare workspace attachment: %w", ingestErr)
	}
	applyPreparedAsset(ctx, store, asset, botID, &item, sourcePath)
	return item, preparedUploadAttachment(store, botID, item), nil
}

func ingestPreparedAttachment(
	ctx context.Context,
	store OutboundAttachmentStore,
	botID string,
	item Attachment,
	reader io.ReadCloser,
	sourceMime string,
	sourcePath string,
) (Attachment, PreparedAttachment, error) {
	if reader == nil {
		return Attachment{}, PreparedAttachment{}, errors.New("attachment reader is required")
	}
	defer func() {
		_ = reader.Close()
	}()

	mediaType := attachmentpkg.MapMediaType(string(item.Type))
	preparedReader, finalMime, err := attachmentpkg.PrepareReaderAndMime(reader, mediaType, sourceMime)
	if err != nil {
		return Attachment{}, PreparedAttachment{}, fmt.Errorf("prepare attachment mime: %w", err)
	}
	asset, err := store.Ingest(ctx, media.IngestInput{
		BotID:       botID,
		Mime:        finalMime,
		Reader:      preparedReader,
		MaxBytes:    media.MaxAssetBytes,
		OriginalExt: preparedAttachmentExt(item, sourcePath),
	})
	if err != nil {
		return Attachment{}, PreparedAttachment{}, fmt.Errorf("ingest attachment: %w", err)
	}
	item.Mime = attachmentpkg.NormalizeMime(finalMime)
	applyPreparedAsset(ctx, store, asset, botID, &item, sourcePath)
	return item, preparedUploadAttachment(store, botID, item), nil
}

func preparedUploadAttachment(store OutboundAttachmentStore, botID string, item Attachment) PreparedAttachment {
	contentHash := strings.TrimSpace(item.ContentHash)
	return PreparedAttachment{
		Logical: item,
		Kind:    PreparedAttachmentUpload,
		Name:    preparedAttachmentName(item, ""),
		Mime:    preparedAttachmentMime(item, ""),
		Size:    item.Size,
		Open: func(ctx context.Context) (io.ReadCloser, error) {
			reader, _, err := store.Open(ctx, botID, contentHash)
			if err != nil {
				return nil, err
			}
			return reader, nil
		},
	}
}

type preparedAttachmentPayload struct {
	reader io.ReadCloser
	mime   string
	name   string
	size   int64
}

func openPreparedAttachmentURL(ctx context.Context, rawURL string) (preparedAttachmentPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return preparedAttachmentPayload{}, fmt.Errorf("build attachment request: %w", err)
	}
	resp, err := sharedHTTPClient.Do(req) //nolint:gosec // G107: attachment URLs are user-controlled channel payloads
	if err != nil {
		return preparedAttachmentPayload{}, fmt.Errorf("download attachment: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		return preparedAttachmentPayload{}, fmt.Errorf("download attachment status: %d", resp.StatusCode)
	}
	if resp.ContentLength > media.MaxAssetBytes {
		_ = resp.Body.Close()
		return preparedAttachmentPayload{}, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, media.MaxAssetBytes)
	}
	mimeType := attachmentpkg.NormalizeMime(resp.Header.Get("Content-Type"))
	name := contentDispositionFilename(resp.Header.Get("Content-Disposition"))
	if name == "" {
		name = preparedPathBase(rawURL)
	}
	return preparedAttachmentPayload{
		reader: resp.Body,
		mime:   mimeType,
		name:   name,
		size:   resp.ContentLength,
	}, nil
}

func resolvePreparedNativeRef(channelType ChannelType, item Attachment) (string, bool) {
	ref := strings.TrimSpace(item.PlatformKey)
	if ref != "" && preparedPlatformMatches(channelType, item.SourcePlatform) {
		switch channelType {
		case ChannelTypeTelegram, ChannelTypeFeishu:
			return ref, true
		case ChannelTypeDingtalk:
			switch item.Type {
			case AttachmentImage, AttachmentGIF:
				return "", false
			default:
				return ref, true
			}
		case ChannelTypeMatrix:
			if strings.HasPrefix(strings.ToLower(ref), "mxc://") {
				return ref, true
			}
		}
	}
	if channelType == ChannelTypeMatrix {
		urlRef := strings.TrimSpace(item.URL)
		if strings.HasPrefix(strings.ToLower(urlRef), "mxc://") {
			return urlRef, true
		}
	}
	return "", false
}

func allowsPreparedPublicURL(channelType ChannelType, item Attachment) bool {
	switch channelType {
	case ChannelTypeTelegram:
		return true
	case ChannelTypeDingtalk:
		return item.Type == AttachmentImage || item.Type == AttachmentGIF
	case ChannelTypeLine:
		return allowsLinePreparedPublicURL(item)
	default:
		return false
	}
}

func allowsLinePreparedPublicURL(item Attachment) bool {
	if item.Type != "" && item.Type != AttachmentImage {
		return false
	}
	raw := strings.TrimSpace(item.URL)
	if raw == "" || len(raw) > 2000 {
		return false
	}
	parsed, err := neturl.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return IsPublicHost(parsed.Hostname())
}

func applyPreparedAsset(ctx context.Context, store OutboundAttachmentStore, asset media.Asset, botID string, item *Attachment, sourcePath string) {
	if item == nil {
		return
	}
	bundle := BundleFromAttachment(*item)
	if sourcePath = strings.TrimSpace(sourcePath); sourcePath != "" {
		bundle.Path = sourcePath
	}
	*item = AttachmentFromBundle(bundle.WithAssetAccess(botID, asset, store.AccessPath(ctx, asset)))
	item.SourcePlatform = ""
	if item.Type == AttachmentFile || item.Type == "" {
		item.Type = preparedAttachmentTypeFromMime(item.Mime)
	}
}

func preparedAttachmentBotID(defaultBotID string, metadata map[string]any) string {
	if botID := strings.TrimSpace(defaultBotID); botID != "" {
		return botID
	}
	return attachmentpkg.MetadataString(metadata, attachmentpkg.MetadataKeyBotID)
}

func preparedAttachmentMime(item Attachment, fallback string) string {
	if mimeType := attachmentpkg.NormalizeMime(item.Mime); mimeType != "" {
		return mimeType
	}
	return attachmentpkg.NormalizeMime(fallback)
}

func preparedAttachmentName(item Attachment, fallback string) string {
	if name := strings.TrimSpace(item.Name); name != "" {
		return name
	}
	if name := preparedPathBase(fallback); name != "" {
		return name
	}
	base := "file"
	switch item.Type {
	case AttachmentImage, AttachmentGIF:
		base = "image"
	case AttachmentAudio, AttachmentVoice:
		base = "audio"
	case AttachmentVideo:
		base = "video"
	}
	if ext := preparedMimeExtension(preparedAttachmentMime(item, "")); ext != "" {
		return base + ext
	}
	return base
}

func preparedAttachmentExt(item Attachment, fallback string) string {
	if ext := filepath.Ext(strings.TrimSpace(item.Name)); ext != "" {
		return ext
	}
	if ext := filepath.Ext(preparedPathBase(fallback)); ext != "" {
		return ext
	}
	return preparedMimeExtension(preparedAttachmentMime(item, ""))
}

func preparedAttachmentTypeFromMime(mimeType string) AttachmentType {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case mimeType == "image/gif":
		return AttachmentGIF
	case strings.HasPrefix(mimeType, "image/"):
		return AttachmentImage
	case strings.HasPrefix(mimeType, "audio/"):
		return AttachmentAudio
	case strings.HasPrefix(mimeType, "video/"):
		return AttachmentVideo
	default:
		return AttachmentFile
	}
}

func preparedPlatformMatches(channelType ChannelType, sourcePlatform string) bool {
	sourcePlatform = strings.TrimSpace(sourcePlatform)
	return sourcePlatform == "" || strings.EqualFold(sourcePlatform, channelType.String())
}

func preparedNativeSourcePlatform(channelType ChannelType, sourcePlatform string) string {
	sourcePlatform = strings.TrimSpace(sourcePlatform)
	if sourcePlatform != "" {
		return sourcePlatform
	}
	return channelType.String()
}

func preparedPathBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := neturl.Parse(raw); err == nil && parsed != nil && parsed.Path != "" {
		if base := filepath.Base(parsed.Path); base != "." && base != "/" {
			return strings.TrimSpace(base)
		}
	}
	base := filepath.Base(raw)
	if base == "." || base == "/" {
		return ""
	}
	return strings.TrimSpace(base)
}

func preparedMimeExtension(mimeType string) string {
	mimeType = attachmentpkg.NormalizeMime(mimeType)
	if mimeType == "" {
		return ""
	}
	exts, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	return exts[0]
}

func mimeFromPath(raw string) string {
	ext := filepath.Ext(preparedPathBase(raw))
	if ext == "" {
		return ""
	}
	return attachmentpkg.NormalizeMime(mime.TypeByExtension(ext))
}

func contentDispositionFilename(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	if filename := strings.TrimSpace(params["filename*"]); filename != "" {
		if idx := strings.LastIndex(filename, "''"); idx >= 0 {
			filename = filename[idx+2:]
		}
		if decoded, err := neturl.QueryUnescape(filename); err == nil {
			return strings.TrimSpace(decoded)
		}
		return filename
	}
	return strings.TrimSpace(params["filename"])
}

// IsDataURL reports whether raw is a data: URL (e.g. "data:image/png;base64,...").
func IsDataURL(raw string) bool {
	return attachmentpkg.IsDataURL(raw)
}

// IsHTTPURL reports whether raw is an http:// or https:// URL.
func IsHTTPURL(raw string) bool {
	return attachmentpkg.IsHTTPURL(raw)
}

// IsDataPath reports whether raw is a container-internal data path (/data/...).
func IsDataPath(raw string) bool {
	return attachmentpkg.IsDataPath(raw)
}

func extractPreparedStorageKey(accessPath string) string {
	return attachmentpkg.ExtractStorageKey(accessPath)
}
