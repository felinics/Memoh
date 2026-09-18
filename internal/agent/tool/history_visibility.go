package tools

import (
	"context"
	"strings"

	session "github.com/felinics/memoh/internal/chat/thread"
)

// visibleHistorySessions resolves the history scope of one tool invocation.
// Memory facts are bot-shared, but the transcripts that produced them are not:
// External routes stay within that route; route-less callers use their owner.
func visibleHistorySessions(ctx context.Context, lister SessionLister, sess SessionContext) ([]session.Thread, map[string]struct{}, error) {
	allowed := make(map[string]struct{})
	currentSessionID := strings.TrimSpace(sess.SessionID)
	if lister == nil {
		if currentSessionID != "" {
			allowed[currentSessionID] = struct{}{}
		}
		return nil, allowed, nil
	}

	threads, err := lister.ListByBot(ctx, strings.TrimSpace(sess.BotID))
	if err != nil {
		return nil, allowed, err
	}

	currentRouteID := ""
	currentUserID := strings.TrimSpace(sess.UserID)
	currentFound := false
	for _, thread := range threads {
		if currentSessionID != "" && strings.TrimSpace(thread.ID) == currentSessionID {
			currentFound = true
			currentRouteID = strings.TrimSpace(thread.RouteID)
			if persistedUserID := strings.TrimSpace(thread.CreatedByUserID); persistedUserID != "" {
				currentUserID = persistedUserID
			}
			break
		}
	}
	if !currentFound {
		return nil, allowed, nil
	}
	visible := make([]session.Thread, 0, len(threads))
	for _, thread := range threads {
		threadID := strings.TrimSpace(thread.ID)
		if threadID == "" {
			continue
		}
		sameSession := threadID == currentSessionID
		sameRoute := currentRouteID != "" && strings.TrimSpace(thread.RouteID) == currentRouteID
		sameUser := currentRouteID == "" && currentUserID != "" && strings.TrimSpace(thread.CreatedByUserID) == currentUserID
		if !sameSession && (thread.Visibility == session.VisibilityInternal || (!sameRoute && !sameUser)) {
			continue
		}
		visible = append(visible, thread)
		allowed[threadID] = struct{}{}
	}
	return visible, allowed, nil
}

func historySessionVisible(allowed map[string]struct{}, sessionID string) bool {
	_, ok := allowed[strings.TrimSpace(sessionID)]
	return ok
}
