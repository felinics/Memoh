package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/contextview"
)

const probeActivationMarker = "at least one message MUST have been sent"

func sdkMessageText(message sdk.Message) string {
	var b strings.Builder
	for _, part := range message.Content {
		if text, ok := part.(sdk.TextPart); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func joinedMessageText(messages []sdk.Message) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(sdkMessageText(message))
		b.WriteString("\n")
	}
	return b.String()
}

// runActivatedDiscussTurn drives a full discuss turn with the gate forced open
// and returns the config the agent received.
func runActivatedDiscussTurn(t *testing.T, cmd turn.StartTurnCommand, base native.RunConfig, inline func(context.Context, string, []timeline.ImageAttachmentRef) []sdk.ImagePart) *native.RunConfig {
	t.Helper()
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{RunConfig: base, ModelID: "model-1"},
		inlineFn:      inline,
	}
	service := newDiscussTestService(&fakeRunner{}, agent, resolver)
	service.turnHooks.discussProbe = func(context.Context, turn.StartTurnCommand, ResolveRunConfigResult) discussProbeResult {
		return discussProbeResult{Ran: true, Activated: true, Outcome: discussProbeOutcomeAct, Reason: "they asked a question"}
	}

	handle, err := service.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, handle)
	if agent.lastConfig == nil {
		t.Fatal("expected the agent to be called")
	}
	return agent.lastConfig
}

// The activation contract has to survive the provider context compiler, not
// merely land in RunConfig.Messages. Production installs
// ProviderRunConfigApplier, which rebuilds Messages from ContextSourceFrags and
// discards anything that only ever existed in the message slice.
func TestDiscussActivationSurvivesProviderContextView(t *testing.T) {
	cfg := runActivatedDiscussTurn(t, discussCommand(), native.RunConfig{}, nil)

	if !strings.Contains(joinedMessageText(cfg.Messages), probeActivationMarker) {
		t.Fatalf("activation missing from RunConfig.Messages:\n%s", joinedMessageText(cfg.Messages))
	}

	rendered, err := contextview.ProviderRunConfigApplier(slog.New(slog.DiscardHandler))(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("ProviderRunConfigApplier() error = %v", err)
	}
	text := joinedMessageText(rendered.Messages)
	if !strings.Contains(text, probeActivationMarker) {
		t.Fatalf("activation did not survive the provider context view; the model never sees it:\n%s", text)
	}
	if !strings.Contains(text, "they asked a question") {
		t.Fatalf("evaluator reason did not survive the provider context view:\n%s", text)
	}

	last := rendered.Messages[len(rendered.Messages)-1]
	if !strings.Contains(sdkMessageText(last), probeActivationMarker) {
		t.Fatalf("activation is not the final message; tail placement is the point:\n%s", sdkMessageText(last))
	}
}

// Inline vision attachments must stay on the newest chat message. The
// activation is itself a trailing user message, so a naive "last user message"
// scan would staple the image onto the instruction instead.
func TestDiscussActivationDoesNotCaptureInlineImages(t *testing.T) {
	cmd := discussCommand()
	cmd.DiscussImageRefs = []turn.DiscussImageRef{{ContentHash: "img-hash", Mime: "image/jpeg"}}
	base := native.RunConfig{SupportsImageInput: true}
	inline := func(context.Context, string, []timeline.ImageAttachmentRef) []sdk.ImagePart {
		return []sdk.ImagePart{{Image: "data:image/jpeg;base64,FAKE", MediaType: "image/jpeg"}}
	}

	cfg := runActivatedDiscussTurn(t, cmd, base, inline)
	rendered, err := contextview.ProviderRunConfigApplier(slog.New(slog.DiscardHandler))(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("ProviderRunConfigApplier() error = %v", err)
	}

	var carrier sdk.Message
	found := false
	for _, message := range rendered.Messages {
		for _, part := range message.Content {
			if _, ok := part.(sdk.ImagePart); ok {
				carrier, found = message, true
			}
		}
	}
	if !found {
		t.Fatal("inline image did not reach the provider request")
	}
	if strings.Contains(sdkMessageText(carrier), probeActivationMarker) {
		t.Fatalf("inline image was attached to the activation instruction instead of the chat message:\n%s", sdkMessageText(carrier))
	}
}
