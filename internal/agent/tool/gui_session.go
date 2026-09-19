package tools

import (
	"strings"
	"sync"
	"time"
)

// guiSessionState is the per-session default context for the GUI tools: the
// browser, tab, and application the session last selected, the snapshot each
// target was last observed with, and the input the session currently holds
// down. It is deliberately scoped to one session (thread) so two threads
// driving the same workspace never overwrite each other's defaults, and it is
// ephemeral: a server restart simply requires observing again.
type guiSessionState struct {
	mu sync.Mutex

	browserID string
	tabID     string
	appID     string

	// browserSnapshots records, per tab id, the snapshot that assigned the
	// refs currently valid on that tab. Navigation clears the entry.
	browserSnapshots map[string]guiSnapshotRecord
	// computerSnapshot is the last desktop/application snapshot.
	computerSnapshot *guiSnapshotRecord

	// heldKeys tracks keys the session pressed with keydown and has not
	// released, so later key events carry the right modifiers and the keys
	// can be released when the target goes away.
	heldKeys map[string]struct{}
	// heldButtons is the RFB button mask the session last left pressed via
	// mouse_move / pointer, released on the next terminal error.
	heldButtons byte
}

// guiSnapshotRecord identifies one observation of one target.
type guiSnapshotRecord struct {
	ID        string
	BrowserID string
	TabID     string
	AppID     string
	Taken     time.Time
}

type guiSessionStore struct {
	mu     sync.Mutex
	states map[string]*guiSessionState
}

func newGUISessionStore() *guiSessionStore {
	return &guiSessionStore{states: make(map[string]*guiSessionState)}
}

// guiSessionKey isolates defaults per bot and session. Sessions without an
// id (some transports) fall back to the chat id, then to a shared bucket.
func guiSessionKey(session SessionContext) string {
	id := strings.TrimSpace(session.SessionID)
	if id == "" {
		id = strings.TrimSpace(session.ChatID)
	}
	if id == "" {
		id = "default"
	}
	return strings.TrimSpace(session.BotID) + "|" + id
}

func (s *guiSessionStore) get(session SessionContext) *guiSessionState {
	key := guiSessionKey(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[key]
	if !ok {
		state = &guiSessionState{
			browserSnapshots: make(map[string]guiSnapshotRecord),
			heldKeys:         make(map[string]struct{}),
		}
		s.states[key] = state
	}
	return state
}

func (st *guiSessionState) defaults() (browserID, tabID, appID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.browserID, st.tabID, st.appID
}

func (st *guiSessionState) selectTab(browserID, tabID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.browserID = browserID
	st.tabID = tabID
}

func (st *guiSessionState) selectBrowser(browserID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.browserID != browserID {
		st.tabID = ""
	}
	st.browserID = browserID
}

func (st *guiSessionState) selectApp(appID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.appID = appID
}

// forgetTab drops the default and snapshot bookkeeping for a closed tab.
func (st *guiSessionState) forgetTab(tabID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.browserSnapshots, tabID)
	if st.tabID == tabID {
		st.tabID = ""
	}
}

func (st *guiSessionState) recordBrowserSnapshot(rec guiSnapshotRecord) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.browserSnapshots[rec.TabID] = rec
}

// invalidateBrowserSnapshot forgets the refs of a tab whose document changed
// (navigation, reload, history). Refs from before are refused afterwards.
func (st *guiSessionState) invalidateBrowserSnapshot(tabID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.browserSnapshots, tabID)
}

func (st *guiSessionState) browserSnapshot(tabID string) (guiSnapshotRecord, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.browserSnapshots[tabID]
	return rec, ok
}

func (st *guiSessionState) recordComputerSnapshot(rec guiSnapshotRecord) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.computerSnapshot = &rec
}

func (st *guiSessionState) lastComputerSnapshot() (guiSnapshotRecord, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.computerSnapshot == nil {
		return guiSnapshotRecord{}, false
	}
	return *st.computerSnapshot, true
}

func (st *guiSessionState) holdKey(key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.heldKeys[normalizeKeyName(key)] = struct{}{}
}

func (st *guiSessionState) releaseKey(key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.heldKeys, normalizeKeyName(key))
}

// heldModifiers returns the CDP modifier bitmask of the keys still held.
func (st *guiSessionState) heldModifiers() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	mods := 0
	for key := range st.heldKeys {
		mods |= cdpModifier(key)
	}
	return mods
}

func (st *guiSessionState) setHeldButtons(mask byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.heldButtons = mask
}

func (st *guiSessionState) takeHeldButtons() byte {
	st.mu.Lock()
	defer st.mu.Unlock()
	mask := st.heldButtons
	st.heldButtons = 0
	return mask
}
