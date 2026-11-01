package application

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/native"
)

// bindQueueContinuation installs the live queue step boundary used by an
// initially admitted turn onto an application-owned native continuation. Queue
// items are transient; history remains persisted by the normal message
// service, while claims are fenced by the session runtime backend.
func (s *Service) bindQueueContinuation(
	ctx context.Context,
	req *ChatRequest,
	cfg *native.RunConfig,
	rc resolvedContext,
	timing *reasoningTimingTracker,
) (*agentStepCommitter, error) {
	if s == nil || req == nil || cfg == nil || s.sessionManager == nil ||
		req.RunHandle.RunID == "" || req.RunHandle.OwnerID == "" || req.RunHandle.FencingToken <= 0 {
		return nil, nil
	}

	stepIndex, err := s.sessionManager.ContinuationStepIndex(req.RunHandle)
	if err != nil {
		return nil, err
	}
	req.StepIndexOffset = stepIndex
	cfg.StepIndexOffset = stepIndex

	req.QueueSteerEnabled = true
	committer := s.bindNativeRun(ctx, *req, rc, cfg, timing)
	if committer == nil {
		return nil, errors.New("live queue step committer is unavailable for decision continuation")
	}
	return committer, nil
}
