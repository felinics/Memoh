package application

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/contextview"
)

// acpContextViaContextView builds the ACP chat context view. The current user
// query joins the build as a KindCurrentUserMessage fragment so the manifest
// records the full request, but the chat renderer keeps it out of the context
// document: the query itself is delivered to the ACP runtime as the prompt by
// the caller. The returned manifest lets the caller record a context
// lifecycle snapshot alongside the persisted round. Legacy assembly fallback
// returns a content-light manifest that records why the view was not used.
func acpContextViaContextView(ctx context.Context, logger *slog.Logger, sections []contextview.ACPSection, query string) (string, string, *contextfrag.Manifest) {
	builder := contextview.NewBuilder(
		contextview.NewMapCollectorRegistry(&contextview.ACPSectionsCollector{}, &contextview.CurrentUserCollector{}),
		&contextview.FragmentSelector{},
		contextview.StablePrefixPlacer{},
		contextview.NewMapRendererRegistry(&contextview.ACPFullContextRenderer{Config: contextview.ACPRenderConfig{
			Mode:       contextview.ACPRenderModeChat,
			ContextURI: acpContextURI,
		}}),
	)
	view, err := builder.Build(ctx, contextview.BuildInput{
		Intent: contextfrag.IntentACPRuntimePrompt,
		Sources: []contextview.SourceSpec{
			{Name: "acp_sections", Config: contextview.ACPSectionsConfig{Sections: sections}},
			{Name: "current_user", Config: contextview.CurrentUserConfig{Query: query}},
		},
		Targets: []contextfrag.RenderTarget{contextfrag.RenderACPFullContext},
	})
	if err != nil {
		if logger != nil {
			logger.Error("acp context view build failed; assembling sections directly", slog.Any("error", err))
		}
		return finalizeACPSections(sections), acpContextURI, acpContextFallbackManifest("build_error")
	}
	rendered := view.Rendered[contextfrag.RenderACPFullContext]
	payload, ok := rendered.Data.(*contextview.ACPRenderedPayload)
	if !ok {
		if logger != nil {
			logger.Error("acp context view rendered unexpected payload; assembling sections directly")
		}
		return finalizeACPSections(sections), acpContextURI, acpContextFallbackManifest("render_payload_mismatch")
	}
	manifest := view.Manifest
	return payload.ContextMarkdown, payload.ContextURI, &manifest
}

func acpContextFallbackManifest(reason string) *contextfrag.Manifest {
	ledger := contextfrag.NewMutationLedger()
	ledger.Record(contextfrag.MutationContextViewFallback, reason)
	manifest := contextfrag.BuildManifest(nil)
	manifest.View = contextfrag.ViewACPRuntimePrompt
	manifest.Mutations = ledger
	return &manifest
}

func finalizeACPSections(sections []contextview.ACPSection) string {
	blocks := make([]string, 0, len(sections))
	for _, section := range sections {
		text := strings.TrimSpace(section.Text)
		if section.Budget.MaxChars > 0 && utf8.RuneCountInString(text) > section.Budget.MaxChars &&
			section.Budget.Overflow == contextfrag.OverflowDrop {
			continue
		}
		blocks = append(blocks, text)
	}
	return contextview.FinalizeACPContextMarkdown(blocks)
}
