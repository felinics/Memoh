package application

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestDiscussComposedInputSurvivesEchoThroughProviderAdmission(t *testing.T) {
	for _, size := range []int{1600, 10000, 80000} {
		t.Run(strings.Repeat("x", size/10000+1), func(t *testing.T) {
			service, resolver, _, _, cmd := discussBudgetRecoveryFixture(t)
			configureDiscussLifecycle(service)
			provider := &triggerLifecycleProvider{}
			resolver.resolveResult.RunConfig.Model.Provider = provider
			input := strings.Repeat("u", size)
			rc := timeline.RenderedContext{
				{MessageID: "old", ReceivedAtMs: 1, Content: []timeline.RenderedContentPiece{{Type: "text", Text: strings.Repeat("h", 1200)}}},
				{MessageID: "current", ReceivedAtMs: 2, Content: []timeline.RenderedContentPiece{{Type: "text", Text: input}}},
				{MessageID: "echo", ReceivedAtMs: 3, IsSelfSent: true, Content: []timeline.RenderedContentPiece{{Type: "text", Text: "self echo"}}},
			}
			composed := timeline.ComposeContextWithCheckpoint(rc, nil, timeline.HistoryCheckpoint{})
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

func TestDiscussAdmissionKeepsNewestFittingSuffixOfCurrentBatch(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "user", Content: strings.Repeat("a", 2400), Source: &turn.ContextMessageSource{Kind: "external", ID: "a", Current: true}},
		{Role: "user", Content: strings.Repeat("b", 2400), Source: &turn.ContextMessageSource{Kind: "external", ID: "b", Current: true}},
		{Role: "user", Content: "echo", Source: &turn.ContextMessageSource{Kind: "self", ID: "echo"}},
	}
	for _, admit := range []func([]turn.DiscussMessage, int) ([]turn.DiscussMessage, discussAdmission){admitDiscussMessages, admitDiscussAgentMessages} {
		kept, admission := admit(messages, 1000)
		if admission.ProtectedOverflow || len(admission.ProtectedSources) != 1 || admission.ProtectedSources[0].ID != "b" || admission.ProtectedTokens != 600 {
			t.Fatalf("an oversized batch must protect its newest fitting suffix: %+v", admission)
		}
		if len(admission.OmittedSources) != 1 || admission.OmittedSources[0].ID != "a" || admission.RecoveryBudgetTokens <= 0 || len(kept) != 2 {
			t.Fatalf("older input must be reported for recovery, with room left to compact it: %+v kept=%d", admission, len(kept))
		}
		if _, admission := admit(messages[1:2], 500); !admission.ProtectedOverflow {
			t.Fatalf("the newest input alone must never be dropped: %+v", admission)
		}
	}
}

// A Discuss recovery Channel asks for without materialized messages still
// names the batch it answers: the fold keeps it newest first while it leaves
// the summary room, priced with the rest of the history.
func TestDiscussRecoveryCarriesCurrentSourcesEvenWithoutMaterializedMessages(t *testing.T) {
	service, runner := newControllerPolicyService(t)
	sources := []turn.ContextMessageSource{{Kind: "external", ID: "a", Current: true}, {Kind: "external", ID: "b", Current: true}}
	cmd := turn.StartTurnCommand{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, DiscussContextOverflow: true, DiscussContextTokens: 2000, DiscussCurrentTokens: 100, DiscussCurrentSources: sources}
	if recomposed, err := service.maybeSyncCompactDiscuss(t.Context(), cmd, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery"); !recomposed || err != nil || len(runner.configs) != 1 {
		t.Fatalf("recovery did not run: %v", err)
	}
	cfg := runner.configs[0]
	if !reflect.DeepEqual(cfg.Anchors, sources) || cfg.AnchorPolicy != compaction.AnchorsFirst || cfg.HistoryBudgetTokens != 1000 {
		t.Fatalf("Channel overflow must keep the batch newest first and compact what does not fit beside it: %+v", cfg)
	}
}
