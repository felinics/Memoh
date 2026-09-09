package wechatoa

import (
	"context"
	"crypto/sha1" //nolint:gosec // WeChat webhook signatures and synthetic IDs intentionally use SHA1 for protocol compatibility.
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/channel"
)

func handleVerifyRequest(verifier *securityVerifier, mode string, r *http.Request, w http.ResponseWriter) error {
	query := r.URL.Query()
	timestamp := strings.TrimSpace(query.Get("timestamp"))
	nonce := strings.TrimSpace(query.Get("nonce"))
	signature := strings.TrimSpace(query.Get("signature"))
	echostr := strings.TrimSpace(query.Get("echostr"))
	if timestamp == "" || nonce == "" || signature == "" || echostr == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid verify query"))
		return nil
	}
	if mode == encryptionModeSafe || mode == encryptionModeCompat {
		msgSig := strings.TrimSpace(query.Get("msg_signature"))
		if msgSig != "" {
			if !verifier.verifyMessageSignature(msgSig, timestamp, nonce, echostr) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("invalid signature"))
				return nil
			}
			plain, err := verifier.decrypt(echostr)
			if err != nil {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("decrypt echostr failed"))
				return nil
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(plain)) //nolint:gosec // WeChat requires echoing the decrypted verification string verbatim.
			return nil
		}
	}
	if !verifier.verifyURLSignature(signature, timestamp, nonce) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("invalid signature"))
		return nil
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(echostr)) //nolint:gosec // WeChat requires echoing the verification string verbatim.
	return nil
}

func (a *WeChatOAAdapter) handleInbound(ctx context.Context, verifier *securityVerifier, mode string, cfg channel.ChannelConfig, handler channel.InboundHandler, r *http.Request, w http.ResponseWriter) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "read body failed")
	}
	defer func() { _ = r.Body.Close() }()

	messageXML, err := decodeInboundXML(verifier, mode, r, raw)
	if err != nil {
		if a.logger != nil {
			a.logger.Warn("decode wechatoa inbound failed", slog.Any("error", err))
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
		return nil
	}

	var payload wechatEnvelope
	if err := xml.Unmarshal([]byte(messageXML), &payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid xml payload")
	}
	if handler != nil {
		msg, ok := buildInboundMessage(payload)
		if ok {
			msg.BotID = cfg.BotID
			if err := handler(ctx, cfg, msg); err != nil && a.logger != nil {
				a.logger.Warn("handle inbound failed", slog.Any("error", err))
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
	return nil
}

func decodeInboundXML(verifier *securityVerifier, mode string, r *http.Request, raw []byte) (string, error) {
	if mode == encryptionModePlain {
		q := r.URL.Query()
		if !verifier.verifyURLSignature(q.Get("signature"), q.Get("timestamp"), q.Get("nonce")) {
			return "", errors.New("invalid url signature")
		}
		return string(raw), nil
	}
	var envelope encryptedEnvelope
	if err := xml.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	encrypt := strings.TrimSpace(envelope.Encrypt)
	if encrypt == "" && mode == encryptionModeCompat {
		q := r.URL.Query()
		if !verifier.verifyURLSignature(q.Get("signature"), q.Get("timestamp"), q.Get("nonce")) {
			return "", errors.New("invalid url signature")
		}
		return string(raw), nil
	}
	if encrypt == "" {
		return "", errors.New("missing encrypted payload")
	}
	q := r.URL.Query()
	if !verifier.verifyMessageSignature(q.Get("msg_signature"), q.Get("timestamp"), q.Get("nonce"), encrypt) {
		return "", errors.New("invalid message signature")
	}
	return verifier.decrypt(encrypt)
}

func buildInboundMessage(in wechatEnvelope) (channel.InboundMessage, bool) {
	msgType := strings.ToLower(strings.TrimSpace(in.MsgType))
	switch msgType {
	case "text", "image", "voice", "video", "shortvideo", "location", "link":
		return buildInboundUserMessage(in)
	case "event":
		return buildInboundEvent(in)
	default:
		return channel.InboundMessage{}, false
	}
}

func buildInboundUserMessage(in wechatEnvelope) (channel.InboundMessage, bool) {
	openID := strings.TrimSpace(in.FromUserName)
	if openID == "" {
		return channel.InboundMessage{}, false
	}
	msg := channel.Message{
		ID:     strings.TrimSpace(in.MsgID),
		Format: channel.MessageFormatPlain,
		Text:   strings.TrimSpace(in.Content),
	}
	if msg.ID == "" {
		msg.ID = syntheticID(openID, strconv.FormatInt(in.CreateTime, 10), in.MsgType, in.Content, in.MediaID)
	}
	switch strings.ToLower(strings.TrimSpace(in.MsgType)) {
	case "image":
		msg.Attachments = []channel.Attachment{channel.NormalizeInboundChannelAttachment(channel.Attachment{
			Type:           channel.AttachmentImage,
			URL:            strings.TrimSpace(in.PicURL),
			PlatformKey:    strings.TrimSpace(in.MediaID),
			SourcePlatform: Type.String(),
		})}
	case "voice":
		msg.Attachments = []channel.Attachment{channel.NormalizeInboundChannelAttachment(channel.Attachment{
			Type:           channel.AttachmentVoice,
			PlatformKey:    strings.TrimSpace(in.MediaID),
			SourcePlatform: Type.String(),
			Mime:           strings.TrimSpace(in.Format),
		})}
	case "video", "shortvideo":
		msg.Attachments = []channel.Attachment{channel.NormalizeInboundChannelAttachment(channel.Attachment{
			Type:           channel.AttachmentVideo,
			PlatformKey:    strings.TrimSpace(in.MediaID),
			SourcePlatform: Type.String(),
			Metadata: map[string]any{
				"thumb_media_id": strings.TrimSpace(in.ThumbMediaID),
			},
		})}
	case "location":
		msg.Text = strings.TrimSpace(in.Label)
		msg.Metadata = map[string]any{
			"location_x": strings.TrimSpace(in.LocationX),
			"location_y": strings.TrimSpace(in.LocationY),
			"scale":      strings.TrimSpace(in.Scale),
		}
	case "link":
		text := strings.TrimSpace(in.Title)
		if text == "" {
			text = strings.TrimSpace(in.Description)
		}
		if text == "" {
			text = strings.TrimSpace(in.URL)
		}
		msg.Text = text
		msg.Metadata = map[string]any{"url": strings.TrimSpace(in.URL)}
	}
	return channel.InboundMessage{
		Channel:     Type,
		Message:     msg,
		ReplyTarget: "openid:" + openID,
		Sender: channel.Identity{
			SubjectID: openID,
			Attributes: map[string]string{
				"openid": openID,
			},
		},
		Conversation: channel.Conversation{
			ID:   openID,
			Type: channel.ConversationTypePrivate,
		},
		ReceivedAt: parseTimestamp(in.CreateTime),
		Source:     "wechatoa",
	}, true
}

func buildInboundEvent(in wechatEnvelope) (channel.InboundMessage, bool) {
	openID := strings.TrimSpace(in.FromUserName)
	if openID == "" {
		return channel.InboundMessage{}, false
	}
	eventType := strings.ToLower(strings.TrimSpace(in.Event))
	if eventType == "" {
		return channel.InboundMessage{}, false
	}
	text := eventType
	if key := strings.TrimSpace(in.EventKey); key != "" {
		text = eventType + ":" + key
	}
	metadata := map[string]any{
		"is_event":  true,
		"event":     eventType,
		"event_key": strings.TrimSpace(in.EventKey),
		"ticket":    strings.TrimSpace(in.Ticket),
	}
	msg := channel.Message{
		ID:       syntheticID(openID, strconv.FormatInt(in.CreateTime, 10), "event", eventType, in.EventKey, in.Ticket),
		Format:   channel.MessageFormatPlain,
		Text:     text,
		Metadata: metadata,
	}
	return channel.InboundMessage{
		Channel:     Type,
		Message:     msg,
		ReplyTarget: "openid:" + openID,
		Sender: channel.Identity{
			SubjectID: openID,
			Attributes: map[string]string{
				"openid": openID,
			},
		},
		Conversation: channel.Conversation{
			ID:   openID,
			Type: channel.ConversationTypePrivate,
		},
		ReceivedAt: parseTimestamp(in.CreateTime),
		Source:     "wechatoa",
		Metadata:   metadata,
	}, true
}

func syntheticID(parts ...string) string {
	//nolint:gosec // SHA1 is sufficient for stable synthetic IDs and matches WeChat signature conventions.
	h := sha1.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(strings.TrimSpace(p)))
		_, _ = h.Write([]byte{0})
	}
	return "wechatoa_" + hex.EncodeToString(h.Sum(nil))
}

func parseTimestamp(value int64) time.Time {
	if value <= 0 {
		return time.Now().UTC()
	}
	return time.Unix(value, 0).UTC()
}
