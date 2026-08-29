package application

import (
	"context"
	"errors"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
)

// bindQueueContinuation installs the same durable step boundary used by an
// initially admitted turn onto an application-owned continuation of that run.
// Decision continuations are separate native invocations, but they are not
// separate session runs: they retain the original owner/fence and continue at
// the next durable step index.
func (s *Service) bindQueueContinuation(
	ctx context.Context,
	req *ChatRequest,
	cfg *native.RunConfig,
	rc resolvedContext,
) (*agentStepCommitter, func(), error) {
	noop := func() {}
	if s == nil || req == nil || cfg == nil || s.queueCoordinator == nil ||
		req.RunHandle.RunID == "" || req.RunHandle.OwnerID == "" || req.RunHandle.FencingToken <= 0 {
		return nil, noop, nil
	}

	stepOffset := req.StepIndexOffset
	if s.queueStore != nil {
		var err error
		stepOffset, err = s.queueStore.NextQueueStepIndex(ctx, req.RunHandle.RunID)
		if err != nil {
			return nil, noop, err
		}
	}
	req.StepIndexOffset = stepOffset
	cfg.StepIndexOffset = stepOffset

	queueInput := make(chan turn.InjectMessage, 16)
	nativeInput := make(chan native.InjectMessage, 16)
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(done) }) }
	existingInput := cfg.InjectCh
	go func() {
		defer close(nativeInput)
		for {
			select {
			case <-done:
				return
			case msg, ok := <-existingInput:
				if !ok {
					existingInput = nil
					continue
				}
				select {
				case nativeInput <- msg:
				case <-done:
					return
				}
			case msg := <-queueInput:
				nativeMessage := native.InjectMessage{Text: msg.Text, HeaderifiedText: msg.HeaderifiedText}
				select {
				case nativeInput <- nativeMessage:
				case <-done:
					return
				}
			}
		}
	}()

	req.QueueInjectCh = queueInput
	cfg.InjectCh = nativeInput
	committer := s.newAgentStepCommitter(ctx, *req, rc)
	if committer == nil {
		stop()
		return nil, noop, errors.New("durable queue step committer is unavailable for decision continuation")
	}
	cfg.ContinueAfterFinal = &committer.continueAfterFinal
	cfg.NextModelInputs = &committer.nextModelInputs
	return committer, stop, nil
}
