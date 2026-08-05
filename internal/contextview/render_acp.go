package contextview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

const defaultACPContextURI = "memoh://context/current-turn"

type ACPRenderedPayload struct {
	ContextMarkdown string
	ContextURI      string
	ContentHash     string
}

type ACPRenderMode string

const ACPRenderModeChat ACPRenderMode = "chat"

type ACPRenderConfig struct {
	Mode            ACPRenderMode
	ContextMarkdown string
	ContextURI      string
}

type ACPFullContextRenderer struct {
	Config ACPRenderConfig
}

func (*ACPFullContextRenderer) Target() contextfrag.RenderTarget {
	return contextfrag.RenderACPFullContext
}

func (r *ACPFullContextRenderer) Render(_ context.Context, input RenderInput) (RenderedPayload, error) {
	if r.Config.Mode != "" && r.Config.Mode != ACPRenderModeChat {
		return RenderedPayload{}, errors.New("acp chat render: unsupported render mode")
	}
	markdown := r.Config.ContextMarkdown
	if hasNonCurrentUserSelection(input.Selected) {
		rendered, err := renderACPSelectedMarkdown(input)
		if err != nil {
			return RenderedPayload{}, err
		}
		markdown = rendered
	} else if strings.TrimSpace(markdown) == "" {
		return RenderedPayload{}, errors.New("acp chat render: no selected context fragments and no legacy context markdown")
	}
	uri := r.Config.ContextURI
	if uri == "" {
		uri = defaultACPContextURI
	}
	hash := acpTextContentHash(markdown)
	payload := &ACPRenderedPayload{
		ContextMarkdown: markdown,
		ContextURI:      uri,
		ContentHash:     hash,
	}
	return RenderedPayload{
		Target:      contextfrag.RenderACPFullContext,
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

func renderACPSelectedMarkdown(input RenderInput) (string, error) {
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
	return FinalizeACPContextMarkdown(blocks), nil
}

func acpTextContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
