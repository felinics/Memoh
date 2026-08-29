package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/messaging"
	"github.com/felinics/memoh/internal/settings"
)

type ttsTestSettings struct{}

func (ttsTestSettings) GetBot(context.Context, string) (settings.Settings, error) {
	return settings.Settings{TtsModelID: "tts-model-1"}, nil
}

type ttsTestAudio struct {
	called int
}

func (a *ttsTestAudio) Synthesize(context.Context, string, string, map[string]any) ([]byte, string, error) {
	a.called++
	return []byte("audio"), "audio/ogg", nil
}

type ttsRecordingSender struct {
	called int
	req    messaging.SendRequest
}

func (s *ttsRecordingSender) Send(_ context.Context, _ string, _ messaging.Platform, req messaging.SendRequest) error {
	s.called++
	s.req = req
	return nil
}

func TestExecSpeakRequiresTargetBeforeLoadingSettings(t *testing.T) {
	t.Parallel()

	provider := NewTTSProvider(nil, nil, nil, nil, usageTestResolver{})
	_, err := provider.execSpeak(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
	}, "call-test", map[string]any{
		"text": "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("execSpeak error = %v, want target is required", err)
	}
}

func TestExecSpeakWithDifferentPlatformDoesNotReuseCurrentTarget(t *testing.T) {
	t.Parallel()

	provider := NewTTSProvider(nil, nil, nil, nil, usageTestResolver{})
	_, err := provider.execSpeak(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
		ReplyTarget:     "telegram-chat-1",
	}, "call-test", map[string]any{
		"platform": "discord",
		"text":     "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("execSpeak error = %v, want target is required", err)
	}
}

func TestTTSProviderToolsTreatTypedNilDependenciesAsUnavailable(t *testing.T) {
	t.Parallel()

	provider := NewTTSProvider(nil, nil, nil, usageTestSender{}, usageTestResolver{})
	tools, err := provider.Tools(context.Background(), SessionContext{BotID: "bot_1"})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools with nil typed dependencies, got %d", len(tools))
	}
}

func TestExecSpeakCurrentConversationCollectingEmitterUsesChannelAdapter(t *testing.T) {
	t.Parallel()

	sender := &ttsRecordingSender{}
	provider := &TTSProvider{
		settings: ttsTestSettings{},
		audio:    &ttsTestAudio{},
		sender:   sender,
		resolver: usageTestResolver{},
	}
	var emitted bool
	_, err := provider.execSpeak(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		Emitter: func(ToolStreamEvent) {
			emitted = true
		},
	}, "call-test", map[string]any{
		"text": "hello",
	})
	if err != nil {
		t.Fatalf("execSpeak returned error: %v", err)
	}
	if emitted {
		t.Fatal("non-live collecting emitter should not be used for current-conversation speak")
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
}

func TestExecSpeakCurrentConversationLiveStreamUsesEmitter(t *testing.T) {
	t.Parallel()

	sender := &ttsRecordingSender{}
	provider := &TTSProvider{
		settings: ttsTestSettings{},
		audio:    &ttsTestAudio{},
		sender:   sender,
		resolver: usageTestResolver{},
	}
	var emitted int
	_, err := provider.execSpeak(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		LiveStream:      true,
		Emitter: func(ToolStreamEvent) {
			emitted++
		},
	}, "call-test", map[string]any{
		"text": "hello",
	})
	if err != nil {
		t.Fatalf("execSpeak returned error: %v", err)
	}
	if emitted != 1 {
		t.Fatalf("expected live stream emitter called once, got %d", emitted)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called for live stream shortcut, got %d", sender.called)
	}
}

func TestExecSpeakDiscussCurrentTargetUsesChannelAdapter(t *testing.T) {
	t.Parallel()

	sender := &ttsRecordingSender{}
	provider := &TTSProvider{
		settings: ttsTestSettings{},
		audio:    &ttsTestAudio{},
		sender:   sender,
		resolver: usageTestResolver{},
	}
	var emitted bool
	_, err := provider.execSpeak(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Discuss,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		Emitter: func(ToolStreamEvent) {
			emitted = true
		},
	}, "call-test", map[string]any{
		"platform": "telegram",
		"target":   "chat-1",
		"text":     "hello",
	})
	if err != nil {
		t.Fatalf("execSpeak returned error: %v", err)
	}
	if emitted {
		t.Fatal("discuss speak should not use local stream emitter")
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
}
