// Package tabmark persists the task marks a session places on workspace
// browser tabs: a "deliverable" is the tab the user should open to receive the
// result of a task, a "handoff" is a tab the user must take over (a login, a
// captcha, a confirmation) before the agent can continue. Marks live in the
// session's metadata so they survive reloads and server restarts, and every
// mark is keyed by browser and tab identity so re-marking is idempotent.
package tabmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/chat/thread"
)

// Kind is what a mark means to the user.
type Kind string

const (
	// KindDeliverable marks the tab that carries the task's result.
	KindDeliverable Kind = "deliverable"
	// KindHandoff marks a tab the user must take over; it does not imply the
	// task is finished.
	KindHandoff Kind = "handoff"
)

// MetadataKey is the session metadata field the marks are stored under.
const MetadataKey = "gui_tab_marks"

// Mark is one persisted tab mark.
type Mark struct {
	Key       string `json:"key"`
	Kind      Kind   `json:"kind"`
	BrowserID string `json:"browser_id"`
	TabID     string `json:"tab_id"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	// SessionName is the task-organisation name the tab was opened with.
	SessionName string `json:"session_name,omitempty"`
	Note        string `json:"note,omitempty"`
	// BrowserInstance identifies the browser process the tab lived in
	// (browser id plus pid); a restarted browser or rebuilt workspace cannot
	// host the tab any more, so the mark reads as closed.
	BrowserInstance string    `json:"browser_instance,omitempty"`
	MarkedAt        time.Time `json:"marked_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// ClosedAt is set once the tab was seen closed; the mark stays visible
	// as expired instead of silently disappearing.
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// KeyFor names the mark of one tab.
func KeyFor(browserID, tabID string) string {
	return strings.TrimSpace(browserID) + "/" + strings.TrimSpace(tabID)
}

// ThreadStore is the slice of the thread service the marks need.
type ThreadStore interface {
	Get(ctx context.Context, sessionID string) (thread.Thread, error)
	UpdateMetadata(ctx context.Context, sessionID string, metadata map[string]any) (thread.Thread, error)
}

// Service reads and writes marks through the session metadata.
type Service struct {
	threads ThreadStore
	now     func() time.Time
	// locks serialises read-modify-write per session inside this process.
	locks sync.Map
}

// NewService wires the marks to the thread store.
func NewService(threads ThreadStore) *Service {
	return &Service{threads: threads, now: time.Now}
}

// ErrNotConfigured is returned when no thread store is available.
var ErrNotConfigured = errors.New("tab marks are not configured")

func (s *Service) lock(sessionID string) func() {
	v, _ := s.locks.LoadOrStore(sessionID, &sync.Mutex{})
	mu, _ := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// List returns the marks of a session, oldest first.
func (s *Service) List(ctx context.Context, sessionID string) ([]Mark, error) {
	if s == nil || s.threads == nil {
		return nil, ErrNotConfigured
	}
	th, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return decode(th.Metadata), nil
}

// Put creates or updates the mark of mark.BrowserID/mark.TabID. Kind, URL,
// title, session name, and note are taken from the argument; MarkedAt is
// kept from the existing mark, so re-marking the same tab is idempotent and
// switching between deliverable and handoff is an explicit replacement.
func (s *Service) Put(ctx context.Context, sessionID string, mark Mark) (Mark, error) {
	if s == nil || s.threads == nil {
		return Mark{}, ErrNotConfigured
	}
	if mark.Kind != KindDeliverable && mark.Kind != KindHandoff {
		return Mark{}, fmt.Errorf("unknown tab mark kind %q", mark.Kind)
	}
	if strings.TrimSpace(mark.BrowserID) == "" || strings.TrimSpace(mark.TabID) == "" {
		return Mark{}, errors.New("a tab mark needs a browser_id and a tab_id")
	}
	unlock := s.lock(sessionID)
	defer unlock()
	th, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return Mark{}, err
	}
	marks := decode(th.Metadata)
	now := s.now()
	mark.Key = KeyFor(mark.BrowserID, mark.TabID)
	mark.UpdatedAt = now
	mark.MarkedAt = now
	mark.ClosedAt = nil
	replaced := false
	for i := range marks {
		if marks[i].Key == mark.Key {
			mark.MarkedAt = marks[i].MarkedAt
			marks[i] = mark
			replaced = true
			break
		}
	}
	if !replaced {
		marks = append(marks, mark)
	}
	if err := s.save(ctx, sessionID, th.Metadata, marks); err != nil {
		return Mark{}, err
	}
	return mark, nil
}

// Delete removes a mark and reports whether it existed.
func (s *Service) Delete(ctx context.Context, sessionID, key string) (bool, error) {
	if s == nil || s.threads == nil {
		return false, ErrNotConfigured
	}
	unlock := s.lock(sessionID)
	defer unlock()
	th, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return false, err
	}
	marks := decode(th.Metadata)
	kept := marks[:0]
	found := false
	for _, m := range marks {
		if m.Key == key {
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return false, nil
	}
	return true, s.save(ctx, sessionID, th.Metadata, kept)
}

// MarkClosed records that the marked tab no longer exists. It is a no-op for
// unknown keys and for marks already closed.
func (s *Service) MarkClosed(ctx context.Context, sessionID, key string) (Mark, bool, error) {
	if s == nil || s.threads == nil {
		return Mark{}, false, ErrNotConfigured
	}
	unlock := s.lock(sessionID)
	defer unlock()
	th, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return Mark{}, false, err
	}
	marks := decode(th.Metadata)
	for i := range marks {
		if marks[i].Key != key {
			continue
		}
		if marks[i].ClosedAt != nil {
			return marks[i], false, nil
		}
		now := s.now()
		marks[i].ClosedAt = &now
		marks[i].UpdatedAt = now
		if err := s.save(ctx, sessionID, th.Metadata, marks); err != nil {
			return Mark{}, false, err
		}
		return marks[i], true, nil
	}
	return Mark{}, false, nil
}

func (s *Service) save(ctx context.Context, sessionID string, metadata map[string]any, marks []Mark) error {
	next := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		next[k] = v
	}
	if len(marks) == 0 {
		delete(next, MetadataKey)
	} else {
		sort.SliceStable(marks, func(i, j int) bool { return marks[i].MarkedAt.Before(marks[j].MarkedAt) })
		raw, err := json.Marshal(marks)
		if err != nil {
			return err
		}
		var generic []any
		if err := json.Unmarshal(raw, &generic); err != nil {
			return err
		}
		next[MetadataKey] = generic
	}
	_, err := s.threads.UpdateMetadata(ctx, sessionID, next)
	return err
}

// decode reads the marks out of session metadata, tolerating a missing or
// malformed field (which yields no marks rather than an error).
func decode(metadata map[string]any) []Mark {
	raw, ok := metadata[MetadataKey]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var marks []Mark
	if err := json.Unmarshal(data, &marks); err != nil {
		return nil
	}
	out := marks[:0]
	for _, m := range marks {
		if m.Key == "" {
			m.Key = KeyFor(m.BrowserID, m.TabID)
		}
		if strings.TrimSpace(m.TabID) == "" {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MarkedAt.Before(out[j].MarkedAt) })
	return out
}
