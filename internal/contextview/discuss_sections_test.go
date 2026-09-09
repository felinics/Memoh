package contextview

import (
	"context"
	"reflect"
	"strings"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func discussSectionsInputFixture() DiscussContextInput {
	rc := timeline.RenderedContext{
		renderedTextSegment(100, "first rc"),
		renderedTextSegment(300, "second rc"),
	}
	trs := []timeline.TurnResponseEntry{{
		RequestedAtMs: 200,
		Role:          "assistant",
		Content:       "assistant turn",
	}}
	composed := timeline.ComposeContextWithArtifacts(rc, trs, []timeline.CompactionArtifact{{
		ID:      "fixture-artifact",
		Summary: "older summary",
	}})
	return DiscussContextInput{
		ComposedMessages: composed.Messages,
	}
}

func TestCollectDiscussSourceFragsUsesProvidedSystemFrags(t *testing.T) {
	t.Parallel()

	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "s1"}
	params := agentpkg.SystemPromptParams{
		SessionType: sessionmode.Discuss,
		Bot:         agentpkg.BotInfo{ID: "bot-1", Name: "research-bot"},
	}
	input := discussSectionsInputFixture()
	input.SystemFrags = agentpkg.SystemSectionFrags(agentpkg.GenerateSystemSections(params), scope)

	frags, err := (&DiscussSDKContextBuilder{}).CollectDiscussSourceFrags(
		context.Background(), scope, "REVERSE_PARSE_MUST_NOT_RUN", input)
	if err != nil {
		t.Fatalf("CollectDiscussSourceFrags error: %v", err)
	}
	if len(frags) <= len(input.SystemFrags) {
		t.Fatalf("frags = %d, want provided system frags followed by discuss frags", len(frags))
	}
	if !reflect.DeepEqual(frags[:len(input.SystemFrags)], input.SystemFrags) {
		t.Fatalf("leading frags = %#v, want the provided system frags verbatim", frags[:len(input.SystemFrags)])
	}
	kinds := make(map[contextfrag.Kind]bool, len(frags))
	for _, frag := range frags {
		kinds[frag.Kind] = true
		for _, part := range frag.Parts {
			if strings.Contains(part.Text, "REVERSE_PARSE_MUST_NOT_RUN") {
				t.Fatalf("flat system string must not be reverse-parsed when SystemFrags are provided: %#v", frag)
			}
		}
	}
	if !kinds[contextfrag.KindBotIdentity] {
		t.Fatalf("system frags must keep the typed bot identity kind, kinds = %#v", kinds)
	}
}

func TestCollectDiscussSourceFragsDoesNotAliasProvidedSystemFrags(t *testing.T) {
	t.Parallel()

	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "s1"}
	params := agentpkg.SystemPromptParams{
		SessionType: sessionmode.Discuss,
		Bot:         agentpkg.BotInfo{ID: "bot-1", Name: "research-bot"},
	}
	provided := agentpkg.SystemSectionFrags(agentpkg.GenerateSystemSections(params), scope)
	withSpareCap := make([]contextfrag.ContextFrag, len(provided), len(provided)+8)
	copy(withSpareCap, provided)
	input := discussSectionsInputFixture()
	input.SystemFrags = withSpareCap

	frags, err := (&DiscussSDKContextBuilder{}).CollectDiscussSourceFrags(
		context.Background(), scope, "", input)
	if err != nil {
		t.Fatalf("CollectDiscussSourceFrags error: %v", err)
	}
	if len(frags) <= len(withSpareCap) {
		t.Fatalf("frags = %d, want discuss frags after the system frags", len(frags))
	}
	firstDiscussID := frags[len(withSpareCap)].ID

	_ = append(withSpareCap, contextfrag.ContextFrag{ID: "caller-sentinel"})

	if got := frags[len(withSpareCap)].ID; got != firstDiscussID {
		t.Fatalf("returned frags share the caller's backing array: frag ID = %q, want %q", got, firstDiscussID)
	}
}

func TestCollectDiscussSourceFragsSectionsMatchReverseParseRender(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		params agentpkg.SystemPromptParams
	}{
		{
			name:   "minimal",
			params: agentpkg.SystemPromptParams{SessionType: sessionmode.Discuss},
		},
		{
			name: "full",
			params: agentpkg.SystemPromptParams{
				SessionType:               sessionmode.Discuss,
				Bot:                       agentpkg.BotInfo{ID: "bot-1", Name: "research-bot", DisplayName: "Research Bot", Timezone: "Asia/Shanghai"},
				Skills:                    []agentpkg.SkillEntry{{Name: "foo-skill", Description: "does foo things"}},
				Files:                     []agentpkg.SystemFile{{Filename: "AGENTS.md", Content: "workspace rules"}},
				Timezone:                  "Asia/Shanghai",
				PlatformIdentitiesSection: "## Platform identities\n\n- telegram: `12345`",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := contextfrag.Scope{BotID: "bot-1", SessionID: "s1"}
			system := agentpkg.GenerateSystemPrompt(tc.params)
			builder := &DiscussSDKContextBuilder{}

			legacyInput := discussSectionsInputFixture()
			legacyFrags, err := builder.CollectDiscussSourceFrags(context.Background(), scope, system, legacyInput)
			if err != nil {
				t.Fatalf("legacy CollectDiscussSourceFrags error: %v", err)
			}
			sectionsInput := discussSectionsInputFixture()
			sectionsInput.SystemFrags = agentpkg.SystemSectionFrags(agentpkg.GenerateSystemSections(tc.params), scope)
			sectionsFrags, err := builder.CollectDiscussSourceFrags(context.Background(), scope, system, sectionsInput)
			if err != nil {
				t.Fatalf("sections CollectDiscussSourceFrags error: %v", err)
			}

			const toolUsage = "## Tool usage\n\nuse tools wisely"
			legacyRendered, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
				ContextSourceFrags: legacyFrags,
				ContextScope:       scope,
				ContextToolUsage:   toolUsage,
			})
			if err != nil {
				t.Fatalf("legacy applyProviderRunConfig error: %v", err)
			}
			sectionsRendered, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
				ContextSourceFrags: sectionsFrags,
				ContextScope:       scope,
				ContextToolUsage:   toolUsage,
			})
			if err != nil {
				t.Fatalf("sections applyProviderRunConfig error: %v", err)
			}
			if sectionsRendered.System != legacyRendered.System {
				t.Fatalf("sections System diverges from reverse-parse System:\ngot:  %q\nwant: %q",
					sectionsRendered.System, legacyRendered.System)
			}
			if !reflect.DeepEqual(sectionsRendered.Messages, legacyRendered.Messages) {
				t.Fatalf("sections messages diverge:\ngot:  %#v\nwant: %#v",
					sectionsRendered.Messages, legacyRendered.Messages)
			}

			budgetedSectionsRendered, err := applyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
				SessionType:            sessionmode.Discuss,
				ContextSourceFrags:     sectionsFrags,
				ContextScope:           scope,
				ContextToolUsage:       toolUsage,
				ContextBudgetMaxTokens: 128000,
			})
			if err != nil {
				t.Fatalf("budgeted sections applyProviderRunConfig error: %v", err)
			}
			if budgetedSectionsRendered.ContextManifest.BudgetPlan == nil {
				t.Fatal("budgeted discuss sections did not produce an active plan")
			}
			if budgetedSectionsRendered.System != legacyRendered.System {
				t.Fatalf("budgeted sections System diverges from legacy System:\ngot:  %q\nwant: %q",
					budgetedSectionsRendered.System, legacyRendered.System)
			}
			if !reflect.DeepEqual(budgetedSectionsRendered.Messages, legacyRendered.Messages) {
				t.Fatalf("budgeted sections messages diverge:\ngot:  %#v\nwant: %#v",
					budgetedSectionsRendered.Messages, legacyRendered.Messages)
			}
		})
	}
}
