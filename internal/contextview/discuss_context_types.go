package contextview

import (
	sdk "github.com/memohai/twilight/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/chat/timeline"
)

// DiscussContextInput carries the authoritative timeline composition and
// already-typed system fragments for one discuss turn.
type DiscussContextInput struct {
	ComposedMessages []timeline.ContextMessage
	InlineImages     []sdk.ImagePart
	SystemFrags      []contextfrag.ContextFrag
}
