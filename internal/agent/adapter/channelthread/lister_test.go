package channelthread

import (
	"context"
	"testing"

	session "github.com/felinics/memoh/internal/chat/thread"
)

type fakeLister struct {
	threads []session.Thread
}

func (f fakeLister) ListByBot(context.Context, string) ([]session.Thread, error) {
	return f.threads, nil
}

type fakeEnricher struct{}

func (fakeEnricher) EnrichThreads(_ context.Context, _ string, threads []session.Thread) ([]session.Thread, error) {
	out := append([]session.Thread(nil), threads...)
	out[0].RouteConversationType = "group"
	out[0].RouteMetadata = map[string]any{"conversation_name": "Memoh"}
	return out, nil
}

func TestListerProjectsRouteFields(t *testing.T) {
	lister := NewLister(
		fakeLister{threads: []session.Thread{{ID: "thread-1", RouteID: "route-1"}}},
		fakeEnricher{},
	)
	got, err := lister.ListByBot(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("ListByBot() error = %v", err)
	}
	if len(got) != 1 || got[0].RouteConversationType != "group" {
		t.Fatalf("ListByBot() = %#v", got)
	}
	if got[0].RouteMetadata["conversation_name"] != "Memoh" {
		t.Fatalf("RouteMetadata = %#v", got[0].RouteMetadata)
	}
}
