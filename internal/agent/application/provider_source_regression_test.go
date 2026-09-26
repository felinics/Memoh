package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestDiscussComposedInputSurvivesEchoThroughProviderAdmission(t *testing.T) {
	for _, size := range []int{1600, 10000, 80000} {
		t.Run(strings.Repeat("x", size/10000+1), func(t *testing.T) {
			service, resolver, _, _, cmd := discussBudgetRecoveryFixture(t)
			service.SetSyncCompactionMode("off")
			configureDiscussLifecycle(service)
			provider := &triggerLifecycleProvider{}
			resolver.resolveResult.RunConfig.Model.Provider = provider
			input := strings.Repeat("u", size)
			rc := timeline.RenderedContext{
				{MessageID: "old", ReceivedAtMs: 1, Content: []timeline.RenderedContentPiece{{Type: "text", Text: strings.Repeat("h", 20000)}}},
				{MessageID: "current", ReceivedAtMs: 2, Content: []timeline.RenderedContentPiece{{Type: "text", Text: input}}},
				{MessageID: "echo", ReceivedAtMs: 3, IsSelfSent: true, Content: []timeline.RenderedContentPiece{{Type: "text", Text: "self echo"}}},
			}
			composed := timeline.ComposeContext(rc, nil)
			wire, err := json.Marshal(composed.Messages)
			if err != nil {
				t.Fatal(err)
			}
			cmd.DiscussMessages = nil
			if err := json.Unmarshal(wire, &cmd.DiscussMessages); err != nil {
				t.Fatal(err)
			}
			handle, err := service.StartTurn(t.Context(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			for range handle.Events() {
			}
			var failed bool
			for err := range handle.Errs() {
				failed = failed || err != nil
			}
			if size == 1600 {
				if failed || provider.callCount() != 1 || resolver.storeCalls != 1 || countRecoveryText(provider.params.Messages, input) != 1 {
					t.Fatalf("current lost: failed=%v provider=%d stores=%d", failed, provider.callCount(), resolver.storeCalls)
				}
			} else if !failed || provider.callCount() != 0 || resolver.storeCalls != 0 {
				t.Fatalf("oversized input silently consumed: failed=%v provider=%d stores=%d", failed, provider.callCount(), resolver.storeCalls)
			}
		})
	}
}

func TestDiscussAdmissionProtectsExplicitCurrentBatch(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "user", Content: strings.Repeat("a", 2400), Source: &turn.ContextMessageSource{Kind: "external", ID: "a", Current: true}},
		{Role: "user", Content: strings.Repeat("b", 2400), Source: &turn.ContextMessageSource{Kind: "external", ID: "b", Current: true}},
		{Role: "user", Content: "echo", Source: &turn.ContextMessageSource{Kind: "self", ID: "echo"}},
	}
	for _, admit := range []func([]turn.DiscussMessage, int) ([]turn.DiscussMessage, discussAdmission){admitDiscussMessages, admitDiscussAgentMessages} {
		_, admission := admit(messages, 1000)
		if !admission.ProtectedOverflow || admission.RecoveryBudgetTokens != 0 {
			t.Fatalf("batch partially admitted: %+v", admission)
		}
	}
}
