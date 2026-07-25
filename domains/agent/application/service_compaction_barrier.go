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

func (s *Service) DeferSessionCompaction(botID, sessionID, streamID string) func() {
	gate, key := s.acquireSessionCompactionGate(botID, sessionID)
	if gate == nil {
		return func() {}
	}
	gate.lock.RLock()
	reader := &sessionCompactionReader{gate: gate, held: true}
	streamID = strings.TrimSpace(streamID)
	if streamID != "" {
		s.sessionCompactionMu.Lock()
		if gate.readers == nil {
			gate.readers = make(map[string]map[*sessionCompactionReader]struct{})
		}
		if gate.readers[streamID] == nil {
			gate.readers[streamID] = make(map[*sessionCompactionReader]struct{})
		}
		gate.readers[streamID][reader] = struct{}{}
		s.sessionCompactionMu.Unlock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			reader.close()
			if streamID != "" {
				s.sessionCompactionMu.Lock()
				delete(gate.readers[streamID], reader)
				if len(gate.readers[streamID]) == 0 {
					delete(gate.readers, streamID)
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

func (s *Service) enterSessionCompactionForStream(botID, sessionID, streamID string) func() {
	gate, key := s.acquireSessionCompactionGate(botID, sessionID)
	if gate == nil {
		return func() {}
	}
	readers := s.sessionCompactionReaders(gate, streamID)
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

func (s *Service) sessionCompactionReaders(gate *sessionCompactionGate, streamID string) []*sessionCompactionReader {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return nil
	}
	s.sessionCompactionMu.Lock()
	defer s.sessionCompactionMu.Unlock()
	readers := make([]*sessionCompactionReader, 0, len(gate.readers[streamID]))
	for reader := range gate.readers[streamID] {
		readers = append(readers, reader)
	}
	return readers
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
