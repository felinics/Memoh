package discuss

import (
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
)

type discussTriggerBuilder struct{}

type discussTurnPlan struct {
	command         agentdomain.StartTurnCommand
	consumedMs      int64
	messageCount    int
	estimatedTokens int
}

// Build composes the durable timeline and persisted turn responses into the
// pure StartTurn command consumed by Agent.
func (discussTriggerBuilder) Build(cfg DiscussSessionConfig, rc timeline.RenderedContext, trs []timeline.TurnResponseEntry, afterMs int64) (discussTurnPlan, bool) {
	composed := timeline.ComposeContext(rc, trs, "")
	if composed == nil {
		return discussTurnPlan{}, false
	}

	isMentioned := wasRecentlyMentioned(rc, afterMs)
	addressed := isMentioned || agentdomain.IsPrivateConversationType(cfg.ConversationType)
	msgs := make([]agentdomain.DiscussMessage, 0, len(composed.Messages))
	for _, message := range composed.Messages {
		msgs = append(msgs, agentdomain.DiscussMessage{
			Role:       message.Role,
			Content:    message.Content,
			RawContent: message.RawContent,
		})
	}
	imageRefs := make([]agentdomain.DiscussImageRef, 0)
	for _, ref := range extractNewImageRefs(rc, afterMs) {
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
			DiscussMentioned:        isMentioned,
			DiscussAddressed:        addressed,
		},
		consumedMs:      latestRCReceivedAtMs(rc),
		messageCount:    len(composed.Messages),
		estimatedTokens: composed.EstimatedTokens,
	}, true
}

// latestRCReceivedAtMs returns the maximum ReceivedAtMs across all segments
// in the given RC, or 0 if the RC is empty.
func latestRCReceivedAtMs(rc timeline.RenderedContext) int64 {
	var latest int64
	for _, segment := range rc {
		if segment.ReceivedAtMs > latest {
			latest = segment.ReceivedAtMs
		}
	}
	return latest
}

// extractNewImageRefs collects image references from external RC segments
// that arrived after the last consumed cursor.
func extractNewImageRefs(rc timeline.RenderedContext, afterMs int64) []timeline.ImageAttachmentRef {
	var refs []timeline.ImageAttachmentRef
	for _, segment := range rc {
		if segment.ReceivedAtMs > afterMs && !segment.IsMyself {
			refs = append(refs, segment.ImageRefs...)
		}
	}
	return refs
}

func wasRecentlyMentioned(rc timeline.RenderedContext, afterMs int64) bool {
	for _, segment := range rc {
		if segment.ReceivedAtMs > afterMs && (segment.MentionsMe || segment.RepliesToMe) {
			return true
		}
	}
	return false
}

func normalizedRuntimeType(runtimeType string) string {
	return strings.TrimSpace(runtimeType)
}
