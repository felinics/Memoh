package workspace

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	fsWatchRetryDelay     = 5 * time.Second
	fsWatchUnsupportedTTL = 5 * time.Minute
	// A watch that dies before this uptime never actually observed the
	// directory (dial failure, stopped workspace) — it missed nothing, so no
	// stale-dir signal is sent and only the retry remains.
	fsWatchEstablishedAfter = 2 * time.Second
	// Budgets bound the amplification an authorized client can drive by
	// fanning out fs_watch sets over many connections: every unique
	// (bot, dir) costs a host goroutine, a bridge stream, and bridge watch
	// state. Over-budget keys stay subscribed but watchless (event-only
	// freshness still works); existing watches are never evicted.
	fsWatchMaxDirsPerBot = 128
	fsWatchMaxDirsTotal  = 1024
)

type fsWatchKey struct {
	botID string
	dir   string
}

type fsDirWatch struct {
	cancel context.CancelFunc
}

// FSWatchService maintains refcounted per-(bot, dir) bridge watch streams
// driven by viewer subscriptions (the set of directories expanded in a files
// pane) and forwards change batches to the fsevent hub. Watch lifetime equals
// viewer attention: when the last subscription for a directory goes away the
// stream is canceled, so idle workspaces carry no standing watches.
type FSWatchService struct {
	logger  *slog.Logger
	publish func(botID string, paths []string)
	// watchDir blocks while streaming batches; swapped in tests.
	watchDir         func(ctx context.Context, botID, dir string, onBatch func([]string)) error
	retryDelay       time.Duration
	establishedAfter time.Duration
	unsupportedFor   time.Duration
	maxPerBot        int
	maxTotal         int

	mu          sync.Mutex
	subs        map[string]map[fsWatchKey]struct{}
	watches     map[fsWatchKey]*fsDirWatch
	unsupported map[string]time.Time
}

func NewFSWatchService(logger *slog.Logger, clients bridge.Provider, publish func(botID string, paths []string)) *FSWatchService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &FSWatchService{
		logger:           logger,
		publish:          publish,
		retryDelay:       fsWatchRetryDelay,
		establishedAfter: fsWatchEstablishedAfter,
		unsupportedFor:   fsWatchUnsupportedTTL,
		maxPerBot:        fsWatchMaxDirsPerBot,
		maxTotal:         fsWatchMaxDirsTotal,
		subs:             make(map[string]map[fsWatchKey]struct{}),
		watches:          make(map[fsWatchKey]*fsDirWatch),
		unsupported:      make(map[string]time.Time),
	}
	s.watchDir = func(ctx context.Context, botID, dir string, onBatch func([]string)) error {
		client, err := clients.MCPClient(ctx, botID)
		if err != nil {
			return err
		}
		return client.WatchDir(ctx, dir, onBatch)
	}
	return s
}

// SetSubscription replaces subID's watched directory set for botID. An empty
// dirs slice clears it.
func (s *FSWatchService) SetSubscription(subID, botID string, dirs []string) {
	if subID == "" || botID == "" {
		return
	}
	want := make(map[fsWatchKey]struct{}, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		want[fsWatchKey{botID: botID, dir: dir}] = struct{}{}
	}

	s.mu.Lock()
	freed := false
	current := s.subs[subID]
	for key := range current {
		if _, keep := want[key]; !keep {
			freed = s.releaseLocked(subID, key) || freed
		}
	}
	if len(want) > 0 {
		if s.subs[subID] == nil {
			s.subs[subID] = make(map[fsWatchKey]struct{})
		}
		// Every wanted key is (re)acquired — not just newly added ones — so a
		// subscription whose watch died as unsupported can recover after the
		// TTL even when the client re-sends an identical directory set.
		for key := range want {
			s.subs[subID][key] = struct{}{}
			s.acquireLocked(key)
		}
	}
	if len(s.subs[subID]) == 0 {
		delete(s.subs, subID)
	}
	if freed {
		s.promoteWatchlessLocked(nil)
	}
	s.mu.Unlock()
}

// DropSubscription releases every directory subID was watching.
func (s *FSWatchService) DropSubscription(subID string) {
	s.mu.Lock()
	freed := false
	for key := range s.subs[subID] {
		freed = s.releaseLocked(subID, key) || freed
	}
	delete(s.subs, subID)
	if freed {
		s.promoteWatchlessLocked(nil)
	}
	s.mu.Unlock()
}

func (s *FSWatchService) acquireLocked(key fsWatchKey) {
	if _, ok := s.watches[key]; ok {
		return
	}
	s.startWatchLocked(key)
}

func (s *FSWatchService) startWatchLocked(key fsWatchKey) {
	if until, ok := s.unsupported[key.botID]; ok {
		if time.Now().Before(until) {
			return
		}
		delete(s.unsupported, key.botID)
	}
	if len(s.watches) >= s.maxTotal {
		return
	}
	botCount := 0
	for existing := range s.watches {
		if existing.botID == key.botID {
			botCount++
		}
	}
	if botCount >= s.maxPerBot {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.watches[key] = &fsDirWatch{cancel: cancel}
	go s.runWatch(ctx, key)
}

func (s *FSWatchService) releaseLocked(subID string, key fsWatchKey) (watchFreed bool) {
	delete(s.subs[subID], key)
	if s.hasSubscriberLocked(key) {
		return false
	}
	watch, ok := s.watches[key]
	if !ok {
		return false
	}
	watch.cancel()
	delete(s.watches, key)
	return true
}

// promoteWatchlessLocked starts watches for still-wanted keys left watchless
// by an earlier budget rejection. The client suppresses unchanged fs_watch
// reports, so freed capacity must be handed out here or it stays unused until
// an unrelated report arrives. startWatchLocked's budget and unsupported
// gates bound the fill; Go's random map order spreads promotion across
// subscriptions and bots. skip names a key promotion must not touch — the
// one whose stream just died, which belongs to the delayed retry (starting
// it here would bypass the backoff and spin on a persistently failing watch).
func (s *FSWatchService) promoteWatchlessLocked(skip *fsWatchKey) {
	for _, keys := range s.subs {
		for key := range keys {
			if skip != nil && key == *skip {
				continue
			}
			if _, running := s.watches[key]; !running {
				s.startWatchLocked(key)
			}
		}
	}
}

func (s *FSWatchService) reacquireBotKeys(botID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, keys := range s.subs {
		for key := range keys {
			if key.botID != botID {
				continue
			}
			if _, running := s.watches[key]; !running {
				s.startWatchLocked(key)
			}
		}
	}
}

func (s *FSWatchService) hasSubscriberLocked(key fsWatchKey) bool {
	for _, keys := range s.subs {
		if _, ok := keys[key]; ok {
			return true
		}
	}
	return false
}

func (s *FSWatchService) runWatch(ctx context.Context, key fsWatchKey) {
	startedAt := time.Now()
	err := s.watchDir(ctx, key.botID, key.dir, func(paths []string) {
		if s.publish != nil {
			s.publish(key.botID, paths)
		}
	})
	if ctx.Err() != nil {
		return
	}
	established := time.Since(startedAt) >= s.establishedAfter

	// Every deletion from s.watches frees budget and is followed by one
	// promotion pass, so a waiting watchless key can take the slot — the
	// unsupported mark is recorded first so promotion skips that bot.
	s.mu.Lock()
	delete(s.watches, key)
	stillWanted := s.hasSubscriberLocked(key)
	if errors.Is(err, bridge.ErrWatchUnsupported) {
		s.unsupported[key.botID] = time.Now().Add(s.unsupportedFor)
		s.promoteWatchlessLocked(nil) //nolint:contextcheck // promoted watches outlive the dead stream's context by design and own fresh ones.
		s.mu.Unlock()
		// The client suppresses identical fs_watch reports, so nothing external
		// re-triggers acquisition when the TTL lapses (e.g. after an in-place
		// bridge upgrade). One timer per unsupported mark reacquires whatever
		// is still wanted for the bot.
		time.AfterFunc(s.unsupportedFor, func() { //nolint:contextcheck // outlives the dead stream's context by design; new watches own fresh ones.
			s.reacquireBotKeys(key.botID)
		})
		return
	}
	s.promoteWatchlessLocked(&key) //nolint:contextcheck // promoted watches outlive the dead stream's context by design and own fresh ones.
	s.mu.Unlock()

	if !stillWanted {
		return
	}
	// A stream that died after being established may have missed events
	// anywhere under its directory, so send a wildcard (a path-scoped signal
	// would only refresh the parent listing, not the stale directory itself);
	// then re-attempt after a delay (bounded — one timer per dead watch,
	// re-armed only while wanted).
	if established && s.publish != nil {
		s.publish(key.botID, nil)
	}
	if err != nil {
		s.logger.Debug("workspace fs watch ended; scheduling retry",
			slog.String("bot_id", key.botID),
			slog.String("dir", key.dir),
			slog.Any("error", err))
	}
	time.AfterFunc(s.retryDelay, func() { //nolint:contextcheck // the retry outlives the dead stream's context by design; the new watch owns a fresh one.
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, running := s.watches[key]; running {
			return
		}
		if !s.hasSubscriberLocked(key) {
			return
		}
		s.startWatchLocked(key)
	})
}
