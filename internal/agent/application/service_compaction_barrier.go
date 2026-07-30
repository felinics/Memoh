package application

import (
	"strings"
	"sync"
)

type sessionCompactionGate struct {
	lock    sync.RWMutex
	refs    int
	readers map[string]map[*sessionCompactionReader]struct{}
}

type sessionCompactionReader struct {
	gate   *sessionCompactionGate
	mu     sync.Mutex
	held   bool
	closed bool
}

func (s *Service) DeferSessionCompaction(botID, sessionID, runID string) func() {
	gate, key := s.acquireSessionCompactionGate(botID, sessionID)
	if gate == nil {
		return func() {}
	}
	gate.lock.RLock()
	reader := &sessionCompactionReader{gate: gate, held: true}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		s.sessionCompactionMu.Lock()
		if gate.readers == nil {
			gate.readers = make(map[string]map[*sessionCompactionReader]struct{})
		}
		if gate.readers[runID] == nil {
			gate.readers[runID] = make(map[*sessionCompactionReader]struct{})
		}
		gate.readers[runID][reader] = struct{}{}
		s.sessionCompactionMu.Unlock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			reader.close()
			if runID != "" {
				s.sessionCompactionMu.Lock()
				delete(gate.readers[runID], reader)
				if len(gate.readers[runID]) == 0 {
					delete(gate.readers, runID)
				}
				s.sessionCompactionMu.Unlock()
			}
			s.releaseSessionCompactionGate(key, gate)
		})
	}
}

func (reader *sessionCompactionReader) suspend() bool {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed || !reader.held {
		return false
	}
	reader.gate.lock.RUnlock()
	reader.held = false
	return true
}

func (reader *sessionCompactionReader) resume() {
	reader.gate.lock.RLock()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		reader.gate.lock.RUnlock()
		return
	}
	reader.held = true
}

func (reader *sessionCompactionReader) close() {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return
	}
	reader.closed = true
	if reader.held {
		reader.gate.lock.RUnlock()
		reader.held = false
	}
}

func (s *Service) enterSessionCompaction(botID, sessionID string) func() {
	gate, key := s.acquireSessionCompactionGate(botID, sessionID)
	if gate == nil {
		return func() {}
	}
	gate.lock.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.lock.Unlock()
			s.releaseSessionCompactionGate(key, gate)
		})
	}
}

func (s *Service) enterSessionCompactionForRun(botID, sessionID, runID string) func() {
	gate, key := s.acquireSessionCompactionGate(botID, sessionID)
	if gate == nil {
		return func() {}
	}
	readers := s.sessionCompactionReaders(gate, runID)
	suspended := make([]*sessionCompactionReader, 0, len(readers))
	for _, reader := range readers {
		if reader.suspend() {
			suspended = append(suspended, reader)
		}
	}
	gate.lock.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.lock.Unlock()
			for _, reader := range suspended {
				reader.resume()
			}
			s.releaseSessionCompactionGate(key, gate)
		})
	}
}

func (s *Service) sessionCompactionReaders(gate *sessionCompactionGate, runID string) []*sessionCompactionReader {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	s.sessionCompactionMu.Lock()
	defer s.sessionCompactionMu.Unlock()
	readers := make([]*sessionCompactionReader, 0, len(gate.readers[runID]))
	for reader := range gate.readers[runID] {
		readers = append(readers, reader)
	}
	return readers
}

// sessionTurnKey names the per-session scope of process-local bookkeeping that
// only makes sense inside one process: a compaction barrier and the ACP prompt
// hubs. It is deliberately not an admission gate — single-session execution is
// enforced durably by the session runtime, which is the only thing that can
// answer for owners in other processes.
func sessionTurnKey(botID, sessionID string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(sessionID)
}

func (s *Service) acquireSessionCompactionGate(botID, sessionID string) (*sessionCompactionGate, string) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || botID == "" || sessionID == "" {
		return nil, ""
	}
	key := sessionTurnKey(botID, sessionID)
	s.sessionCompactionMu.Lock()
	defer s.sessionCompactionMu.Unlock()
	if s.sessionCompactions == nil {
		s.sessionCompactions = make(map[string]*sessionCompactionGate)
	}
	gate := s.sessionCompactions[key]
	if gate == nil {
		gate = &sessionCompactionGate{}
		s.sessionCompactions[key] = gate
	}
	gate.refs++
	return gate, key
}

func (s *Service) releaseSessionCompactionGate(key string, gate *sessionCompactionGate) {
	s.sessionCompactionMu.Lock()
	defer s.sessionCompactionMu.Unlock()
	gate.refs--
	if gate.refs == 0 && s.sessionCompactions[key] == gate {
		delete(s.sessionCompactions, key)
	}
}
