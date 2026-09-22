package application

import (
	"sync"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// runtimeNotices belongs to the application invocation, not the driver's turn
// recorder: startup notices can arrive before a driver creates its turn.
type runtimeNotices struct {
	mu      sync.Mutex
	notices []event.Notice
}

func (n *runtimeNotices) observe(ev event.StreamEvent) bool {
	notice, ok := event.NoticeFromStream(ev)
	if !ok {
		return true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	previous := len(n.notices)
	n.notices = event.AppendNotice(n.notices, notice)
	return len(n.notices) != previous
}

func (n *runtimeNotices) apply(result *external.PromptResult) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, notice := range n.notices {
		result.Notices = event.AppendNotice(result.Notices, notice)
	}
}
