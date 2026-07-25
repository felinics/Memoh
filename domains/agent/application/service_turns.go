package application

import (
	"context"
	"strings"
	"sync"
)

func sessionTurnKey(botID, sessionID string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(sessionID)
}

func (s *Service) enterSessionTurn(_ context.Context, botID, sessionID string) func() {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return func() {}
	}

	key := sessionTurnKey(botID, sessionID)
	s.sessionTurnMu.Lock()
	lock := s.sessionTurnLockLocked(key)
	s.sessionTurnRefs[key]++
	s.sessionTurnMu.Unlock()

	lock.Lock()
	return s.makeSessionTurnReleaser(key, lock)
}

func (s *Service) tryEnterIdleSessionTurn(_ context.Context, botID, sessionID string) (func(), bool) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return func() {}, true
	}

	key := sessionTurnKey(botID, sessionID)
	s.sessionTurnMu.Lock()
	lock := s.sessionTurnLockLocked(key)
	if s.sessionTurnRefs[key] > 0 {
		s.sessionTurnMu.Unlock()
		return func() {}, false
	}
	s.sessionTurnRefs[key] = 1
	lock.Lock()
	s.sessionTurnMu.Unlock()
	return s.makeSessionTurnReleaser(key, lock), true
}

func (s *Service) SessionTurnActive(botID, sessionID string) bool {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || botID == "" || sessionID == "" {
		return false
	}
	key := sessionTurnKey(botID, sessionID)
	s.sessionTurnMu.Lock()
	defer s.sessionTurnMu.Unlock()
	return s.sessionTurnRefs[key] > 0
}

func (s *Service) sessionTurnLockLocked(key string) *sync.Mutex {
	if s.sessionTurnRefs == nil {
		s.sessionTurnRefs = make(map[string]int)
	}
	if s.sessionTurnLocks == nil {
		s.sessionTurnLocks = make(map[string]*sync.Mutex)
	}
	lock := s.sessionTurnLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.sessionTurnLocks[key] = lock
	}
	return lock
}

func (s *Service) makeSessionTurnReleaser(key string, lock *sync.Mutex) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.sessionTurnMu.Lock()
			switch refs := s.sessionTurnRefs[key] - 1; {
			case refs > 0:
				s.sessionTurnRefs[key] = refs
			default:
				delete(s.sessionTurnRefs, key)
			}
			s.sessionTurnMu.Unlock()
			lock.Unlock()
		})
	}
}
