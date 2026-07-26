// Package projection composes ACL observed-conversation candidates from
// Agent-owned observations and Channel-owned route metadata.
//
// This is a business join — it sorts, batches, and decides which fields the
// ACL contract receives — so it lives in a named package rather than an
// assembly seam, where readers would expect only trivial wiring.
package projection

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/memohai/memoh/domains/agent/chat/message"
	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	"github.com/memohai/memoh/domains/channel"
)

type observedConversationReader struct {
	observations  message.ObservedRouteReader
	conversations channel.ConversationProjectionReader
}

const conversationProjectionBatchSize = 1000

// NewObservedConversationReader composes Agent-owned observations with
// Channel-owned route metadata without either owner reading the other's schema.
func NewObservedConversationReader(
	observations message.ObservedRouteReader,
	conversations channel.ConversationProjectionReader,
) aclpersistence.ObservedConversationReader {
	return &observedConversationReader{
		observations:  observations,
		conversations: conversations,
	}
}

func (r *observedConversationReader) ListObservedConversations(
	ctx context.Context,
	query aclpersistence.ObservedConversationQuery,
) ([]aclpersistence.ObservedConversationCandidate, error) {
	if r == nil || r.observations == nil || r.conversations == nil {
		return nil, errors.New("observed conversation reader not configured")
	}

	query.BotID = strings.TrimSpace(query.BotID)
	query.ChannelIdentityID = strings.TrimSpace(query.ChannelIdentityID)
	query.ChannelType = strings.TrimSpace(query.ChannelType)
	if query.BotID == "" {
		return nil, errors.New("bot_id is required")
	}
	if (query.ChannelIdentityID == "") == (query.ChannelType == "") {
		return nil, errors.New("exactly one observed conversation filter is required")
	}

	observations, err := r.observations.ListObservedRoutes(ctx, message.ObservedRouteQuery{
		BotID:             query.BotID,
		ChannelIdentityID: query.ChannelIdentityID,
	})
	if err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return []aclpersistence.ObservedConversationCandidate{}, nil
	}

	slices.SortFunc(observations, func(a, b message.ObservedRoute) int {
		if byTime := b.LastObservedAt.Compare(a.LastObservedAt); byTime != 0 {
			return byTime
		}
		return cmp.Compare(a.RouteID, b.RouteID)
	})
	routeIDs := make([]string, 0, len(observations))
	for _, observation := range observations {
		routeIDs = append(routeIDs, observation.RouteID)
	}

	projections := make([]channel.ConversationProjection, 0, len(routeIDs))
	for start := 0; start < len(routeIDs); start += conversationProjectionBatchSize {
		end := min(start+conversationProjectionBatchSize, len(routeIDs))
		batch, err := r.conversations.ListConversationProjections(ctx, channel.ConversationProjectionRequest{
			BotID:       query.BotID,
			RouteIDs:    routeIDs[start:end],
			ChannelType: query.ChannelType,
		})
		if err != nil {
			return nil, err
		}
		projections = append(projections, batch...)
	}
	byRouteID := make(map[string]channel.ConversationProjection, len(projections))
	for _, projection := range projections {
		byRouteID[projection.RouteID] = projection
	}

	result := make([]aclpersistence.ObservedConversationCandidate, 0, len(projections))
	for _, observation := range observations {
		projection, ok := byRouteID[observation.RouteID]
		if !ok {
			continue
		}
		result = append(result, aclpersistence.ObservedConversationCandidate{
			RouteID:               projection.RouteID,
			Channel:               projection.Channel,
			ConversationType:      projection.ConversationType,
			ConversationID:        projection.ConversationID,
			ThreadID:              projection.ThreadID,
			ConversationName:      projection.ConversationName,
			ConversationAvatarURL: projection.ConversationAvatarURL,
			LastObservedAt:        observation.LastObservedAt.UTC(),
		})
	}
	return result, nil
}
