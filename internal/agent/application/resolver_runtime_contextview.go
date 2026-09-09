package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/contextview"
)

// runtimeContextViaContextView builds the runtime context view. The current user
// query joins the build as a KindCurrentUserMessage fragment so the manifest
// records the full request, but the chat renderer keeps it out of the context
// document: the query itself is delivered to the External Agent as the prompt by
// the caller. The returned manifest lets the caller record a context
// lifecycle snapshot alongside the persisted round. Legacy assembly fallback
// returns a content-light manifest that records why the view was not used.
func runtimeContextViaContextView(ctx context.Context, logger *slog.Logger, sections []contextview.RuntimeSection, query string) (string, string, *contextfrag.Manifest) {
	ledger := contextfrag.NewMutationLedger()
	builder := contextview.NewBuilder(
		contextview.NewMapCollectorRegistry(&contextview.RuntimeSectionsCollector{}, &contextview.CurrentUserCollector{}),
		&contextview.FragmentSelector{},
		contextview.StablePrefixPlacer{},
		contextview.NewMapRendererRegistry(&contextview.RuntimeFullContextRenderer{Config: contextview.RuntimeRenderConfig{
			Mode:       contextview.RuntimeRenderModeChat,
			ContextURI: runtimeContextURI,
		}}),
	)
	view, err := builder.Build(ctx, contextview.BuildInput{
		Intent: contextfrag.IntentExternalAgentPrompt,
		Sources: []contextview.SourceSpec{
			{Name: "runtime_sections", Config: contextview.RuntimeSectionsConfig{Sections: sections}},
			{Name: "current_user", Config: contextview.CurrentUserConfig{Query: query}},
		},
		Targets:   []contextfrag.RenderTarget{contextfrag.RenderRuntimeFullContext},
		Mutations: ledger,
	})
	if err != nil {
		if logger != nil {
			logger.Error("runtime context view build failed; assembling sections directly", slog.Any("error", err))
		}
		return finalizeRuntimeSectionsWithAudit(sections, ledger), runtimeContextURI, runtimeContextFallbackManifest("build_error", ledger)
	}
	rendered := view.Rendered[contextfrag.RenderRuntimeFullContext]
	payload, ok := rendered.Data.(*contextview.RuntimeRenderedPayload)
	if !ok {
		if logger != nil {
			logger.Error("runtime context view rendered unexpected payload; assembling sections directly")
		}
		return finalizeRuntimeSectionsWithAudit(sections, ledger), runtimeContextURI, runtimeContextFallbackManifest("render_payload_mismatch", ledger)
	}
	manifest := view.Manifest
	return payload.ContextMarkdown, payload.ContextURI, &manifest
}

func runtimeContextFallbackManifest(reason string, ledger *contextfrag.MutationLedger) *contextfrag.Manifest {
	if ledger == nil {
		ledger = contextfrag.NewMutationLedger()
	}
	ledger.Record(contextfrag.MutationContextViewFallback, reason)
	manifest := contextfrag.BuildManifest(nil)
	manifest.View = contextfrag.ViewExternalAgentPrompt
	manifest.Mutations = ledger
	return &manifest
}

func finalizeRuntimeSections(sections []contextview.RuntimeSection) string {
	return finalizeRuntimeSectionsWithAudit(sections, nil)
}

func finalizeRuntimeSectionsWithAudit(sections []contextview.RuntimeSection, ledger *contextfrag.MutationLedger) string {
	blocks := make([]string, 0, len(sections))
	for _, section := range sections {
		text := strings.TrimSpace(section.Text)
		if section.Budget.MaxChars > 0 && utf8.RuneCountInString(text) > section.Budget.MaxChars &&
			section.Budget.Overflow == contextfrag.OverflowDrop {
			continue
		}
		blocks = append(blocks, text)
	}
	joined := contextview.JoinRuntimeContextBlocks(blocks)
	finalized := contextview.FinalizeRuntimeContextMarkdownFromJoined(joined)
	if finalized != joined {
		ledger.Record(
			contextfrag.MutationRendererPrune,
			fmt.Sprintf("runtime_context_bytes:%d->%d", len(joined), len(finalized)),
		)
	}
	return finalized
}
