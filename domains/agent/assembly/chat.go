package assembly

import (
	"log/slog"

	"github.com/memohai/memoh/domains/agent/chat/event"
	"github.com/memohai/memoh/domains/agent/chat/message"
	"github.com/memohai/memoh/domains/agent/chat/thread"
)

// NewMessageService constructs the public message service.
func NewMessageService(log *slog.Logger, store message.Persistence, publishers ...event.Publisher) *message.DBService {
	return message.NewService(log, store, publishers...)
}

// NewThreadService constructs the public thread service.
func NewThreadService(log *slog.Logger, store thread.Store, policyReader thread.ACPPolicyReader, publisher event.Publisher) *thread.Service {
	return thread.NewService(log, store, policyReader, publisher)
}
