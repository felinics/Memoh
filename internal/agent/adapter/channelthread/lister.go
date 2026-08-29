package channelthread

import (
	"context"

	session "github.com/felinics/memoh/internal/chat/thread"
)

type threadLister interface {
	ListByBot(context.Context, string) ([]session.Thread, error)
}

type threadEnricher interface {
	EnrichThreads(context.Context, string, []session.Thread) ([]session.Thread, error)
}

// Lister adapts Thread persistence plus Channel route projection to the
// route-aware view consumed by Agent tools.
type Lister struct {
	threads  threadLister
	enricher threadEnricher
}

func NewLister(threads threadLister, enricher threadEnricher) *Lister {
	return &Lister{threads: threads, enricher: enricher}
}

func (l *Lister) ListByBot(ctx context.Context, botID string) ([]session.Thread, error) {
	threads, err := l.threads.ListByBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	if l.enricher == nil {
		return threads, nil
	}
	return l.enricher.EnrichThreads(ctx, botID, threads)
}
