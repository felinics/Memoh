// Package cdpsession keeps the revocable CDP sessions the browser_remote_session
// tool hands out. A session is a capability: whoever holds its id may reach one
// page target of one workspace browser through the server-side proxy until the
// session is closed or idles out. Revoking a session drops every connection the
// proxy opened for it, which is what makes "close" more than deleting a row.
package cdpsession

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// DefaultIdleTTL is how long an unused session stays valid.
const DefaultIdleTTL = 30 * time.Minute

// Session is one issued CDP capability.
type Session struct {
	// ID is the secret the proxy path carries; it is never derived from the
	// target so it can be revoked independently of the tab.
	ID        string
	BotID     string
	ThreadID  string
	BrowserID string
	// Port is the CDP port of the browser inside the workspace.
	Port int
	// TabID is the only target the session may attach to.
	TabID string
	// CreatedTab records whether create opened the tab for this session,
	// which is what close_tab defaults to acting on.
	CreatedTab bool
	CreatedAt  time.Time
	LastUsed   time.Time
	ExpiresAt  time.Time
}

type entry struct {
	session Session
	conns   map[io.Closer]struct{}
}

// Store is the in-memory session registry shared by the tool that issues
// sessions and the HTTP proxy that serves them.
type Store struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]*entry
	now      func() time.Time
}

// New returns a store whose sessions expire after ttl of inactivity.
func New(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultIdleTTL
	}
	return &Store{ttl: ttl, sessions: make(map[string]*entry), now: time.Now}
}

// ErrNotFound is returned for unknown, expired, or revoked sessions.
var ErrNotFound = errors.New("cdp session not found")

func newID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

// Create registers a session and mints its id.
func (s *Store) Create(session Session) (Session, error) {
	if s == nil {
		return Session{}, errors.New("cdp session store is not configured")
	}
	if strings.TrimSpace(session.BotID) == "" || strings.TrimSpace(session.TabID) == "" || session.Port <= 0 {
		return Session{}, errors.New("cdp session needs a bot, a tab, and a port")
	}
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	session.ID = id
	session.CreatedAt = now
	session.LastUsed = now
	session.ExpiresAt = now.Add(s.ttl)
	s.mu.Lock()
	s.pruneLocked(now)
	s.sessions[id] = &entry{session: session, conns: make(map[io.Closer]struct{})}
	s.mu.Unlock()
	return session, nil
}

// Get returns the session and extends its idle deadline. botID, when set,
// must match: a session id is only meaningful under the bot it was issued for.
func (s *Store) Get(id, botID string) (Session, bool) {
	if s == nil {
		return Session{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	e, ok := s.sessions[id]
	if !ok || (botID != "" && e.session.BotID != botID) {
		return Session{}, false
	}
	e.session.LastUsed = now
	e.session.ExpiresAt = now.Add(s.ttl)
	return e.session, true
}

// ListForBot returns the live sessions of one bot, oldest first.
func (s *Store) ListForBot(botID string) []Session {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	out := make([]Session, 0)
	for _, e := range s.sessions {
		if e.session.BotID == botID {
			out = append(out, e.session)
		}
	}
	sortSessions(out)
	return out
}

// Connections reports how many proxied connections a session currently has.
func (s *Store) Connections(id string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[strings.TrimSpace(id)]
	if !ok {
		return 0
	}
	return len(e.conns)
}

// Attach registers a live proxied connection with the session so that
// revoking the session closes it. The returned release must be called when
// the connection ends on its own.
func (s *Store) Attach(id, botID string, conn io.Closer) (release func(), ok bool) {
	if s == nil || conn == nil {
		return func() {}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.sessions[strings.TrimSpace(id)]
	if !found || (botID != "" && e.session.BotID != botID) {
		return func() {}, false
	}
	e.conns[conn] = struct{}{}
	e.session.LastUsed = s.now()
	e.session.ExpiresAt = e.session.LastUsed.Add(s.ttl)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if current, still := s.sessions[e.session.ID]; still {
			delete(current.conns, conn)
		}
	}, true
}

// Revoke removes the session and closes every connection attached to it.
// It reports the session and how many connections were dropped.
func (s *Store) Revoke(id, botID string) (Session, int, bool) {
	if s == nil {
		return Session{}, 0, false
	}
	s.mu.Lock()
	e, ok := s.sessions[strings.TrimSpace(id)]
	if !ok || (botID != "" && e.session.BotID != botID) {
		s.mu.Unlock()
		return Session{}, 0, false
	}
	delete(s.sessions, e.session.ID)
	conns := make([]io.Closer, 0, len(e.conns))
	for c := range e.conns {
		conns = append(conns, c)
	}
	e.conns = make(map[io.Closer]struct{})
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return e.session, len(conns), true
}

// RevokeTab revokes every session bound to a tab, used when the tab is
// closed so status never reports a session for a target that is gone.
func (s *Store) RevokeTab(botID, tabID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	ids := make([]string, 0)
	for id, e := range s.sessions {
		if e.session.BotID == botID && e.session.TabID == tabID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.Revoke(id, botID)
	}
	return len(ids)
}

func (s *Store) pruneLocked(now time.Time) {
	for id, e := range s.sessions {
		if e.session.ExpiresAt.After(now) {
			continue
		}
		delete(s.sessions, id)
		for c := range e.conns {
			_ = c.Close()
		}
	}
}

func sortSessions(list []Session) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].CreatedAt.Before(list[j-1].CreatedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
