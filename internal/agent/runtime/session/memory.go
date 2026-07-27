package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	chatview "github.com/memohai/memoh/internal/agent/view"
)

// subscriberSet tracks per-key subscriber channels. All methods must be called
// with the owning backend's mutex held.
type subscriberSet[T any] struct {
	nextID int
	subs   map[string]map[int]*subscriberEntry[T]
}

type subscriberEntry[T any] struct {
	ch               chan T
	stopContextWatch func() bool
}

func newSubscriberSet[T any]() *subscriberSet[T] {
	return &subscriberSet[T]{subs: make(map[string]map[int]*subscriberEntry[T])}
}

func (s *subscriberSet[T]) register(key string) (int, chan T) {
	s.nextID++
	id := s.nextID
	ch := make(chan T, 64)
	if s.subs[key] == nil {
		s.subs[key] = make(map[int]*subscriberEntry[T])
	}
	s.subs[key][id] = &subscriberEntry[T]{ch: ch}
	return id, ch
}

func (s *subscriberSet[T]) setContextWatch(key string, id int, stop func() bool) bool {
	entry := s.subs[key][id]
	if entry == nil {
		return false
	}
	entry.stopContextWatch = stop
	return true
}

func (s *subscriberSet[T]) unregister(key string, id int) {
	subs := s.subs[key]
	if subs == nil {
		return
	}
	if target := subs[id]; target != nil {
		delete(subs, id)
		close(target.ch)
	}
	if len(subs) == 0 {
		delete(s.subs, key)
	}
}

func (s *subscriberSet[T]) closeAll() {
	for key, subs := range s.subs {
		for id, entry := range subs {
			delete(subs, id)
			if entry.stopContextWatch != nil {
				entry.stopContextWatch()
			}
			close(entry.ch)
		}
		delete(s.subs, key)
	}
}

type MemoryBackend struct {
	mu                sync.Mutex
	stateTTL          time.Duration
	nextCleanup       time.Time
	snapshots         map[string]Snapshot
	snapshotExpiresAt map[string]time.Time
	subscribers       *subscriberSet[Event]
	closed            bool
	// generation is this process's liveness incarnation. It is minted once and
	// never changes, so it dies with the heap that holds the live state.
	generation string
}

func NewMemoryBackend() *MemoryBackend {
	return NewMemoryBackendWithTTL(24 * time.Hour)
}

func NewMemoryBackendWithTTL(stateTTL time.Duration) *MemoryBackend {
	if stateTTL <= 0 {
		stateTTL = 24 * time.Hour
	}
	return &MemoryBackend{
		stateTTL:          stateTTL,
		snapshots:         make(map[string]Snapshot),
		snapshotExpiresAt: make(map[string]time.Time),
		subscribers:       newSubscriberSet[Event](),
		generation:        uuid.NewString(),
	}
}

func (*MemoryBackend) Now(ctx context.Context) (time.Time, error) {
	if err := contextError(ctx); err != nil {
		return time.Time{}, err
	}
	return time.Now().UTC(), nil
}

func (b *MemoryBackend) Load(ctx context.Context, key Key) (Snapshot, bool, error) {
	if err := contextError(ctx); err != nil {
		return Snapshot{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return Snapshot{}, false, err
	}
	now := time.Now()
	b.purgeExpiredLocked(now)
	k := key.String()
	snapshot, ok := b.snapshots[k]
	if !ok {
		return Snapshot{}, false, nil
	}
	cloned, err := cloneSnapshot(snapshot)
	return cloned, true, err
}

func (b *MemoryBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	if update == nil {
		return Snapshot{}, false, errors.New("snapshot update is required")
	}
	if err := contextError(ctx); err != nil {
		return Snapshot{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return Snapshot{}, false, err
	}
	now := time.Now()
	b.purgeExpiredLocked(now)
	k := key.String()
	snapshot, ok := b.snapshots[k]
	// The callback receives a private clone and owns whatever it returns, so
	// only the stored copy needs re-isolation before handing `next` back.
	current, err := cloneSnapshot(snapshot)
	if err != nil {
		return Snapshot{}, false, err
	}
	next, changed, err := update(current, ok)
	if err != nil || !changed {
		return next, changed, err
	}
	stored, err := cloneSnapshot(next)
	if err != nil {
		return Snapshot{}, false, err
	}
	b.snapshots[k] = stored
	b.snapshotExpiresAt[k] = now.Add(b.stateTTL)
	b.scheduleCleanupLocked(b.snapshotExpiresAt[k])
	return next, true, nil
}

func (b *MemoryBackend) Publish(ctx context.Context, event Event) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	shared, err := cloneEvent(event)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	subs := b.subscribers.subs[Key{BotID: event.BotID, SessionID: event.SessionID}.String()]
	if len(subs) == 0 {
		return nil
	}
	// Consumers treat events as read-only, so one isolation clone shared by
	// every subscriber decouples them all from the publisher's copy.
	for _, entry := range subs {
		enqueueRuntimeEvent(entry.ch, shared)
	}
	return nil
}

func (b *MemoryBackend) Subscribe(ctx context.Context, key Key) (Subscription, error) {
	if err := contextError(ctx); err != nil {
		return Subscription{}, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		ch := make(chan Event)
		close(ch)
		return Subscription{C: ch, Close: func() {}}, nil
	}
	k := key.String()
	id, ch := b.subscribers.register(k)
	b.mu.Unlock()
	var once sync.Once
	unregister := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.subscribers.unregister(k, id)
		})
	}
	stopContextWatch := context.AfterFunc(ctx, unregister)
	b.mu.Lock()
	watchRegistered := b.subscribers.setContextWatch(k, id, stopContextWatch)
	b.mu.Unlock()
	if !watchRegistered {
		stopContextWatch()
	}
	return Subscription{
		C: ch,
		Close: func() {
			stopContextWatch()
			unregister()
		},
	}, nil
}

func (b *MemoryBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.subscribers.closeAll()
	return nil
}

func cloneSnapshot(snapshot Snapshot) (Snapshot, error) {
	var messages []chatview.UIMessage
	if snapshot.CurrentRunView != nil {
		messages = snapshot.CurrentRunView.Messages
		currentRun := *snapshot.CurrentRunView
		currentRun.Messages = nil
		snapshot.CurrentRunView = &currentRun
	}
	var out Snapshot
	if err := cloneJSON(snapshot, &out); err != nil {
		return Snapshot{}, err
	}
	if out.CurrentRunView != nil {
		clonedMessages, err := cloneUIMessages(messages)
		if err != nil {
			return Snapshot{}, err
		}
		out.CurrentRunView.Messages = clonedMessages
	}
	return out, nil
}

func cloneUIMessages(messages []chatview.UIMessage) ([]chatview.UIMessage, error) {
	if messages == nil {
		return nil, nil
	}
	out := make([]chatview.UIMessage, len(messages))
	for i := range messages {
		message := messages[i]
		if message.Running != nil {
			running := *message.Running
			message.Running = &running
		}
		var err error
		if message.Input, err = cloneJSONValue(message.Input); err != nil {
			return nil, err
		}
		if message.Output, err = cloneJSONValue(message.Output); err != nil {
			return nil, err
		}
		if len(message.Progress) > 0 {
			message.Progress = make([]any, len(messages[i].Progress))
			for j := range messages[i].Progress {
				message.Progress[j], err = cloneJSONValue(messages[i].Progress[j])
				if err != nil {
					return nil, err
				}
			}
		}
		if err := cloneJSON(messages[i].Attachments, &message.Attachments); err != nil {
			return nil, err
		}
		if messages[i].Approval != nil {
			approval := *messages[i].Approval
			message.Approval = &approval
		}
		if messages[i].UserInput != nil {
			var userInput chatview.UIUserInput
			if err := cloneJSON(messages[i].UserInput, &userInput); err != nil {
				return nil, err
			}
			message.UserInput = &userInput
		}
		if messages[i].Background != nil {
			background := *messages[i].Background
			message.Background = &background
		}
		out[i] = message
	}
	return out, nil
}

func cloneJSONValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	var out any
	if err := cloneJSON(value, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *MemoryBackend) purgeExpiredLocked(now time.Time) {
	if !b.nextCleanup.IsZero() && now.Before(b.nextCleanup) {
		return
	}
	nextCleanup := time.Time{}
	for key, expiresAt := range b.snapshotExpiresAt {
		if !now.Before(expiresAt) {
			if snapshot, ok := b.snapshots[key]; ok && snapshot.CurrentRunView != nil && isActiveRunStatus(snapshot.CurrentRunView.Status) {
				// Active local runs are owned by their in-process runControl and
				// live until they terminalize or the process exits.
				delete(b.snapshotExpiresAt, key)
				continue
			}
			delete(b.snapshots, key)
			delete(b.snapshotExpiresAt, key)
		} else if nextCleanup.IsZero() || expiresAt.Before(nextCleanup) {
			nextCleanup = expiresAt
		}
	}
	b.nextCleanup = nextCleanup
}

func (b *MemoryBackend) scheduleCleanupLocked(expiresAt time.Time) {
	if expiresAt.IsZero() {
		return
	}
	if b.nextCleanup.IsZero() || expiresAt.Before(b.nextCleanup) {
		b.nextCleanup = expiresAt
	}
}

func cloneEvent(event Event) (Event, error) {
	var out Event
	if err := cloneJSON(event, &out); err != nil {
		return Event{}, err
	}
	return out, nil
}

func cloneJSON(in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
