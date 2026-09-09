package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/media"
)

// SessionContext carries request-scoped identity for tool execution.
type SessionContext struct {
	BotID              string
	ChatID             string
	CanOmitTarget      bool
	AllowLocalShortcut bool
	CurrentPlatform    string
	ReplyTarget        string
}

// AssetMeta holds resolved metadata for a media asset.
type AssetMeta = media.Asset

// Executor provides send and react operations for channel messaging.
type Executor struct {
	Sender        Sender
	Reactor       Reactor
	Resolver      ChannelTypeResolver
	AssetResolver AssetResolver
	Promoter      AttachmentPromoter
	Logger        *slog.Logger
}

// SendResult is the success payload returned after sending a message.
type SendResult struct {
	BotID     string
	Platform  string
	Target    string
	MessageID string
	// Local is true when the message targets the current conversation.
	// The caller should emit the resolved attachments as stream events.
	Local            bool
	LocalAttachments []Attachment
	// LocalTextOmitted is true when the local shortcut delivered the
	// attachments but dropped accompanying text/parts; the caller should tell
	// the model to restate that text in its assistant reply.
	LocalTextOmitted bool
}

// ReactResult is the success payload returned after reacting.
type ReactResult struct {
	BotID     string
	Platform  string
	Target    string
	MessageID string
	Emoji     string
	Action    string // "added" or "removed"
	Local     bool
	Remove    bool
}

type sendMode struct {
	name                   string
	allowLocalShortcut     bool
	requireTarget          bool
	promoteDataAttachments bool
}

type sendPlan struct {
	botID       string
	channelType Platform
	target      string
	sameConv    bool
	message     Message
}

var errOutboundMessageRequired = errors.New("message is required")

// Send executes a send-message action. args are the tool call arguments.
func (e *Executor) Send(ctx context.Context, session SessionContext, args map[string]any) (*SendResult, error) {
	return e.sendWithMode(ctx, session, "", args, sendMode{
		name:               "send",
		allowLocalShortcut: true,
	})
}

// SendDirect sends a message via the channel adapter without the same-conversation
// local shortcut. Used by discuss mode where there is no active stream emitter.
func (e *Executor) SendDirect(ctx context.Context, session SessionContext, target string, args map[string]any) (*SendResult, error) {
	return e.sendWithMode(ctx, session, target, args, sendMode{
		name:                   "send direct",
		requireTarget:          true,
		promoteDataAttachments: true,
	})
}

func (e *Executor) sendWithMode(
	ctx context.Context,
	session SessionContext,
	explicitTarget string,
	args map[string]any,
	mode sendMode,
) (*SendResult, error) {
	if e.Sender == nil || e.Resolver == nil {
		return nil, errors.New("message service not available")
	}

	plan, err := e.prepareSendPlan(ctx, session, explicitTarget, args, mode)
	if err != nil {
		return nil, err
	}

	if mode.allowLocalShortcut && session.AllowLocalShortcut && plan.sameConv {
		if !localShortcutCanRepresent(plan.message) {
			return nil, errors.New("send to the current conversation is only for standalone files or attachments; use assistant text for ordinary replies")
		}
		return &SendResult{
			BotID:            plan.botID,
			Platform:         plan.channelType.String(),
			Target:           plan.target,
			Local:            true,
			LocalAttachments: plan.message.Attachments,
			LocalTextOmitted: localShortcutOmitsText(plan.message),
		}, nil
	}

	if mode.promoteDataAttachments {
		e.promoteDataPathAttachmentsToAssets(ctx, plan.botID, plan.channelType, &plan.message)
	}

	if err := e.Sender.Send(ctx, plan.botID, plan.channelType, SendRequest{
		Target:  plan.target,
		Message: plan.message,
	}); err != nil {
		if e.Logger != nil {
			e.Logger.Warn("outbound send failed",
				slog.String("mode", mode.name),
				slog.Any("error", err),
				slog.String("bot_id", plan.botID),
				slog.String("platform", string(plan.channelType)),
			)
		}
		return nil, err
	}

	return &SendResult{
		BotID:    plan.botID,
		Platform: plan.channelType.String(),
		Target:   plan.target,
	}, nil
}

func (e *Executor) prepareSendPlan(
	ctx context.Context,
	session SessionContext,
	explicitTarget string,
	args map[string]any,
	mode sendMode,
) (*sendPlan, error) {
	if err := validateSendArguments(args); err != nil {
		return nil, err
	}
	botID, err := e.resolveBotID(args, session)
	if err != nil {
		return nil, err
	}
	channelType, err := e.resolvePlatform(args, session)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(explicitTarget)
	if target == "" {
		target = firstStringArg(args, "target")
	}
	if target == "" {
		target = defaultReplyTargetForPlatform(args, session, channelType)
	}

	if mode.requireTarget && target == "" {
		return nil, errors.New("target is required")
	}
	if !mode.allowLocalShortcut && target == "" {
		return nil, errors.New("target is required for cross-conversation send")
	}
	if target == "" {
		return nil, errors.New("target is required")
	}

	sameConv := IsSameConversation(session, channelType.String(), target)
	allowSameConversationShortcut := mode.allowLocalShortcut && session.AllowLocalShortcut && sameConv
	msg, err := e.buildOutboundMessage(ctx, botID, session, channelType, target, args, allowSameConversationShortcut)
	if err != nil {
		return nil, err
	}

	return &sendPlan{
		botID:       botID,
		channelType: channelType,
		target:      target,
		sameConv:    sameConv,
		message:     msg,
	}, nil
}

func validateSendArguments(args map[string]any) error {
	allowed := map[string]struct{}{
		"bot_id": {}, "platform": {}, "target": {}, "text": {}, "reply_to": {}, "attachments": {}, "message": {},
	}
	for key := range args {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown send field %q", key)
		}
	}
	if raw, ok := args["reply_to"]; ok && raw != nil {
		replyTo, ok := raw.(string)
		if !ok {
			return errors.New("reply_to must be string")
		}
		args["reply_to"] = strings.TrimSpace(replyTo)
	}
	return nil
}

// localShortcutCanRepresent reports whether the message can be delivered by
// emitting stream events into the current reply: it must carry attachments and
// nothing that requires a real channel send (actions, reply refs, forwards).
// Text and parts are tolerated but not delivered — the model is told to
// restate them in its assistant reply (see SendResult.LocalTextOmitted).
func localShortcutCanRepresent(msg Message) bool {
	return len(msg.Attachments) > 0 &&
		len(msg.Actions) == 0 &&
		msg.Reply == nil &&
		msg.Forward == nil
}

func localShortcutOmitsText(msg Message) bool {
	return strings.TrimSpace(msg.Text) != "" || len(msg.Parts) > 0
}

func (e *Executor) buildOutboundMessage(
	ctx context.Context,
	botID string,
	session SessionContext,
	channelType Platform,
	target string,
	args map[string]any,
	isSameConv bool,
) (Message, error) {
	messageText := firstStringArg(args, "text")
	messageAttachments := messageAttachmentsArg(args)
	messageArgs := args
	if messageAttachments != nil {
		messageArgs = withoutMessageAttachments(args)
	}
	outboundMessage, parseErr := ParseOutboundMessage(messageArgs, messageText)
	if parseErr != nil {
		rawAtt, hasTopLevelAttachments := args["attachments"]
		hasAttachments := (hasTopLevelAttachments && rawAtt != nil) || messageAttachments != nil
		if !hasAttachments || !errors.Is(parseErr, errOutboundMessageRequired) {
			return Message{}, parseErr
		}
		outboundMessage = Message{Text: strings.TrimSpace(messageText)}
	}

	if messageAttachments != nil {
		outboundMessage.Attachments = nil
		attachments, err := e.resolveOutboundAttachments(ctx, botID, session, channelType, target, messageAttachments, isSameConv)
		if err != nil {
			return Message{}, err
		}
		outboundMessage.Attachments = append(outboundMessage.Attachments, attachments...)
	}
	if rawAttachments, ok := args["attachments"]; ok && rawAttachments != nil {
		attachments, err := e.resolveOutboundAttachments(ctx, botID, session, channelType, target, rawAttachments, isSameConv)
		if err != nil {
			return Message{}, err
		}
		outboundMessage.Attachments = append(outboundMessage.Attachments, attachments...)
	}
	if outboundMessage.IsEmpty() {
		return Message{}, errors.New("message or attachments required")
	}
	if replyTo := firstStringArg(args, "reply_to"); replyTo != "" {
		outboundMessage.Reply = &ReplyRef{MessageID: replyTo}
	}
	if outboundMessage.Format == "" && ContainsMarkdown(outboundMessage.Text) {
		outboundMessage.Format = MessageFormatMarkdown
	}
	return outboundMessage, nil
}

func messageAttachmentsArg(args map[string]any) any {
	if args == nil {
		return nil
	}
	raw, ok := args["message"]
	if !ok || raw == nil {
		return nil
	}
	switch msg := raw.(type) {
	case map[string]any:
		return msg["attachments"]
	default:
		return nil
	}
}

func withoutMessageAttachments(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	raw, ok := args["message"]
	if !ok || raw == nil {
		return args
	}
	msg, ok := raw.(map[string]any)
	if !ok {
		return args
	}
	msgCopy := make(map[string]any, len(msg))
	for k, v := range msg {
		if k != "attachments" {
			msgCopy[k] = v
		}
	}
	argsCopy := make(map[string]any, len(args))
	for k, v := range args {
		argsCopy[k] = v
	}
	argsCopy["message"] = msgCopy
	return argsCopy
}

func (e *Executor) resolveOutboundAttachments(
	ctx context.Context,
	botID string,
	_ SessionContext,
	_ Platform,
	_ string,
	rawAttachments any,
	allowSameConversationShortcut bool,
) ([]Attachment, error) {
	if err := validateOutboundAttachmentInput(rawAttachments); err != nil {
		return nil, err
	}
	bundles, ok := attachmentpkg.ParseToolInputBundles(rawAttachments)
	if !ok {
		return nil, errors.New("attachments must be a string, object, or array")
	}
	if len(bundles) == 0 {
		return nil, nil
	}
	if allowSameConversationShortcut {
		return resolveSameConversationAttachments(bundles), nil
	}
	resolved := e.ResolveAttachments(ctx, botID, bundles)
	resolved = dropUnresolvedDataPathAttachments(resolved)
	if len(resolved) == 0 {
		return nil, errors.New("attachments could not be resolved")
	}
	return resolved, nil
}

func validateOutboundAttachmentInput(raw any) error {
	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		return nil
	case map[string]any:
		return validateOutboundAttachmentObject("", value)
	case []string:
		return nil
	case []any:
		for i, item := range value {
			if err := validateOutboundAttachmentItem(i, item); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func validateOutboundAttachmentItem(index int, raw any) error {
	switch value := raw.(type) {
	case string:
		return nil
	case map[string]any:
		return validateOutboundAttachmentObject(fmt.Sprintf(" at index %d", index), value)
	default:
		return fmt.Errorf("attachment must be string or object at index %d", index)
	}
}

func validateOutboundAttachmentObject(location string, raw map[string]any) error {
	allowed := map[string]struct{}{
		"type": {}, "base64": {}, "path": {}, "url": {}, "platform_key": {}, "source_platform": {},
		"content_hash": {}, "name": {}, "mime": {}, "size": {}, "duration_ms": {}, "width": {},
		"height": {}, "thumbnail_url": {}, "caption": {}, "metadata": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown attachment field %q%s", key, location)
		}
	}
	if rawType, ok := raw["type"]; ok && rawType != nil {
		attType, ok := rawType.(string)
		if !ok {
			return fmt.Errorf("attachment type must be string%s", location)
		}
		attType = strings.TrimSpace(attType)
		switch AttachmentType(attType) {
		case "", AttachmentImage, AttachmentAudio, AttachmentVideo, AttachmentVoice, AttachmentFile, AttachmentGIF:
			raw["type"] = attType
		default:
			return fmt.Errorf("unsupported attachment type %q%s", attType, location)
		}
	}
	if rawURL, ok := raw["url"]; ok && rawURL != nil {
		url, ok := rawURL.(string)
		if !ok {
			return fmt.Errorf("attachment url must be string%s", location)
		}
		url = strings.TrimSpace(url)
		if url != "" && !attachmentpkg.IsHTTPURL(url) && !attachmentpkg.IsDataURL(url) {
			return fmt.Errorf("attachment url must be http(s) or data URL%s", location)
		}
		raw["url"] = url
	}
	for _, key := range []string{"base64", "path", "url", "platform_key", "content_hash"} {
		if strings.TrimSpace(stringMapValue(raw, key)) != "" {
			return nil
		}
	}
	return fmt.Errorf("attachment reference is required%s", location)
}

func stringMapValue(raw map[string]any, key string) string {
	value, _ := raw[key].(string)
	return value
}

func dropUnresolvedDataPathAttachments(attachments []Attachment) []Attachment {
	if len(attachments) == 0 {
		return nil
	}
	filtered := attachments[:0]
	for _, att := range attachments {
		if strings.TrimSpace(att.ContentHash) == "" &&
			strings.TrimSpace(att.URL) == "" &&
			strings.TrimSpace(att.Base64) == "" &&
			strings.TrimSpace(att.PlatformKey) == "" &&
			attachmentpkg.IsDataPath(att.Path) {
			continue
		}
		filtered = append(filtered, att)
	}
	return filtered
}

func resolveSameConversationAttachments(bundles []attachmentpkg.Bundle) []Attachment {
	if len(bundles) == 0 {
		return nil
	}
	result := make([]Attachment, 0, len(bundles))
	for _, bundle := range bundles {
		result = append(result, AttachmentFromBundle(bundle))
	}
	return result
}

// promoteDataPathAttachmentsToAssets converts /data/* attachment references
// into content_hash-backed attachments before channel send.
// This avoids adapters treating local container paths as HTTP URLs.
func (e *Executor) promoteDataPathAttachmentsToAssets(ctx context.Context, botID string, platform Platform, msg *Message) {
	if e.Promoter == nil || msg == nil || len(msg.Attachments) == 0 {
		return
	}
	prepared, err := e.Promoter.PromoteAttachments(ctx, botID, platform, *msg)
	if err != nil {
		return
	}
	*msg = prepared
}

// React executes a react action. args are the tool call arguments.
func (e *Executor) React(ctx context.Context, session SessionContext, args map[string]any) (*ReactResult, error) {
	if e.Reactor == nil || e.Resolver == nil {
		return nil, errors.New("reaction service not available")
	}
	botID, err := e.resolveBotID(args, session)
	if err != nil {
		return nil, err
	}
	channelType, err := e.resolvePlatform(args, session)
	if err != nil {
		return nil, err
	}
	target := firstStringArg(args, "target")
	if target == "" {
		target = defaultReplyTargetForPlatform(args, session, channelType)
	}
	if target == "" {
		return nil, errors.New("target is required")
	}
	messageID := firstStringArg(args, "message_id")
	if messageID == "" {
		return nil, errors.New("message_id is required")
	}
	emoji := firstStringArg(args, "emoji")
	remove, _, _ := boolArg(args, "remove")
	sameConv := IsSameConversation(session, channelType.String(), target)
	if session.AllowLocalShortcut && sameConv {
		action := "added"
		if remove {
			action = "removed"
		}
		return &ReactResult{
			BotID: botID, Platform: channelType.String(), Target: target,
			MessageID: messageID, Emoji: emoji, Action: action, Local: true, Remove: remove,
		}, nil
	}
	if err := e.Reactor.React(ctx, botID, channelType, ReactRequest{
		Target: target, MessageID: messageID, Emoji: emoji, Remove: remove,
	}); err != nil {
		if e.Logger != nil {
			e.Logger.Warn("react failed", slog.Any("error", err), slog.String("bot_id", botID), slog.String("platform", string(channelType)))
		}
		return nil, err
	}
	action := "added"
	if remove {
		action = "removed"
	}
	return &ReactResult{
		BotID: botID, Platform: channelType.String(), Target: target,
		MessageID: messageID, Emoji: emoji, Action: action,
	}, nil
}

func defaultReplyTargetForPlatform(args map[string]any, session SessionContext, channelType Platform) string {
	if !session.CanOmitTarget {
		return ""
	}
	if firstStringArg(args, "platform") != "" && !strings.EqualFold(channelType.String(), strings.TrimSpace(session.CurrentPlatform)) {
		return ""
	}
	return strings.TrimSpace(session.ReplyTarget)
}

// CanSend returns true if the executor has a sender and resolver configured.
func (e *Executor) CanSend() bool { return e.Sender != nil && e.Resolver != nil }

// CanReact returns true if the executor has a reactor and resolver configured.
func (e *Executor) CanReact() bool { return e.Reactor != nil && e.Resolver != nil }

// IsSameConversation reports whether platform+target matches the session's
// current conversation.
func IsSameConversation(session SessionContext, platform, target string) bool {
	replyTarget := strings.TrimSpace(session.ReplyTarget)
	if replyTarget == "" {
		return false
	}
	if platform == "" {
		platform = strings.TrimSpace(session.CurrentPlatform)
	}
	if target == "" {
		target = replyTarget
	}
	return strings.EqualFold(platform, strings.TrimSpace(session.CurrentPlatform)) &&
		target == replyTarget
}

func (*Executor) resolveBotID(args map[string]any, session SessionContext) (string, error) {
	botID := firstStringArg(args, "bot_id")
	if botID == "" {
		botID = strings.TrimSpace(session.BotID)
	}
	if botID == "" {
		return "", errors.New("bot_id is required")
	}
	if strings.TrimSpace(session.BotID) != "" && botID != strings.TrimSpace(session.BotID) {
		return "", errors.New("bot_id mismatch")
	}
	return botID, nil
}

func (e *Executor) resolvePlatform(args map[string]any, session SessionContext) (Platform, error) {
	platform := firstStringArg(args, "platform")
	if platform == "" {
		platform = strings.TrimSpace(session.CurrentPlatform)
	}
	if platform == "" {
		return "", errors.New("platform is required")
	}
	return e.Resolver.ParseChannelType(platform)
}

// ResolveAttachments converts raw attachment arguments into Attachment values.
func (e *Executor) ResolveAttachments(ctx context.Context, botID string, bundles []attachmentpkg.Bundle) []Attachment {
	var result []Attachment
	for _, bundle := range bundles {
		if att := e.resolveAttachmentBundle(ctx, botID, bundle); att != nil {
			result = append(result, *att)
		}
	}
	return result
}

func (e *Executor) resolveAttachmentBundle(ctx context.Context, botID string, bundle attachmentpkg.Bundle) *Attachment {
	att := AttachmentFromBundle(bundle)
	if att.Type == "" {
		att.Type = AttachmentFile
	}
	if strings.TrimSpace(att.ContentHash) != "" {
		return &att
	}
	if attachmentpkg.IsHTTPURL(bundle.URL) {
		att.URL = bundle.URL
		return &att
	}
	if bundle.Base64 != "" {
		att.Base64 = bundle.Base64
		return &att
	}
	ref := strings.TrimSpace(bundle.Path)
	if ref == "" {
		if strings.TrimSpace(att.PlatformKey) == "" {
			return nil
		}
		return &att
	}
	if storageKey := attachmentpkg.ExtractStorageKey(ref); storageKey != "" && e.AssetResolver != nil {
		asset, err := e.AssetResolver.GetByStorageKey(ctx, botID, storageKey)
		if err == nil {
			return AssetMetaToAttachment(asset, botID, string(att.Type), bundle.Name)
		}
	}
	if attachmentpkg.IsDataPath(ref) && e.AssetResolver != nil {
		asset, err := e.AssetResolver.IngestContainerFile(ctx, botID, ref)
		if err == nil {
			return AssetMetaToAttachment(asset, botID, string(att.Type), bundle.Name)
		}
		return nil
	}
	// att.Path is already set correctly by AttachmentFromBundle (bundle.Path = ref).
	return &att
}

// ParseOutboundMessage parses a message from tool call arguments.
func ParseOutboundMessage(arguments map[string]any, fallbackText string) (Message, error) {
	var msg Message
	if raw, ok := arguments["message"]; ok && raw != nil {
		switch value := raw.(type) {
		case string:
			msg.Text = strings.TrimSpace(value)
		case map[string]any:
			if err := validateOutboundMessageObject(value); err != nil {
				return Message{}, err
			}
			data, err := json.Marshal(value)
			if err != nil {
				return Message{}, err
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				return Message{}, err
			}
		default:
			return Message{}, errors.New("message must be object or string")
		}
	}
	if msg.IsEmpty() && strings.TrimSpace(fallbackText) != "" {
		msg.Text = strings.TrimSpace(fallbackText)
	}
	if msg.IsEmpty() {
		return Message{}, errOutboundMessageRequired
	}
	return msg, nil
}

func validateOutboundMessageObject(raw map[string]any) error {
	allowed := map[string]struct{}{
		"format": {}, "text": {}, "parts": {}, "attachments": {}, "actions": {}, "reply": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown message field %q", key)
		}
	}
	if format, ok := raw["format"]; ok && format != nil {
		normalized, err := validateOutboundMessageFormat(format)
		if err != nil {
			return err
		}
		raw["format"] = string(normalized)
	}
	if parts, ok := raw["parts"]; ok && parts != nil {
		if err := validateOutboundMessageParts(parts); err != nil {
			return err
		}
	}
	if attachments, ok := raw["attachments"]; ok && attachments != nil {
		if err := validateOutboundAttachmentInput(attachments); err != nil {
			return err
		}
	}
	if actions, ok := raw["actions"]; ok && actions != nil {
		if err := validateOutboundMessageActions(actions); err != nil {
			return err
		}
	}
	if reply, ok := raw["reply"]; ok && reply != nil {
		if err := validateOutboundMessageReply(reply); err != nil {
			return err
		}
	}
	return nil
}

func validateOutboundMessageFormat(raw any) (MessageFormat, error) {
	value, ok := raw.(string)
	if !ok {
		return "", errors.New("message format must be string")
	}
	switch format := MessageFormat(strings.TrimSpace(value)); format {
	case MessageFormatPlain, MessageFormatMarkdown, MessageFormatRich:
		return format, nil
	default:
		return "", fmt.Errorf("unsupported message format %q", value)
	}
}

func validateOutboundMessageReply(raw any) error {
	reply, ok := raw.(map[string]any)
	if !ok {
		return errors.New("message reply must be object")
	}
	allowed := map[string]struct{}{
		"message_id": {},
	}
	for key := range reply {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown message reply field %q", key)
		}
	}
	messageID, _ := reply["message_id"].(string)
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("message reply message_id is required")
	}
	reply["message_id"] = messageID
	return nil
}

func validateOutboundMessageParts(raw any) error {
	parts, ok := raw.([]any)
	if !ok {
		return errors.New("message parts must be array")
	}
	for i, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return fmt.Errorf("message part must be object at index %d", i)
		}
		if err := validateOutboundMessagePart(i, part); err != nil {
			return err
		}
	}
	return nil
}

func validateOutboundMessageActions(raw any) error {
	actions, ok := raw.([]any)
	if !ok {
		return errors.New("message actions must be array")
	}
	for i, rawAction := range actions {
		action, ok := rawAction.(map[string]any)
		if !ok {
			return fmt.Errorf("message action must be object at index %d", i)
		}
		if err := validateOutboundMessageAction(i, action); err != nil {
			return err
		}
	}
	return nil
}

func validateOutboundMessageAction(index int, raw map[string]any) error {
	allowed := map[string]struct{}{
		"type": {}, "label": {}, "url": {}, "row": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown message action field %q at index %d", key, index)
		}
	}
	label, _ := raw["label"].(string)
	if strings.TrimSpace(label) == "" {
		return fmt.Errorf("message action label is required at index %d", index)
	}
	rawURL, _ := raw["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("message action url is required at index %d", index)
	}
	if !attachmentpkg.IsHTTPURL(rawURL) {
		return fmt.Errorf("message action url must be http(s) at index %d", index)
	}
	return nil
}

func validateOutboundMessagePart(index int, raw map[string]any) error {
	allowed := map[string]struct{}{
		"type": {}, "text": {}, "url": {}, "styles": {}, "language": {},
		"channel_identity_id": {}, "emoji": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown message part field %q at index %d", key, index)
		}
	}
	partType, _ := raw["type"].(string)
	normalizedType := MessagePartType(strings.TrimSpace(partType))
	switch normalizedType {
	case MessagePartText, MessagePartLink, MessagePartCodeBlock, MessagePartMention, MessagePartEmoji, MessagePartHeading, MessagePartBlockquote, MessagePartListItem:
		raw["type"] = string(normalizedType)
	default:
		return fmt.Errorf("unsupported message part type %q at index %d", partType, index)
	}
	if err := validateOutboundMessagePartContent(index, normalizedType, raw); err != nil {
		return err
	}
	styles, ok := raw["styles"]
	if !ok || styles == nil {
		return nil
	}
	return normalizeOutboundMessagePartStyles(index, styles)
}

func normalizeOutboundMessagePartStyles(index int, styles any) error {
	switch styleItems := styles.(type) {
	case []any:
		for i, rawStyle := range styleItems {
			style, _ := rawStyle.(string)
			normalizedStyle := MessageTextStyle(strings.TrimSpace(style))
			switch normalizedStyle {
			case MessageStyleBold, MessageStyleItalic, MessageStyleStrikethrough, MessageStyleCode, MessageStyleUnderline, MessageStyleSpoiler:
				styleItems[i] = string(normalizedStyle)
			default:
				return fmt.Errorf("unsupported message part style %q at index %d", style, index)
			}
		}
	case []string:
		for i, style := range styleItems {
			normalizedStyle := MessageTextStyle(strings.TrimSpace(style))
			switch normalizedStyle {
			case MessageStyleBold, MessageStyleItalic, MessageStyleStrikethrough, MessageStyleCode, MessageStyleUnderline, MessageStyleSpoiler:
				styleItems[i] = string(normalizedStyle)
			default:
				return fmt.Errorf("unsupported message part style %q at index %d", style, index)
			}
		}
	default:
		return errors.New("message part styles must be array")
	}
	return nil
}

func validateOutboundMessagePartContent(index int, partType MessagePartType, raw map[string]any) error {
	text, _ := raw["text"].(string)
	text = strings.TrimSpace(text)
	switch partType {
	case MessagePartLink:
		rawURL, _ := raw["url"].(string)
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return fmt.Errorf("message link part url is required at index %d", index)
		}
		if !attachmentpkg.IsHTTPURL(rawURL) {
			return fmt.Errorf("message link part url must be http(s) at index %d", index)
		}
	case MessagePartMention:
		if text == "" {
			return fmt.Errorf("message mention part text is required at index %d", index)
		}
	case MessagePartEmoji:
		emoji, _ := raw["emoji"].(string)
		if text == "" && strings.TrimSpace(emoji) == "" {
			return fmt.Errorf("message emoji part text or emoji is required at index %d", index)
		}
	default:
		if text == "" {
			return fmt.Errorf("message part content is required at index %d", index)
		}
	}
	return nil
}

// AssetMetaToAttachment converts an AssetMeta to a Attachment.
func AssetMetaToAttachment(asset media.Asset, botID, attType, name string) *Attachment {
	bundle := attachmentpkg.Bundle{
		Type: attType,
		Name: name,
	}.WithAsset(botID, asset)
	att := AttachmentFromBundle(bundle)
	return &att
}

// InferAttachmentTypeFromMime infers attachment type from MIME type.
func InferAttachmentTypeFromMime(mime string) AttachmentType {
	return AttachmentType(attachmentpkg.InferTypeFromMime(mime))
}

// InferAttachmentTypeFromExt infers attachment type from file extension.
func InferAttachmentTypeFromExt(path string) AttachmentType {
	return AttachmentType(attachmentpkg.InferTypeFromExt(path))
}

func firstStringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if args == nil {
			continue
		}
		raw, ok := args[key]
		if !ok {
			continue
		}
		if s, ok := raw.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func boolArg(args map[string]any, key string) (bool, bool, error) {
	if args == nil {
		return false, false, nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return false, false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, true, errors.New(key + " must be a boolean")
	}
	return value, true, nil
}
