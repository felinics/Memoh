package contextview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const defaultRuntimeContextURI = "memoh://context/current-turn"

type RuntimeRenderedPayload struct {
	ContextMarkdown string
	ContextURI      string
	ContentHash     string
}

type RuntimeRenderMode string

const RuntimeRenderModeChat RuntimeRenderMode = "chat"

type RuntimeRenderConfig struct {
	Mode            RuntimeRenderMode
	ContextMarkdown string
	ContextURI      string
}

type RuntimeFullContextRenderer struct {
	Config RuntimeRenderConfig
}

func (*RuntimeFullContextRenderer) Target() contextfrag.RenderTarget {
	return contextfrag.RenderRuntimeFullContext
}

func (r *RuntimeFullContextRenderer) Render(_ context.Context, input RenderInput) (RenderedPayload, error) {
	if r.Config.Mode != "" && r.Config.Mode != RuntimeRenderModeChat {
		return RenderedPayload{}, errors.New("runtime context render: unsupported render mode")
	}
	markdown := r.Config.ContextMarkdown
	if hasNonCurrentUserSelection(input.Selected) {
		rendered, err := renderRuntimeSelectedMarkdown(input)
		if err != nil {
			return RenderedPayload{}, err
		}
		markdown = rendered
	} else if strings.TrimSpace(markdown) == "" {
		return RenderedPayload{}, errors.New("runtime context render: no selected context fragments and no legacy context markdown")
	}
	uri := r.Config.ContextURI
	if uri == "" {
		uri = defaultRuntimeContextURI
	}
	hash := runtimeTextContentHash(markdown)
	payload := &RuntimeRenderedPayload{
		ContextMarkdown: markdown,
		ContextURI:      uri,
		ContentHash:     hash,
	}
	return RenderedPayload{
		Target:      contextfrag.RenderRuntimeFullContext,
		ContentHash: hash,
		Data:        payload,
	}, nil
}

func hasNonCurrentUserSelection(selected []contextfrag.ContextFrag) bool {
	for _, frag := range selected {
		if frag.Slot != contextfrag.SlotCurrentUser {
			return true
		}
	}
	return false
}

func renderRuntimeSelectedMarkdown(input RenderInput) (string, error) {
	ordered, err := orderedSelectedFrags(input.Selected, input.Placement)
	if err != nil {
		return "", err
	}
	blocks := make([]string, 0, len(ordered))
	for _, frag := range ordered {
		if frag.Slot == contextfrag.SlotCurrentUser {
			continue
		}
		for _, part := range frag.Parts {
			if part.Type != contextfrag.PartText {
				continue
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				blocks = append(blocks, text)
			}
		}
	}
	joined := JoinRuntimeContextBlocks(blocks)
	finalized := FinalizeRuntimeContextMarkdownFromJoined(joined)
	if finalized != joined && input.Manifest != nil {
		input.Manifest.Mutations.Record(
			contextfrag.MutationRendererPrune,
			fmt.Sprintf("runtime_context_bytes:%d->%d", len(joined), len(finalized)),
		)
	}
	return finalized, nil
}

func runtimeTextContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
