package sessionruntime

import (
	"context"
	"strings"
)

type targetedSteerBackend interface {
	EnqueueSteerForRun(context.Context, Key, string, string, string, []byte) (SteerItem, error)
}

func (m *Manager) EnqueueSteerForRun(ctx context.Context, key Key, runID, itemID, invocationID string, payload []byte) (SteerItem, error) {
	if strings.TrimSpace(runID) == "" {
		return SteerItem{}, ErrQueueInvalidReference
	}
	queue, err := m.liveQueueBackend()
	if err != nil {
		return SteerItem{}, err
	}
	targeted, ok := queue.(targetedSteerBackend)
	if !ok {
		return SteerItem{}, ErrQueueSteerUnsupported
	}
	item, err := targeted.EnqueueSteerForRun(ctx, key, runID, itemID, invocationID, payload)
	if err == nil && item.Status == QueueAccepted {
		m.notifySteer(ctx, key, item.TargetRunID)
	}
	return item, err
}
