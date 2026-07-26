package projection

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/agent/chat/message"
	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	"github.com/memohai/memoh/domains/channel"
)

type observedRouteReaderFake struct {
	query message.ObservedRouteQuery
	items []message.ObservedRoute
}

func (f *observedRouteReaderFake) ListObservedRoutes(_ context.Context, query message.ObservedRouteQuery) ([]message.ObservedRoute, error) {
	f.query = query
	return append([]message.ObservedRoute(nil), f.items...), nil
}

type conversationProjectionReaderFake struct {
	request  channel.ConversationProjectionRequest
	requests []channel.ConversationProjectionRequest
	items    []channel.ConversationProjection
	calls    int
}

func (f *conversationProjectionReaderFake) ListConversationProjections(_ context.Context, request channel.ConversationProjectionRequest) ([]channel.ConversationProjection, error) {
	f.calls++
	f.request = request
	f.requests = append(f.requests, request)
	return append([]channel.ConversationProjection(nil), f.items...), nil
}

func TestObservedConversationReaderComposesOwnerProjections(t *testing.T) {
	older := time.Date(2026, 7, 24, 1, 0, 0, 0, time.FixedZone("test", 9*60*60))
	newer := older.Add(time.Hour)
	observations := &observedRouteReaderFake{items: []message.ObservedRoute{
		{RouteID: "route-old", LastObservedAt: older},
		{RouteID: "route-missing", LastObservedAt: newer.Add(time.Hour)},
		{RouteID: "route-new", LastObservedAt: newer},
	}}
	conversations := &conversationProjectionReaderFake{items: []channel.ConversationProjection{
		{RouteID: "route-new", Channel: "slack", ConversationType: "group", ConversationID: "C2", ConversationName: "New"},
		{RouteID: "route-old", Channel: "slack", ConversationType: "thread", ConversationID: "C1", ThreadID: "T1"},
	}}
	reader := NewObservedConversationReader(observations, conversations)

	items, err := reader.ListObservedConversations(t.Context(), aclpersistence.ObservedConversationQuery{
		BotID: " bot ", ChannelIdentityID: " identity ",
	})
	if err != nil {
		t.Fatalf("ListObservedConversations() error = %v", err)
	}
	if observations.query != (message.ObservedRouteQuery{BotID: "bot", ChannelIdentityID: "identity"}) {
		t.Fatalf("observation query = %#v", observations.query)
	}
	if conversations.request.BotID != "bot" || conversations.request.ChannelType != "" ||
		!reflect.DeepEqual(conversations.request.RouteIDs, []string{"route-missing", "route-new", "route-old"}) {
		t.Fatalf("projection request = %#v", conversations.request)
	}
	if len(items) != 2 || items[0].RouteID != "route-new" || items[1].RouteID != "route-old" {
		t.Fatalf("items = %#v", items)
	}
	if items[0].LastObservedAt.Location() != time.UTC || !items[0].LastObservedAt.Equal(newer) {
		t.Fatalf("last observed at = %v", items[0].LastObservedAt)
	}
}

func TestObservedConversationReaderForwardsChannelTypeToChannelOwner(t *testing.T) {
	observations := &observedRouteReaderFake{items: []message.ObservedRoute{{RouteID: "route"}}}
	conversations := &conversationProjectionReaderFake{}
	reader := NewObservedConversationReader(observations, conversations)

	_, err := reader.ListObservedConversations(t.Context(), aclpersistence.ObservedConversationQuery{
		BotID: "bot", ChannelType: " slack ",
	})
	if err != nil {
		t.Fatalf("ListObservedConversations() error = %v", err)
	}
	if observations.query.ChannelIdentityID != "" || conversations.request.ChannelType != "slack" {
		t.Fatalf("queries = observation %#v, projection %#v", observations.query, conversations.request)
	}
}

func TestObservedConversationReaderSkipsChannelForEmptyObservationSet(t *testing.T) {
	conversations := &conversationProjectionReaderFake{}
	reader := NewObservedConversationReader(&observedRouteReaderFake{}, conversations)

	items, err := reader.ListObservedConversations(t.Context(), aclpersistence.ObservedConversationQuery{
		BotID: "bot", ChannelType: "slack",
	})
	if err != nil || len(items) != 0 || conversations.calls != 0 {
		t.Fatalf("ListObservedConversations() = (%#v, %v), channel calls = %d", items, err, conversations.calls)
	}
}

func TestObservedConversationReaderBatchesChannelProjectionRequests(t *testing.T) {
	items := make([]message.ObservedRoute, conversationProjectionBatchSize+1)
	for i := range items {
		items[i].RouteID = "route"
	}
	conversations := &conversationProjectionReaderFake{}
	reader := NewObservedConversationReader(&observedRouteReaderFake{items: items}, conversations)

	_, err := reader.ListObservedConversations(t.Context(), aclpersistence.ObservedConversationQuery{
		BotID: "bot", ChannelType: "slack",
	})
	if err != nil {
		t.Fatalf("ListObservedConversations() error = %v", err)
	}
	if conversations.calls != 2 || len(conversations.requests[0].RouteIDs) != conversationProjectionBatchSize ||
		len(conversations.requests[1].RouteIDs) != 1 {
		t.Fatalf("projection batches = %#v", conversations.requests)
	}
}
