package channel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// processingStatusTimeout bounds each Started, Completed and Failed call.
const processingStatusTimeout = 60 * time.Second

// Processing status stages reported to ProcessingStatusErrorFunc.
const (
	ProcessingStageStarted   = "processing_started"
	ProcessingStageRefresh   = "processing_refresh"
	ProcessingStageCompleted = "processing_completed"
	ProcessingStageFailed    = "processing_failed"
)

// ProcessingStatusRefresher is implemented by notifiers whose platform
// indicator expires and must be re-sent while processing continues, such as
// the Telegram and Discord typing actions.
type ProcessingStatusRefresher interface {
	// ProcessingRefreshInterval is how often the indicator is re-sent. A
	// non-positive interval disables refreshing.
	ProcessingRefreshInterval() time.Duration
	ProcessingRefresh(ctx context.Context, cfg ChannelConfig, msg InboundMessage, info ProcessingStatusInfo, handle ProcessingStatusHandle) error
}

// ProcessingStatusErrorFunc receives the best-effort failures of a
// ProcessingStatus, keyed by one of the ProcessingStage constants.
type ProcessingStatusErrorFunc func(ctx context.Context, stage string, err error)

// ProcessingStatus drives the platform processing indicator for one turn:
// Started once, Refresh on the notifier's interval while the turn runs, then
// exactly one of Completed or Failed. Every call is best-effort; failures go
// to the error func and never fail the turn. A nil notifier yields an inert
// session, so callers need no nil checks.
type ProcessingStatus struct {
	notifier ProcessingStatusNotifier
	cfg      ChannelConfig
	msg      InboundMessage
	info     ProcessingStatusInfo
	handle   ProcessingStatusHandle
	onError  ProcessingStatusErrorFunc

	stopOnce   sync.Once
	stopCh     chan struct{}
	refreshed  chan struct{}
	finishOnce sync.Once
	paused     atomic.Bool
	resumed    chan struct{}
}

// StartProcessingStatus reports Started and, when the notifier implements
// ProcessingStatusRefresher, keeps the indicator alive until StopRefresh,
// Complete or Fail. ctx scopes Started and the refreshes, and its
// cancellation ends refreshing.
func StartProcessingStatus(ctx context.Context, notifier ProcessingStatusNotifier, cfg ChannelConfig, msg InboundMessage, info ProcessingStatusInfo, onError ProcessingStatusErrorFunc) *ProcessingStatus {
	s := &ProcessingStatus{
		notifier: notifier,
		cfg:      cfg,
		msg:      msg,
		info:     info,
		onError:  onError,
		stopCh:   make(chan struct{}),
		resumed:  make(chan struct{}, 1),
	}
	if notifier == nil {
		return s
	}
	statusCtx, cancel := context.WithTimeout(ctx, processingStatusTimeout)
	handle, err := notifier.ProcessingStarted(statusCtx, cfg, msg, info)
	cancel()
	if err != nil {
		s.report(ctx, ProcessingStageStarted, err)
	} else {
		s.handle = handle
	}
	if refresher, ok := notifier.(ProcessingStatusRefresher); ok {
		if interval := refresher.ProcessingRefreshInterval(); interval > 0 {
			s.refreshed = make(chan struct{})
			go s.refreshLoop(ctx, refresher, interval)
		}
	}
	return s
}

// refreshLoop re-sends the indicator every interval. Each call is bounded by
// the interval, since a refresh that takes longer is already stale. Only the
// first failure is reported: a chat the bot can no longer reach would
// otherwise log once per interval for the rest of the turn.
func (s *ProcessingStatus) refreshLoop(ctx context.Context, refresher ProcessingStatusRefresher, interval time.Duration) {
	defer close(s.refreshed)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	reported := false
	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.resumed:
			ticker.Reset(interval)
		}
		// The ticker and stop may be ready together; stop wins.
		select {
		case <-s.stopCh:
			return
		default:
		}
		if s.paused.Load() {
			continue
		}
		refreshCtx, cancel := context.WithTimeout(ctx, min(interval, processingStatusTimeout))
		err := refresher.ProcessingRefresh(refreshCtx, s.cfg, s.msg, s.info, s.handle)
		cancel()
		if err != nil && !reported {
			reported = true
			s.report(ctx, ProcessingStageRefresh, err)
		}
	}
}

// StopRefresh ends refreshing and returns only after an in-flight refresh
// has returned, so no indicator is sent after the caller delivers its reply.
// An in-flight refresh is waited for rather than canceled: canceling a
// request already on the wire does not stop the platform from applying it.
func (s *ProcessingStatus) StopRefresh() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	if s.refreshed != nil {
		<-s.refreshed
	}
}

// PauseRefresh holds the indicator while the turn waits on the user, such as
// a pending tool approval or ask_user question; the platform indicator then
// lapses on its own. ResumeRefresh re-sends it at once and restarts the
// interval.
func (s *ProcessingStatus) PauseRefresh() {
	if s == nil {
		return
	}
	s.paused.Store(true)
}

// ResumeRefresh undoes PauseRefresh. It is cheap to call when not paused.
func (s *ProcessingStatus) ResumeRefresh() {
	if s == nil || !s.paused.CompareAndSwap(true, false) {
		return
	}
	select {
	case s.resumed <- struct{}{}:
	default:
	}
}

// Complete stops refreshing and reports Completed. Only the first Complete
// or Fail reports; later calls are no-ops.
func (s *ProcessingStatus) Complete(ctx context.Context) {
	s.finish(ctx, nil)
}

// Fail stops refreshing and reports Failed with cause. Only the first
// Complete or Fail reports; later calls are no-ops.
func (s *ProcessingStatus) Fail(ctx context.Context, cause error) {
	s.finish(ctx, cause)
}

func (s *ProcessingStatus) finish(ctx context.Context, cause error) {
	if s == nil {
		return
	}
	s.StopRefresh()
	s.finishOnce.Do(func() {
		if s.notifier == nil {
			return
		}
		// Completed and Failed clear the indicator (a Feishu reaction stays
		// on the message otherwise), so they run even when ctx was canceled.
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), processingStatusTimeout)
		defer cancel()
		if cause == nil {
			if err := s.notifier.ProcessingCompleted(statusCtx, s.cfg, s.msg, s.info, s.handle); err != nil {
				s.report(ctx, ProcessingStageCompleted, err)
			}
			return
		}
		if err := s.notifier.ProcessingFailed(statusCtx, s.cfg, s.msg, s.info, s.handle, cause); err != nil {
			s.report(ctx, ProcessingStageFailed, err)
		}
	})
}

func (s *ProcessingStatus) report(ctx context.Context, stage string, err error) {
	if s.onError != nil {
		s.onError(ctx, stage, err)
	}
}
