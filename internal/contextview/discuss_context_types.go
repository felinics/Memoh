package contextview

import (
	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// DiscussContextInput carries the authoritative timeline composition and
// already-typed system fragments for one discuss turn.
type DiscussContextInput struct {
	ComposedMessages []timeline.ContextMessage
	InlineImages     []sdk.ImagePart
	SystemFrags      []contextfrag.ContextFrag
}
