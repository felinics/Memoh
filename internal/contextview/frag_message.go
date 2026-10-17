package contextview

import (
	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func rebuildMessageFrag(frag contextfrag.ContextFrag, msg sdk.Message) contextfrag.ContextFrag {
	conflictKey := frag.ConflictKey
	frag = contextfrag.RebuildFragMessage(frag, msg)
	frag.ConflictKey = conflictKey
	frag.TokenEstimate = 0
	frag.Ref.HashAlgo = ""
	frag.Ref.HashScope = ""
	frag.Ref.ContentHash = ""
	return contextfrag.WithContextRef(frag, frag.Ref)
}

func isToolExchangeFrag(frag contextfrag.ContextFrag) bool {
	msg := contextfrag.FragMessage(frag)
	if msg == nil {
		return false
	}
	if msg.Role == sdk.MessageRoleTool {
		return true
	}
	for _, part := range msg.Content {
		switch part.(type) {
		case sdk.ToolCallPart, sdk.ToolResultPart:
			return true
		}
	}
	return false
}
