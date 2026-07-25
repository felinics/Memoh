package discuss

import (
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
)

type discussTriggerBuilder struct{}

type discussTurnPlan struct {
	command         agentdomain.StartTurnCommand
	consumed        timeline.DiscussCursorPosition
	messageCount    int
	estimatedTokens int
}

// Build composes the durable timeline and persisted turn responses into the
// pure StartTurn command consumed by Agent.
func (discussTriggerBuilder) Build(cfg DiscussSessionConfig, rc timeline.RenderedContext, trs []timeline.TurnResponseEntry, after timeline.DiscussCursorPosition, artifacts []timeline.CompactionArtifact) (discussTurnPlan, bool) {
	composed := timeline.ComposeContextWithArtifacts(rc, trs, artifacts)
	if composed == nil {
		return discussTurnPlan{}, false
	}

	isMentioned := wasRecentlyMentioned(rc, after)
	addressed := isMentioned || agentdomain.IsPrivateConversationType(cfg.ConversationType)
	msgs := make([]agentdomain.DiscussMessage, 0, len(composed.Messages))
	for _, message := range composed.Messages {
		msgs = append(msgs, agentdomain.DiscussMessage{
			Role:                 message.Role,
			Content:              message.Content,
			RawContent:           message.RawContent,
			CompactionArtifactID: message.CompactionArtifactID,
		})
	}
	imageRefs := make([]agentdomain.DiscussImageRef, 0)
	for _, ref := range extractNewImageRefs(timeline.ActiveRenderedContext(rc, artifacts), after) {
		imageRefs = append(imageRefs, agentdomain.DiscussImageRef{
			ContentHash: ref.ContentHash,
			Mime:        ref.Mime,
		})
	}

	return discussTurnPlan{
		command: agentdomain.StartTurnCommand{
			SchemaVersion:           1,
			TeamID:                  cfg.TeamID,
			Mode:                    agentdomain.ModeDiscuss,
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
	}, true
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
