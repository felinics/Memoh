package contextview

import (
	"reflect"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type StablePrefixPlacer struct{}

func (StablePrefixPlacer) Place(selected []contextfrag.ContextFrag, _ contextfrag.Intent) PlacementPlan {
	items := make([]PlacementItem, 0, len(selected))
	firstVolatile := len(selected)
	for i, frag := range selected {
		ref := placementRef(frag)
		items = append(items, PlacementItem{
			FragID:    frag.ID,
			Slot:      frag.Slot,
			Position:  i,
			CacheHint: frag.CacheClass,
			Ref:       ref,
		})
		if firstVolatile == len(selected) && frag.CacheClass != contextfrag.CacheStable {
			firstVolatile = i
		}
	}
	firstVolatile = renderedStablePrefixEnd(selected, firstVolatile)
	return PlacementPlan{
		StablePrefixHash:   stablePrefixHash(selected[:firstVolatile]),
		FirstVolatileIndex: firstVolatile,
		Items:              items,
	}
}

func renderedStablePrefixEnd(frags []contextfrag.ContextFrag, end int) int {
	if end <= 0 || end >= len(frags) {
		return end
	}
	stable := renderSDKPayloadFromFrags(frags[:end])
	full := renderSDKPayloadFromFrags(frags)
	if stable.System != "" && full.System != stable.System && !strings.HasPrefix(full.System, stable.System+"\n\n") {
		return 0
	}
	if len(stable.Messages) > len(full.Messages) {
		return 0
	}
	for i, message := range stable.Messages {
		if reflect.DeepEqual(message, full.Messages[i]) {
			continue
		}
		return fragmentIndexForRenderedMessage(frags[:end], i)
	}
	return end
}

func fragmentIndexForRenderedMessage(frags []contextfrag.ContextFrag, target int) int {
	messageIndex := 0
	for i, frag := range frags {
		if frag.Slot == contextfrag.SlotSystem {
			continue
		}
		for _, part := range frag.Parts {
			if part.Type != contextfrag.PartSDKMessage || sdkMessagePart(part) == nil {
				continue
			}
			if messageIndex == target {
				return i
			}
			messageIndex++
		}
	}
	return 0
}

func placementRef(frag contextfrag.ContextFrag) contextfrag.ContextRef {
	if err := contextfrag.ValidateContextRef(frag.Ref); err == nil {
		return frag.Ref
	}
	return contextfrag.WithContextRef(frag, frag.Ref).Ref
}

func stablePrefixHash(frags []contextfrag.ContextFrag) string {
	if len(frags) == 0 {
		return ""
	}
	hash, err := sdkRenderedPayloadHash(renderSDKPayloadFromFrags(frags))
	if err != nil {
		return ""
	}
	return hash
}
