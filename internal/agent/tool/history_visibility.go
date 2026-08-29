package tools

import (
	"context"
	"strings"

	session "github.com/felinics/memoh/internal/chat/thread"
)

// visibleHistorySessions resolves the history scope of one tool invocation.
// Memory facts are bot-shared, but the transcripts that produced them are not:
// a caller may read its current session, another thread on the same external
// route, or another thread created by the same authenticated user.
func visibleHistorySessions(ctx context.Context, lister SessionLister, sess SessionContext) ([]session.Thread, map[string]struct{}, error) {
	allowed := make(map[string]struct{})
	currentSessionID := strings.TrimSpace(sess.SessionID)
	if currentSessionID != "" {
		allowed[currentSessionID] = struct{}{}
	}
	if lister == nil {
		return nil, allowed, nil
	}

	threads, err := lister.ListByBot(ctx, strings.TrimSpace(sess.BotID))
	if err != nil {
		return nil, allowed, err
	}

	currentRouteID := ""
	currentUserID := strings.TrimSpace(sess.UserID)
	for _, thread := range threads {
		if thread.ID == currentSessionID {
			currentRouteID = strings.TrimSpace(thread.RouteID)
			if persistedUserID := strings.TrimSpace(thread.CreatedByUserID); persistedUserID != "" {
				currentUserID = persistedUserID
			}
			break
		}
	}
	visible := make([]session.Thread, 0, len(threads))
	for _, thread := range threads {
		threadID := strings.TrimSpace(thread.ID)
		if threadID == "" {
			continue
		}
		sameSession := threadID == currentSessionID
		sameRoute := currentRouteID != "" && strings.TrimSpace(thread.RouteID) == currentRouteID
		sameUser := currentUserID != "" && strings.TrimSpace(thread.CreatedByUserID) == currentUserID
		if !sameSession && !sameRoute && !sameUser {
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
