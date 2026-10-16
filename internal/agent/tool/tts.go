package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/messaging"
	"github.com/felinics/memoh/internal/settings"
)

const ttsMaxTextLen = 500

// TTSSender sends outbound messages through the channel manager.
type ttsSettings interface {
	GetBot(ctx context.Context, botID string) (settings.Settings, error)
}

type ttsAudio interface {
	Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error)
}

type TTSProvider struct {
	logger   *slog.Logger
	settings ttsSettings
	audio    ttsAudio
	sender   messaging.Sender
	resolver messaging.ChannelTypeResolver
}

func NewTTSProvider(log *slog.Logger, settingsSvc *settings.Service, audioSvc *audiopkg.Service, sender messaging.Sender, resolver messaging.ChannelTypeResolver) *TTSProvider {
	if log == nil {
		log = slog.Default()
	}
	var settingsDep ttsSettings
	if settingsSvc != nil {
		settingsDep = settingsSvc
	}
	var audioDep ttsAudio
	if audioSvc != nil {
		audioDep = audioSvc
	}
	return &TTSProvider{
		logger:   log.With(slog.String("tool", "tts")),
		settings: settingsDep,
		audio:    audioDep,
		sender:   sender,
		resolver: resolver,
	}
}

func (*TTSProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	ref, ok := available.Ref(ToolSpeak())
	if !ok {
		return ""
	}
	text := ref + ": Send a voice message."
	if session.CanOmitMessagingTarget() {
		text += " Omit `target` to speak in the current conversation; specify `target` for another channel/person."
	} else {
		text += " Specify `platform` and `target` in this session."
	}
	return usageSection("Voice messaging", []string{
		text,
	})
}

func (p *TTSProvider) Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.settings == nil || p.audio == nil || p.sender == nil || p.resolver == nil {
		return nil, nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, nil
	}
	if strings.TrimSpace(botSettings.TtsModelID) == "" {
		return nil, nil
	}
	return p.speakTools(session), nil
}

type speakArgs struct {
	Text     string `json:"text" jsonschema:"The text to convert to speech (max 500 characters)"`
	Platform string `json:"platform,omitempty"`
	Target   string `json:"target,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty" jsonschema:"Message ID to reply to. The voice message will reference this message on the platform."`
}

// speakTools builds the speak tool for a session that has passed the
// settings gate; the platform/target text and the required set follow the
// session.
func (p *TTSProvider) speakTools(session SessionContext) []toolexec.Tool {
	sess := session
	description, platformDescription, targetDescription, required := speakToolPromptMetadata(session)
	return []toolexec.Tool{
		toolexec.Define(ToolSpeak().String(), description,
			func(execCtx *toolexec.ToolExecContext, args speakArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execSpeak(execCtx.Context, sess, execCtx.ToolCallID, args))
			},
			toolexec.Describe("platform", platformDescription),
			toolexec.Describe("target", targetDescription),
			toolexec.Require(required...),
		),
	}
}

func speakToolPromptMetadata(session SessionContext) (description string, platformDescription string, targetDescription string, required []string) {
	if session.CanOmitMessagingTarget() {
		return "Send a voice message. When target is omitted, speaks in the current conversation. When target is specified, sends to that channel/person. Synthesizes text to speech and delivers as audio.",
			"Channel platform name. Defaults to current session platform.",
			"Channel target (chat/group/thread ID). Optional — omit to speak in the current conversation.",
			[]string{"text"}
	}
	return "Send a voice message. Specify platform and target when speaking to a person or channel from this session. Synthesizes text to speech and delivers as audio.",
		"Channel platform name. Required in this session.",
		"Channel target (chat/group/thread ID). Required in this session.",
		[]string{"text", "platform", "target"}
}

func (p *TTSProvider) execSpeak(ctx context.Context, session SessionContext, toolCallID string, args speakArgs) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return nil, errors.New("text is required")
	}
	if len([]rune(text)) > ttsMaxTextLen {
		return nil, errors.New("text too long, max 500 characters")
	}
	channelType, err := p.resolvePlatform(args.Platform, session)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(args.Target)
	if target == "" {
		target = defaultSpeakTargetForPlatform(args.Platform, session, channelType)
	}
	if target == "" {
		return nil, errors.New("target is required for cross-conversation speak")
	}

	isSameConv := session.IsSameConversation(channelType.String(), target)
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, errors.New("failed to load bot settings")
	}
	if botSettings.TtsModelID == "" {
		return nil, errors.New("bot has no TTS model configured")
	}
	audioData, contentType, synthErr := p.audio.Synthesize(ctx, botSettings.TtsModelID, text, nil)
	if synthErr != nil {
		return nil, fmt.Errorf("speech synthesis failed: %s", synthErr.Error())
	}

	dataURL := fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(audioData))

	// Same-conversation: emit the synthesized audio as a voice attachment.
	if isSameConv && session.CanUseLocalMessagingShortcut() {
		session.Emitter(ToolStreamEvent{
			Type:       StreamEventAttachment,
			ToolCallID: toolCallID,
			Attachments: []Attachment{{
				Type: "voice",
				URL:  dataURL,
				Mime: contentType,
				Size: int64(len(audioData)),
			}},
		})
		return map[string]any{
			"ok":        true,
			"delivered": "current_conversation",
		}, nil
	}
	msg := messaging.Message{
		Attachments: []messaging.Attachment{{Type: messaging.AttachmentVoice, URL: dataURL, Mime: contentType, Size: int64(len(audioData))}},
	}
	if replyTo := strings.TrimSpace(args.ReplyTo); replyTo != "" {
		msg.Reply = &messaging.ReplyRef{MessageID: replyTo}
	}
	if err := p.sender.Send(ctx, botID, channelType, messaging.SendRequest{Target: target, Message: msg}); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "bot_id": botID, "platform": channelType.String(), "target": target,
		"instruction": "Voice message delivered successfully. You have completed your response. Please STOP now and do not call any more tools.",
	}, nil
}

func defaultSpeakTargetForPlatform(requestedPlatform string, session SessionContext, channelType messaging.Platform) string {
	if !session.CanOmitMessagingTarget() {
		return ""
	}
	if strings.TrimSpace(requestedPlatform) != "" && !strings.EqualFold(channelType.String(), strings.TrimSpace(session.CurrentPlatform)) {
		return ""
	}
	return strings.TrimSpace(session.ReplyTarget)
}

func (p *TTSProvider) resolvePlatform(requested string, session SessionContext) (messaging.Platform, error) {
	platform := strings.TrimSpace(requested)
	if platform == "" {
		platform = strings.TrimSpace(session.CurrentPlatform)
	}
	if platform == "" {
		return "", errors.New("platform is required")
	}
	return p.resolver.ParseChannelType(platform)
}
