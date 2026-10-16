package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/models"
)

const (
	gatewayInlineAttachmentMaxBytes int64 = 20 * 1024 * 1024

	// attachmentPreparationTimeout bounds preparing one turn's attachments.
	// Rendering an animated sticker has its own, tighter deadline; this is the
	// ceiling on a turn that carries a lot of them, so a batch of pathological
	// media cannot hold a conversation open indefinitely.
	attachmentPreparationTimeout = 30 * time.Second

	// maxTurnVisionImages caps how many images one turn hands the model.
	//
	// A discussion turn gathers attachments from every new message, and an
	// animated sticker expands into several images, so the two multiply. Past
	// the cap an attachment contributes its first frame only — every
	// attachment stays visible, and it is the expansion that is shed first.
	maxTurnVisionImages = 20
)

// routeAndMergeAttachments applies CapabilityFallbackPolicy to split
// request attachments by model input modalities, then merges the results
// into a single []any for the gateway request.
func (s *Service) routeAndMergeAttachments(ctx context.Context, model models.GetResponse, req ChatRequest) []any {
	if len(req.Attachments) == 0 && len(req.ReplyAttachments) == 0 {
		return []any{}
	}
	typed := s.prepareGatewayAttachments(ctx, req, modelAcceptsImages(model))
	routed := routeAttachmentsByCapability(model.Config.Compatibilities, typed)
	for i := range routed.Fallback {
		fallbackPath := strings.TrimSpace(routed.Fallback[i].FallbackPath)
		if fallbackPath == "" {
			if s != nil && s.logger != nil {
				s.logger.WarnContext(ctx,
					"drop attachment without fallback path",
					slog.String("type", strings.TrimSpace(routed.Fallback[i].Type)),
					slog.String("transport", strings.TrimSpace(routed.Fallback[i].Transport)),
					slog.String("content_hash", strings.TrimSpace(routed.Fallback[i].ContentHash)),
					slog.Bool("has_payload", strings.TrimSpace(routed.Fallback[i].Payload) != ""),
				)
			}
			routed.Fallback[i] = gatewayAttachment{}
			continue
		}
		routed.Fallback[i].Type = "file"
		routed.Fallback[i].Transport = gatewayTransportToolFileRef
		routed.Fallback[i].Payload = fallbackPath
	}
	merged := make([]any, 0, len(routed.Native)+len(routed.Fallback))
	merged = append(merged, attachmentsToAny(routed.Native)...)
	for _, fb := range routed.Fallback {
		if fb.Type == "" || strings.TrimSpace(fb.Transport) == "" || strings.TrimSpace(fb.Payload) == "" {
			continue
		}
		merged = append(merged, fb)
	}
	if len(merged) == 0 {
		return []any{}
	}
	return merged
}

// prepareGatewayAttachments materializes request attachments for the gateway.
//
// acceptsImages says whether inline images can be used at all. Preparing an
// image is not free — an animated sticker is rasterised, any stored image is
// read and encoded — and a model without vision routes every one of them to a
// file reference regardless, so the work is skipped rather than thrown away.
func (s *Service) prepareGatewayAttachments(ctx context.Context, req ChatRequest, acceptsImages bool) []gatewayAttachment {
	ctx, cancel := context.WithTimeout(ctx, attachmentPreparationTimeout)
	defer cancel()
	attachments := requestAttachmentsForGateway(req)
	if len(attachments) == 0 {
		return nil
	}
	prepared := make([]gatewayAttachment, 0, len(attachments))
	for _, raw := range attachments {
		bundle := raw.Bundle()
		attachmentType := strings.ToLower(strings.TrimSpace(bundle.Type))
		payload := strings.TrimSpace(bundle.Base64)
		transport := ""
		fallbackPath := strings.TrimSpace(bundle.Path)
		rawURL := strings.TrimSpace(bundle.URL)
		if payload != "" {
			transport = gatewayTransportInlineDataURL
			if fallbackPath == "" && rawURL != "" {
				fallbackPath = rawURL
			}
		} else {
			contentHash := strings.TrimSpace(bundle.ContentHash)
			switch {
			case isLikelyPublicURL(rawURL) && contentHash == "":
				// Only treat a public HTTP URL as direct vision input when the
				// attachment has not been persisted yet. If ContentHash is set,
				// the file is already in the media store and will be inlined
				// by inlineImageAttachmentAssetIfNeeded below — prefer that path
				// so we never expose ephemeral or credentialed platform URLs
				// directly to the model.
				payload = rawURL
				transport = gatewayTransportPublicURL
			case rawURL != "" && fallbackPath == "":
				// URL is either a persisted local path (contentHash set) or an
				// unresolvable reference; store it as fallbackPath so the agent
				// can access it via the file tool if needed.
				fallbackPath = rawURL
			}
		}
		item := gatewayAttachment{
			ContentHash:  strings.TrimSpace(bundle.ContentHash),
			Type:         attachmentType,
			Mime:         strings.TrimSpace(bundle.Mime),
			Size:         bundle.Size,
			Name:         strings.TrimSpace(bundle.Name),
			Transport:    transport,
			Payload:      payload,
			Metadata:     bundle.Metadata,
			FallbackPath: fallbackPath,
		}
		if item.ContentHash != "" && strings.TrimSpace(item.FallbackPath) == "" {
			if accessPath, err := s.assetAccessPath(ctx, strings.TrimSpace(req.BotID), item.ContentHash); err != nil {
				if s != nil && s.logger != nil {
					s.logger.WarnContext(ctx,
						"resolve gateway attachment access path failed",
						slog.Any("error", err),
						slog.String("bot_id", strings.TrimSpace(req.BotID)),
						slog.String("content_hash", item.ContentHash),
					)
				}
			} else {
				item.FallbackPath = accessPath
			}
		}
		item = normalizeGatewayAttachmentPayload(item)
		if acceptsImages {
			item = s.inlineImageAttachmentAssetIfNeeded(ctx, strings.TrimSpace(req.BotID), item)
		}
		item = s.inlineFileAttachmentAssetIfNeeded(ctx, strings.TrimSpace(req.BotID), item)
		prepared = append(prepared, item)
	}
	return prepared
}

func (s *Service) assetAccessPath(ctx context.Context, botID, contentHash string) (string, error) {
	if s == nil || s.assetLoader == nil {
		return "", errors.New("gateway asset loader not configured")
	}
	accessPath, err := s.assetLoader.AccessPathForGateway(ctx, botID, contentHash)
	if err != nil {
		return "", err
	}
	accessPath = strings.TrimSpace(accessPath)
	if accessPath == "" {
		return "", errors.New("asset has no reachable access path")
	}
	return accessPath, nil
}

func requestAttachmentsForGateway(req ChatRequest) []ChatAttachment {
	if len(req.ReplyAttachments) == 0 {
		return req.Attachments
	}
	attachments := make([]ChatAttachment, 0, len(req.Attachments)+len(req.ReplyAttachments))
	attachments = append(attachments, req.Attachments...)
	attachments = append(attachments, req.ReplyAttachments...)
	return attachments
}

func normalizeGatewayAttachmentPayload(item gatewayAttachment) gatewayAttachment {
	if item.Transport != gatewayTransportInlineDataURL {
		return item
	}
	payload := strings.TrimSpace(item.Payload)
	if payload == "" {
		return item
	}
	if strings.HasPrefix(strings.ToLower(payload), "data:") {
		mime := strings.TrimSpace(item.Mime)
		if mime == "" || strings.EqualFold(mime, "application/octet-stream") {
			if extracted := attachmentpkg.MimeFromDataURL(payload); extracted != "" {
				item.Mime = extracted
			}
		}
		item.Payload = payload
		return item
	}
	mime := strings.TrimSpace(item.Mime)
	if mime == "" {
		mime = "application/octet-stream"
	}
	item.Payload = attachmentpkg.NormalizeBase64DataURL(payload, mime)
	return item
}

func isLikelyPublicURL(raw string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")
}

// extractNativeAttachmentParts returns non-image native parts for attachments
// routed to inline multimodal input: sdk.FilePart for provider-native
// documents (PDF), wrapped sdk.TextPart for the small plain-text channel.
// Images are handled by extractNativeImageParts.
func extractNativeAttachmentParts(attachments []any) []sdk.MessagePart {
	var parts []sdk.MessagePart
	for _, att := range attachments {
		ga, ok := att.(gatewayAttachment)
		if !ok || ga.Type != "file" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(ga.Transport)) != gatewayTransportInlineDataURL {
			continue
		}
		if strings.TrimSpace(ga.Payload) == "" {
			continue
		}
		switch {
		case isNativeDocumentMime(ga.Mime):
			data, mime := splitInlineDataURL(ga.Payload)
			if strings.TrimSpace(ga.Mime) != "" {
				mime = strings.TrimSpace(ga.Mime)
			}
			parts = append(parts, sdk.FilePart{
				Data:      data,
				MediaType: mime,
				Filename:  strings.TrimSpace(ga.Name),
			})
		case isInlineTextMime(ga.Mime):
			if part, ok := inlineTextAttachmentPart(ga); ok {
				parts = append(parts, part)
			}
		}
	}
	return parts
}

// splitInlineDataURL strips a data URL down to bare base64 plus the header
// mime. FilePart.Data carries bare base64 by convention; provider framing is
// the adapters' job.
func splitInlineDataURL(payload string) (string, string) {
	payload = strings.TrimSpace(payload)
	if !strings.HasPrefix(strings.ToLower(payload), "data:") {
		return payload, ""
	}
	idx := strings.Index(payload, ",")
	if idx < 0 {
		return payload, ""
	}
	header := payload[len("data:"):idx]
	if semi := strings.Index(header, ";"); semi >= 0 {
		header = header[:semi]
	}
	return payload[idx+1:], strings.TrimSpace(header)
}

func inlineTextAttachmentPart(ga gatewayAttachment) (sdk.TextPart, bool) {
	data, _ := splitInlineDataURL(ga.Payload)
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || !utf8.Valid(decoded) {
		return sdk.TextPart{}, false
	}
	name := strings.TrimSpace(ga.Name)
	if name == "" {
		name = "attachment.txt"
	}
	// The wrapper delimits untrusted user content; escape a would-be closing
	// tag so the content cannot break out of the attachment envelope.
	content := strings.ReplaceAll(string(decoded), "</attachment", "<\\/attachment")
	return sdk.TextPart{Text: fmt.Sprintf("<attachment filename=%q>\n%s\n</attachment>", name, content)}, true
}

// inlineInjectAttachments converts image attachments from an injected message
// into sdk.ImagePart values for direct vision input. Non-image attachments and
// media the model cannot parse are skipped; the message text still carries the
// attachment, so the agent keeps a usable reference to the original.
func (s *Service) inlineInjectAttachments(ctx context.Context, botID string, atts []ChatAttachment) []sdk.ImagePart {
	ctx, cancel := context.WithTimeout(ctx, attachmentPreparationTimeout)
	defer cancel()
	var parts []sdk.ImagePart
	var budget visionBudget
	seen := make(map[string]bool, len(atts))
	for _, att := range atts {
		if strings.ToLower(strings.TrimSpace(att.Type)) != "image" {
			continue
		}
		contentHash := strings.TrimSpace(att.ContentHash)
		if contentHash == "" || seen[contentHash] {
			continue
		}
		seen[contentHash] = true
		framed, err := s.inlineStoredImageParts(ctx, botID, contentHash, att.Mime)
		if err != nil {
			s.logImageInputRejected(err, botID, contentHash)
			continue
		}
		parts = append(parts, budget.take(framed)...)
	}
	return parts
}

func (s *Service) inlineImageAttachmentAssetIfNeeded(ctx context.Context, botID string, item gatewayAttachment) gatewayAttachment {
	if item.Type != "image" {
		return item
	}
	if strings.TrimSpace(item.Payload) != "" && item.Transport == gatewayTransportInlineDataURL {
		// Bytes are already here, so check them rather than trusting the label
		// the uploader attached to them.
		payload, mime, err := normalizeInlineImageDataURL(item.Payload)
		if err != nil {
			s.logImageInputRejected(err, botID, item.ContentHash)
			return demoteNonImageAttachment(item)
		}
		item.Payload, item.Mime = payload, mime
		return item
	}
	if strings.TrimSpace(item.Payload) != "" && item.Transport == gatewayTransportPublicURL {
		// Remote bytes cannot be sniffed here; the raster allowlist on the
		// declared MIME is all the capability router has to go on.
		return item
	}
	contentHash := strings.TrimSpace(item.ContentHash)
	if contentHash == "" {
		return item
	}
	dataURLs, mime, err := s.inlineStoredImageDataURLs(ctx, botID, contentHash, item.Mime)
	if err != nil {
		s.logImageInputRejected(err, botID, contentHash)
		if errors.Is(err, errUnsupportedImageBytes) {
			return demoteNonImageAttachment(item)
		}
		return item
	}
	item.Transport = gatewayTransportInlineDataURL
	item.Payload = dataURLs[0]
	item.Mime = mime
	if len(dataURLs) > 1 {
		item.Frames = dataURLs
		// Size drives the per-request media budget, so it has to account for
		// every frame the model will actually receive.
		item.Size = totalInlineDataURLBytes(dataURLs)
	}
	return item
}

// totalInlineDataURLBytes estimates the decoded size of a set of data URLs.
func totalInlineDataURLBytes(dataURLs []string) int64 {
	var total int64
	for _, dataURL := range dataURLs {
		body, _ := splitInlineDataURL(dataURL)
		total += int64(len(body)) * 3 / 4
	}
	return total
}

// demoteNonImageAttachment keeps media the model cannot parse out of the vision
// lane. The capability router turns it into a file reference, so the agent can
// still reach the original through workspace tools.
func demoteNonImageAttachment(item gatewayAttachment) gatewayAttachment {
	item.Mime = "application/octet-stream"
	return item
}

// logImageInputRejected records why an attachment was withheld from vision
// input. content_hash is the handle support needs to find the stored original;
// the bytes themselves and any credentialed source URL stay out of the log.
func (s *Service) logImageInputRejected(err error, botID, contentHash string) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Warn(
		"attachment withheld from vision input; original still reachable as a file",
		slog.Any("error", err),
		slog.String("bot_id", strings.TrimSpace(botID)),
		slog.String("content_hash", strings.TrimSpace(contentHash)),
	)
}

// inlineFileAttachmentAssetIfNeeded mirrors inlineImageAttachmentAssetIfNeeded
// for the file lane: native-document (PDF) and inline-text candidates get
// their payload materialized from the media store so the capability router can
// route them natively. Other file types are left to the workspace fallback.
func (s *Service) inlineFileAttachmentAssetIfNeeded(ctx context.Context, botID string, item gatewayAttachment) gatewayAttachment {
	if item.Type != "file" {
		return item
	}
	nativeDoc := isNativeDocumentMime(item.Mime)
	smallText := isInlineTextMime(item.Mime) && attachmentBinarySize(item) <= inlineTextAttachmentMaxBytes
	if !nativeDoc && !smallText {
		return item
	}
	if strings.TrimSpace(item.Payload) != "" && item.Transport == gatewayTransportInlineDataURL {
		return item
	}
	contentHash := strings.TrimSpace(item.ContentHash)
	if contentHash == "" {
		return item
	}
	dataURL, mime, err := s.inlineAssetAsDataURL(ctx, botID, contentHash, item.Type, item.Mime)
	if err != nil {
		if s != nil && s.logger != nil {
			s.logger.WarnContext(ctx,
				"inline gateway file attachment failed",
				slog.Any("error", err),
				slog.String("bot_id", botID),
				slog.String("content_hash", contentHash),
			)
		}
		return item
	}
	item.Transport = gatewayTransportInlineDataURL
	item.Payload = dataURL
	if strings.TrimSpace(item.Mime) == "" {
		item.Mime = mime
	}
	return item
}

func (s *Service) inlineAssetAsDataURL(ctx context.Context, botID, contentHash, attachmentType, fallbackMime string) (string, string, error) {
	if s == nil || s.assetLoader == nil {
		return "", "", errors.New("gateway asset loader not configured")
	}
	reader, assetMime, err := s.assetLoader.OpenForGateway(ctx, botID, contentHash)
	if err != nil {
		return "", "", fmt.Errorf("open asset: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()
	mime := strings.TrimSpace(fallbackMime)
	if mime == "" {
		mime = strings.TrimSpace(assetMime)
	}
	dataURL, resolvedMime, err := encodeReaderAsDataURL(reader, gatewayInlineAttachmentMaxBytes, attachmentType, mime)
	if err != nil {
		return "", "", err
	}
	return dataURL, resolvedMime, nil
}

func encodeReaderAsDataURL(reader io.Reader, maxBytes int64, attachmentType, fallbackMime string) (string, string, error) {
	if reader == nil {
		return "", "", errors.New("reader is required")
	}
	if maxBytes <= 0 {
		return "", "", errors.New("max bytes must be greater than 0")
	}
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	head := make([]byte, 512)
	// A Reader may return fewer bytes than asked for without being at the end
	// of the stream, and a short prefix cannot be told apart from arbitrary
	// binary. Fill the sniff window before deciding anything about the format.
	n, err := io.ReadFull(limited, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", "", fmt.Errorf("read asset: %w", err)
	}
	head = head[:n]

	mime := strings.TrimSpace(fallbackMime)
	if strings.EqualFold(strings.TrimSpace(attachmentType), "image") {
		detected, err := modelImageMimeFromBytes(head)
		if err != nil {
			return "", "", err
		}
		mime = detected
	}
	if mime == "" {
		mime = "application/octet-stream"
	}

	var encoded strings.Builder
	encoded.Grow(len("data:") + len(mime) + len(";base64,"))
	encoded.WriteString("data:")
	encoded.WriteString(mime)
	encoded.WriteString(";base64,")

	encoder := base64.NewEncoder(base64.StdEncoding, &encoded)
	if len(head) > 0 {
		if _, err := encoder.Write(head); err != nil {
			_ = encoder.Close()
			return "", "", fmt.Errorf("encode asset head: %w", err)
		}
	}
	copied, err := io.Copy(encoder, limited)
	if err != nil {
		_ = encoder.Close()
		return "", "", fmt.Errorf("encode asset body: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", "", fmt.Errorf("finalize asset encoding: %w", err)
	}

	total := int64(len(head)) + copied
	if total > maxBytes {
		return "", "", fmt.Errorf(
			"asset too large to inline: %d > %d",
			total,
			maxBytes,
		)
	}
	return encoded.String(), mime, nil
}
