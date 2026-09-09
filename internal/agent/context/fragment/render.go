package contextfrag

import (
	"sort"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// BuildManifest creates a non-sensitive summary from fragments.
func BuildManifest(frags []ContextFrag) Manifest {
	manifest := Manifest{
		SchemaVersions: DefaultSchemaVersions(),
		SlotPolicies:   DefaultSlotRenderPolicies(),
	}
	manifest.Items = make([]ManifestItem, 0, len(frags))
	for _, frag := range frags {
		ref := frag.Ref
		if err := ValidateContextRef(ref); err != nil {
			normalized := WithContextRef(frag, ref)
			ref = normalized.Ref
		}
		manifest.ValidationWarnings = append(manifest.ValidationWarnings, ContextRefWarnings(ref)...)
		item := ManifestItem{
			ID:                 frag.ID,
			Ref:                ref,
			Kind:               frag.Kind,
			Slot:               frag.Slot,
			Role:               frag.Role,
			Priority:           frag.Priority,
			RetentionTier:      frag.RetentionTier,
			DropPriority:       frag.DropPriority,
			RequiredCapability: frag.RequiredCapability,
			CacheClass:         frag.CacheClass,
			Trust:              frag.Trust,
			Source:             frag.Provenance.Source,
			SourceID:           frag.Provenance.SourceID,
			Collector:          frag.Provenance.Collector,
			ConflictKey:        frag.ConflictKey,
			Render:             frag.Render,
			Scope:              frag.Scope,
		}
		for _, part := range frag.Parts {
			item.PartTypes = append(item.PartTypes, part.Type)
			switch part.Type {
			case PartText:
				item.TextBytes += len(part.Text)
			case PartSDKMessage:
				item.TextBytes += messageTextBytes(partMessage(part))
				item.ImageCount += messageImageCount(partMessage(part))
				manifest.Counts.Messages++
			case PartImage:
				item.ImageCount++
			}
		}
		item.TokenEstimate = ResolveFragTokens(frag)
		manifest.Counts.TextBytes += item.TextBytes
		manifest.Counts.Images += item.ImageCount
		manifest.Counts.TokenEstimate += item.TokenEstimate
		if frag.Coverage != nil {
			manifest.CoverageTrace = append(manifest.CoverageTrace, *frag.Coverage)
		}
		manifest.Items = append(manifest.Items, item)
	}
	manifest.Counts.Fragments = len(frags)
	manifest.Breakdown = breakdownFromItems(manifest.Items)
	manifest.TrustBreakdown = trustBreakdownFromItems(manifest.Items)
	manifest.RenderedOutputs = renderedOutputRefs(frags)
	return manifest
}

// trustBreakdownFromItems rolls manifest items up by TrustLevel, ordered by
// descending token estimate with Trust as the tie-breaker.
func trustBreakdownFromItems(items []ManifestItem) []TrustBreakdown {
	if len(items) == 0 {
		return nil
	}
	byTrust := make(map[TrustLevel]*TrustBreakdown, 4)
	for _, item := range items {
		entry, ok := byTrust[item.Trust]
		if !ok {
			entry = &TrustBreakdown{Trust: item.Trust}
			byTrust[item.Trust] = entry
		}
		entry.Fragments++
		entry.TokenEstimate += item.TokenEstimate
		entry.TextBytes += item.TextBytes
		entry.Images += item.ImageCount
	}
	out := make([]TrustBreakdown, 0, len(byTrust))
	for _, entry := range byTrust {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TokenEstimate != out[j].TokenEstimate {
			return out[i].TokenEstimate > out[j].TokenEstimate
		}
		return out[i].Trust < out[j].Trust
	})
	return out
}

// breakdownFromItems rolls manifest items up by Kind, ordered by descending
// token estimate with Kind as the tie-breaker so the output is deterministic.
func breakdownFromItems(items []ManifestItem) []KindBreakdown {
	if len(items) == 0 {
		return nil
	}
	byKind := make(map[Kind]*KindBreakdown, len(items))
	for _, item := range items {
		entry, ok := byKind[item.Kind]
		if !ok {
			entry = &KindBreakdown{Kind: item.Kind}
			byKind[item.Kind] = entry
		}
		entry.Fragments++
		entry.TokenEstimate += item.TokenEstimate
		entry.TextBytes += item.TextBytes
		entry.Images += item.ImageCount
	}
	out := make([]KindBreakdown, 0, len(byKind))
	for _, entry := range byKind {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TokenEstimate != out[j].TokenEstimate {
			return out[i].TokenEstimate > out[j].TokenEstimate
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Render builds the legacy SDK-shaped view from fragments.
func Render(frags []ContextFrag) AssembledContext {
	var out AssembledContext
	var previousSystemRender RenderPolicy
	hasRenderedSystem := false
	for _, frag := range frags {
		switch frag.Slot {
		case SlotSystem:
			for _, part := range frag.Parts {
				if part.Type != PartText || RenderText(part.Text, frag.Render) == "" {
					continue
				}
				if hasRenderedSystem {
					out.System += RenderSeparator(previousSystemRender, frag.Render)
				}
				out.System += RenderText(part.Text, frag.Render)
				previousSystemRender = frag.Render
				hasRenderedSystem = true
			}
		case SlotCurrentUser:
			for _, part := range frag.Parts {
				switch part.Type {
				case PartText:
					if strings.TrimSpace(part.Text) != "" {
						out.Query = strings.TrimSpace(part.Text)
					}
				case PartImage:
					if image := partSDKImage(part); image != nil && strings.TrimSpace(image.Image) != "" {
						out.InlineImages = append(out.InlineImages, *image)
					}
				case PartSDKMessage:
					if msg := partMessage(part); msg != nil {
						out.Messages = append(out.Messages, cloneMessage(*msg))
					}
				}
			}
		default:
			for _, part := range frag.Parts {
				if msg := partMessage(part); part.Type == PartSDKMessage && msg != nil {
					out.Messages = append(out.Messages, cloneMessage(*msg))
				}
			}
		}
	}
	return out
}

func cloneMessage(msg sdk.Message) sdk.Message {
	out := msg
	if len(msg.Content) > 0 {
		out.Content = append([]sdk.MessagePart(nil), msg.Content...)
	}
	return out
}

func partMessage(part Part) *sdk.Message {
	if part.SDKMessage != nil {
		return part.SDKMessage
	}
	return part.Message
}

func partSDKImage(part Part) *sdk.ImagePart {
	if part.SDKImage != nil {
		return part.SDKImage
	}
	return part.ImagePart
}

func messageTextBytes(msg *sdk.Message) int {
	if msg == nil {
		return 0
	}
	total := 0
	for _, part := range msg.Content {
		switch p := part.(type) {
		case sdk.TextPart:
			total += len(p.Text)
		case sdk.ReasoningPart:
			total += len(p.Text)
		}
	}
	return total
}

func messageImageCount(msg *sdk.Message) int {
	if msg == nil {
		return 0
	}
	total := 0
	for _, part := range msg.Content {
		if _, ok := part.(sdk.ImagePart); ok {
			total++
		}
	}
	return total
}

func renderTargetForSlot(slot Slot) string {
	switch slot {
	case SlotSystem:
		return "system"
	case SlotCurrentUser:
		return "query_inline_images"
	default:
		return "messages"
	}
}

func renderedOutputRefs(frags []ContextFrag) []RenderedOutputRef {
	lastCurrentText := lastCurrentUserTextIndex(frags)
	out := make([]RenderedOutputRef, 0, len(frags))
	for i, frag := range frags {
		if !fragContributesToRender(i, frag, lastCurrentText) {
			continue
		}
		ref := frag.Ref
		if err := ValidateContextRef(ref); err != nil {
			ref = WithContextRef(frag, ref).Ref
		}
		out = append(out, RenderedOutputRef{
			Target: renderTargetForSlot(frag.Slot),
			Slot:   frag.Slot,
			Refs:   []ContextRef{ref},
		})
	}
	return out
}

func lastCurrentUserTextIndex(frags []ContextFrag) int {
	last := -1
	for i, frag := range frags {
		if frag.Slot != SlotCurrentUser {
			continue
		}
		for _, part := range frag.Parts {
			if part.Type == PartText && strings.TrimSpace(part.Text) != "" {
				last = i
			}
		}
	}
	return last
}

func fragContributesToRender(index int, frag ContextFrag, lastCurrentText int) bool {
	switch frag.Slot {
	case SlotSystem:
		for _, part := range frag.Parts {
			if part.Type == PartText && strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	case SlotCurrentUser:
		for _, part := range frag.Parts {
			switch part.Type {
			case PartText:
				if index == lastCurrentText && strings.TrimSpace(part.Text) != "" {
					return true
				}
			case PartImage:
				if image := partSDKImage(part); image != nil && strings.TrimSpace(image.Image) != "" {
					return true
				}
			case PartSDKMessage:
				if partMessage(part) != nil {
					return true
				}
			}
		}
	default:
		for _, part := range frag.Parts {
			if part.Type == PartSDKMessage && partMessage(part) != nil {
				return true
			}
		}
	}
	return false
}
