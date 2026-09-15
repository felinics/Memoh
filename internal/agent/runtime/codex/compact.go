package codex

import (
	"context"
	"errors"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var _ external.Compactor = (*Driver)(nil)

func (d *Driver) Compact(ctx context.Context, input external.PromptInput) (external.CompactionResult, error) {
	if metadataString(input.RuntimeMetadata, metadataThreadIDKey) == "" {
		return external.CompactionResult{}, external.ErrThreadUnavailable
	}
	cfg, _, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID, true)
	if err != nil {
		return external.CompactionResult{}, err
	}
	srv, release, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.CompactionResult{}, err
	}
	defer release()
	if err := srv.ensureAuth(ctx, cfg); err != nil {
		return external.CompactionResult{}, err
	}
	input.Command = "compact"
	threadID, _, err := d.ensureThread(ctx, srv, cfg, input)
	if err != nil {
		return external.CompactionResult{}, err
	}
	// Compaction uses the runtime's turn lifecycle, but none of its transcript
	// events become chat messages. The caller publishes operation state.
	input.CanRequestUserInput = false
	input.Sink = external.EventSinkFunc(func(event.StreamEvent) {})
	var waiter func(string) func()
	if d.approval != nil {
		waiter = d.approval.RegisterWaiter
	}
	turn := newTurnState(ctx, input, threadID, d.approval, waiter, d.userInput, srv.toolLookup, d.logger)
	defer turn.close()
	srv.registerTurn(threadID, turn)
	defer srv.unregisterTurn(threadID, turn)
	metadata, err := awaitCompaction(ctx, srv.conn, turn, srv.proc.Done(), func() { d.cancelOperation(srv, input, turn) })
	result := external.CompactionResult{RuntimeMetadata: metadata}
	if err == nil && d.stateStore != nil {
		if err := d.stageCheckpoint(ctx, srv, input, threadID, turn.currentTurnID()); err != nil {
			return result, checkpointError(err)
		}
		result.Checkpoint = external.CheckpointStaged
	}
	return result, err
}

// Acknowledgement only starts the operation; turn/completed owns its outcome.
func awaitCompaction(ctx context.Context, connection *conn, turn *turnState, processDone <-chan struct{}, cancelOperation func()) (map[string]any, error) {
	if err := connection.Call(ctx, protocol.MethodThreadCompactStart, protocol.ThreadCompactStartParams{ThreadID: turn.threadID}, nil); err != nil {
		if ctx.Err() != nil {
			cancelOperation()
		}
		return nil, err
	}
	select {
	case <-turn.done:
	case <-ctx.Done():
		cancelOperation()
		return nil, ctx.Err()
	case <-processDone:
		return nil, errors.New("codex app-server exited during compaction")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result, err := turn.result("")
	if err != nil {
		return nil, err
	}
	if !result.TurnCompleted {
		return nil, context.Canceled
	}
	return result.RuntimeMetadata, nil
}

func (d *Driver) cancelOperation(srv *appServer, input external.PromptInput, turn *turnState) {
	id := turn.currentTurnID()
	if id == "" {
		d.ResetBotAgent(input.BotID, input.BotAgentID)
		return
	}
	d.interruptTurn(srv, turn.threadID, id)
	timer := time.NewTimer(interruptSettleTimeout)
	defer timer.Stop()
	select {
	case <-turn.done:
	case <-srv.proc.Done():
	case <-timer.C:
		d.ResetBotAgent(input.BotID, input.BotAgentID)
	}
}
