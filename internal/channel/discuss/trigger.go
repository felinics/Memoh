package discuss

import (
	"strings"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

type discussTriggerBuilder struct{}

type discussTurnPlan struct {
	command         turn.StartTurnCommand
	consumed        timeline.DiscussCursorPosition
	messageCount    int
	estimatedTokens int
}

// Build composes the durable timeline and persisted turn responses into the
// pure StartTurn command consumed by Agent. The admission budget is applied
// before materialization (CM-ADM-001); a ProtectedOverflow admission returns
// ok=false so the caller fails closed instead of running the turn.
func (discussTriggerBuilder) Build(cfg DiscussSessionConfig, rc timeline.RenderedContext, trs []timeline.TurnResponseEntry, after timeline.DiscussCursorPosition, artifacts []timeline.CompactionArtifact, budget timeline.ComposeBudget) (discussTurnPlan, timeline.ComposeAdmission, bool) {
	composed, admission := timeline.ComposeContextWithArtifactsBudgeted(rc, trs, artifacts, budget)
	if composed == nil {
		return discussTurnPlan{}, admission, false
	}

	isMentioned := wasRecentlyMentioned(rc, after)
	addressed := isMentioned || turn.IsPrivateConversationType(cfg.ConversationType)
	msgs := make([]turn.DiscussMessage, 0, len(composed.Messages))
	for _, message := range composed.Messages {
		msgs = append(msgs, turn.DiscussMessage{
			Role:                 message.Role,
			Content:              message.Content,
			RawContent:           message.RawContent,
			CompactionArtifactID: message.CompactionArtifactID,
		})
	}
	imageRefs := make([]turn.DiscussImageRef, 0)
	for _, ref := range extractNewImageRefs(timeline.ActiveRenderedContext(rc, artifacts), after) {
		imageRefs = append(imageRefs, turn.DiscussImageRef{
			ContentHash: ref.ContentHash,
			Mime:        ref.Mime,
		})
	}

	return discussTurnPlan{
		command: turn.StartTurnCommand{
			SchemaVersion:           1,
			TeamID:                  cfg.TeamID,
			Mode:                    turn.ModeDiscuss,
			BotID:                   cfg.BotID,
			ThreadID:                cfg.ThreadID,
			RouteID:                 cfg.RouteID,
			SourceChannelIdentityID: cfg.ChannelIdentityID,
			CurrentChannel:          cfg.CurrentPlatform,
			ReplyTarget:             cfg.ReplyTarget,
			ConversationType:        cfg.ConversationType,
			ConversationName:        cfg.ConversationName,
			SessionToken:            cfg.SessionToken,
			ChatToken:               cfg.ChatToken,
			ToolHTTPURL:             cfg.ToolHTTPURL,
			DiscussMessages:         msgs,
			DiscussImageRefs:        imageRefs,
			DiscussAddressed:        addressed,
		},
		consumed:        timeline.ConsumedDiscussCursor(rc),
		messageCount:    len(composed.Messages),
		estimatedTokens: composed.EstimatedTokens,
	}, admission, true
}

// extractNewImageRefs collects image references from external RC segments
// that arrived after the last consumed cursor.
func extractNewImageRefs(rc timeline.RenderedContext, after timeline.DiscussCursorPosition) []timeline.ImageAttachmentRef {
	var refs []timeline.ImageAttachmentRef
	for _, segment := range rc {
		if !after.Covers(segment) && !segment.IsMyself && !segment.IsSelfSent {
			refs = append(refs, segment.ImageRefs...)
		}
	}
	return refs
}

func wasRecentlyMentioned(rc timeline.RenderedContext, after timeline.DiscussCursorPosition) bool {
	for _, segment := range rc {
		if segment.IsMyself || segment.IsSelfSent {
			continue
		}
		if !after.Covers(segment) && (segment.MentionsMe || segment.RepliesToMe) {
			return true
		}
	}
	return false
}

func normalizedRuntimeType(runtimeType string) string {
	return strings.TrimSpace(runtimeType)
}
