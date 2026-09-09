package channelcontact

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel/route"
)

type fakeRouteLister struct {
	items []route.Route
}

func (f fakeRouteLister) List(context.Context, string) ([]route.Route, error) {
	return f.items, nil
}

func TestSourceProjectsRouteContact(t *testing.T) {
	t.Parallel()

	now := time.Now()
	source := NewSource(fakeRouteLister{items: []route.Route{{
		ID:                     "route-1",
		Platform:               "telegram",
		ConversationType:       "private",
		ReplyTarget:            "chat-1",
		ExternalConversationID: "chat-1",
		Metadata:               map[string]any{"conversation_name": "Alice"},
		UpdatedAt:              now,
	}}})
	got, err := source.ListContacts(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(got) != 1 || got[0].RouteID != "route-1" || got[0].Platform != "telegram" || !got[0].UpdatedAt.Equal(now) {
		t.Fatalf("unexpected contacts: %#v", got)
	}
}
