package application

import (
	"sync"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// runtimeNoticeCodes are the public codes of the notices runtimes report.
var runtimeNoticeCodes = map[event.NoticeKind]apperror.Code{
	event.NoticeNativeHistoryLost:   apperror.CodeRuntimeNativeHistoryLost,
	event.NoticeToolsUnavailable:    apperror.CodeRuntimeToolsUnavailable,
	event.NoticeElicitationDeclined: apperror.CodeRuntimeElicitationDeclined,
	event.NoticeSteerFailed:         apperror.CodeRuntimeControlSteerFailed,
}

// publicRuntimeNotice gives a runtime notice its public code before the event
// reaches any consumer. A notice without text takes the code's catalog detail.
func publicRuntimeNotice(ev event.StreamEvent) event.StreamEvent {
	if ev.Type != event.RuntimeNotice || ev.NoticeKind == "" {
		return ev
	}
	code, ok := runtimeNoticeCodes[ev.NoticeKind]
	if !ok {
		return ev
	}
	ev.Code = string(code)
	if ev.Delta == "" {
		if definition, ok := apperror.Lookup(code); ok {
			ev.Delta = definition.Detail
		}
	}
	ev.NoticeKind = ""
	return ev
}

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
