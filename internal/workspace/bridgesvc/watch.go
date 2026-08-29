package bridgesvc

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	watchCoalesceWindow = 100 * time.Millisecond
	watchSinkBuffer     = 512
)

// watchSink is one WatchDir stream's mailbox. Overflow drops the event and
// records it; the handler then reports the watched directory itself as
// changed, degrading to a full re-list instead of losing the change.
type watchSink struct {
	events chan fsnotify.Event
	drops  atomic.Int64
}

// sharedWatcher multiplexes every WatchDir stream over ONE fsnotify watcher:
// each inotify instance is a scarce kernel resource (fs.inotify.max_user_instances
// defaults to 128), so per-stream watchers would let a single busy files pane
// starve every other inotify user in the container. Directories are added on
// first sink and removed with the last one.
type sharedWatcher struct {
	mu      sync.Mutex
	watcher *fsnotify.Watcher
	sinks   map[string]map[int]*watchSink
	nextID  int
}

func (w *sharedWatcher) ensureStartedLocked() error {
	if w.watcher != nil {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = watcher
	w.sinks = make(map[string]map[int]*watchSink)
	go w.route(watcher)
	return nil
}

func (w *sharedWatcher) route(watcher *fsnotify.Watcher) {
	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				w.fail()
				return
			}
			w.deliver(filepath.Dir(ev.Name), ev)
			// A watched directory's own removal/rename arrives with the
			// directory as the event name; its own sinks need it to end
			// their streams, in addition to the parent's sinks above.
			w.deliver(ev.Name, ev)
		case _, ok := <-watcher.Errors:
			if !ok {
				w.fail()
				return
			}
			w.fail()
			return
		}
	}
}

func (w *sharedWatcher) deliver(dir string, ev fsnotify.Event) {
	w.mu.Lock()
	targets := make([]*watchSink, 0, len(w.sinks[dir]))
	for _, sink := range w.sinks[dir] {
		targets = append(targets, sink)
	}
	w.mu.Unlock()
	for _, sink := range targets {
		select {
		case sink.events <- ev:
		default:
			sink.drops.Add(1)
		}
	}
}

// fail tears the shared watcher down: every sink's channel closes so handlers
// end their streams, and the next WatchDir lazily starts a fresh watcher.
// Idempotent — a second call finds no sinks and no watcher.
func (w *sharedWatcher) fail() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, sinks := range w.sinks {
		for _, sink := range sinks {
			close(sink.events)
		}
	}
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
	w.watcher = nil
	w.sinks = nil
}

func (w *sharedWatcher) add(dir string) (*watchSink, int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureStartedLocked(); err != nil {
		return nil, 0, err
	}
	if len(w.sinks[dir]) == 0 {
		if err := w.watcher.Add(dir); err != nil {
			return nil, 0, err
		}
	}
	w.nextID++
	id := w.nextID
	sink := &watchSink{events: make(chan fsnotify.Event, watchSinkBuffer)}
	if w.sinks[dir] == nil {
		w.sinks[dir] = make(map[int]*watchSink)
	}
	w.sinks[dir][id] = sink
	return sink, id, nil
}

func (w *sharedWatcher) remove(dir string, id int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sinks, ok := w.sinks[dir]
	if !ok {
		return
	}
	delete(sinks, id)
	if len(sinks) == 0 {
		delete(w.sinks, dir)
		if w.watcher != nil {
			// The directory may already be gone; fsnotify drops such watches
			// on its own and Remove then errors — nothing to do about it.
			_ = w.watcher.Remove(dir)
		}
	}
}

func (w *sharedWatcher) instances() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watcher != nil {
		return 1
	}
	return 0
}

func (s *Server) activeWatchInstances() int {
	return s.watches.instances()
}

// WatchDir watches one directory (non-recursive) and streams coalesced change
// batches with container-visible paths. Chmod-only events are dropped: they
// carry no content or listing change the UI could render.
func (s *Server) WatchDir(req *pb.WatchDirRequest, stream grpc.ServerStreamingServer[pb.WatchDirEvent]) error {
	requested := strings.TrimSpace(req.GetPath())
	if requested == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}
	dir := s.resolvePath(requested)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return status.Error(codes.NotFound, "directory not found")
		}
		return status.Error(codes.Internal, err.Error())
	}
	if !info.IsDir() {
		return status.Error(codes.InvalidArgument, "path is not a directory")
	}

	sink, id, err := s.watches.add(dir)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	defer s.watches.remove(dir, id)

	ctx := stream.Context()
	pending := make(map[string]struct{})
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-sink.events:
			if !ok {
				return status.Error(codes.Internal, "filesystem watch failed")
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			pending[watchEventPath(requested, dir, ev.Name)] = struct{}{}
			if timer == nil {
				timer = time.NewTimer(watchCoalesceWindow)
				timerC = timer.C
			}
			// The watched directory itself disappearing ends the watch: flush
			// what we have (including the directory path) and let the stream
			// end tell the host to re-list the parent and re-subscribe.
			if ev.Name == dir && ev.Op.Has(fsnotify.Remove|fsnotify.Rename) {
				return stream.Send(&pb.WatchDirEvent{Paths: drainWatchPending(requested, sink, pending)})
			}
		case <-timerC:
			timer = nil
			timerC = nil
			if err := stream.Send(&pb.WatchDirEvent{Paths: drainWatchPending(requested, sink, pending)}); err != nil {
				return err
			}
		}
	}
}

func watchEventPath(requested, watchedDir, eventPath string) string {
	if eventPath == watchedDir {
		return requested
	}
	return path.Join(requested, filepath.Base(eventPath))
}

func drainWatchPending(requested string, sink *watchSink, pending map[string]struct{}) []string {
	if sink.drops.Swap(0) > 0 {
		pending[requested] = struct{}{}
	}
	paths := make([]string, 0, len(pending))
	for p := range pending {
		paths = append(paths, p)
	}
	for p := range pending {
		delete(pending, p)
	}
	return paths
}
