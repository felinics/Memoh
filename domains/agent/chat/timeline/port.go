package timeline

import "context"

// EventRecord is the persistence representation of one canonical timeline event.
type EventRecord struct {
	BotID                   string
	SessionID               string
	Kind                    EventKind
	Data                    []byte
	ExternalMessageID       string
	SenderChannelIdentityID string
	ReceivedAtMS            int64
}

// StoredEvent is the domain row needed to replay a timeline.
type StoredEvent struct {
	ID   string
	Kind EventKind
	Data []byte
}

// DiscussCursorRecord is the persisted discuss-consumption position.
type DiscussCursorRecord struct {
	BotID               string
	SessionID           string
	ScopeKey            string
	RouteID             string
	Source              string
	ConsumedCursor      int64
	ConsumedEventCursor int64
}

// Store is the consumer-owned persistence port used by EventStore.
type Store interface {
	NextEventCursor(context.Context) (int64, error)
	CreateEvent(context.Context, EventRecord) (string, error)
	ListEvents(context.Context, string) ([]StoredEvent, error)
	CountEvents(context.Context, string) (int64, error)
	GetDiscussCursor(context.Context, string, string) (DiscussCursorPosition, error)
	UpsertDiscussCursor(context.Context, DiscussCursorRecord) error
}
