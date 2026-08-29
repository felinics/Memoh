// Package channelcontact adapts Channel routes to Agent messaging contacts.
package channelcontact

import (
	"context"

	"github.com/felinics/memoh/internal/channel/route"
	"github.com/felinics/memoh/internal/messaging"
)

type RouteLister interface {
	List(ctx context.Context, botID string) ([]route.Route, error)
}

type Source struct {
	routes RouteLister
}

func NewSource(routes RouteLister) *Source {
	return &Source{routes: routes}
}

func (s *Source) ListContacts(ctx context.Context, botID string) ([]messaging.Contact, error) {
	routes, err := s.routes.List(ctx, botID)
	if err != nil {
		return nil, err
	}
	contacts := make([]messaging.Contact, 0, len(routes))
	for _, item := range routes {
		contacts = append(contacts, messaging.Contact{
			RouteID:                item.ID,
			Platform:               item.Platform,
			ConversationType:       item.ConversationType,
			ReplyTarget:            item.ReplyTarget,
			ExternalConversationID: item.ExternalConversationID,
			Metadata:               item.Metadata,
			UpdatedAt:              item.UpdatedAt,
		})
	}
	return contacts, nil
}
