package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/runtimekind"
)

const maxQueuedSubagentMessages = 16

func boundedAgentTaskContext(parent context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := parent.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Duration(background.BackgroundExecTimeout) * time.Second)
	}
	return context.WithDeadline(context.WithoutCancel(parent), deadline)
}

func (p *SpawnProvider) subagentBusy(ctx context.Context, parent SessionContext, childID string) (bool, error) {
	runtime, ok := p.admitter.(SubagentRuntimeService)
	if !ok {
		return false, nil
	}
	state, err := runtime.SnapshotSubagent(ctx, parent, childID)
	if err != nil {
		return false, err
	}
	if state.Status == "execution_unknown" {
		return false, apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil)
	}
	return state.Active, nil
}

func (c *agentCoordinator) lockExternal(req *agentRequest) func() {
	if !runtimekind.IsExternal(req.runtimeType) {
		return func() {}
	}
	c.mu.Lock()
	if c.externalLocks == nil {
		c.externalLocks = make(map[string]*sync.Mutex)
	}
	lock := c.externalLocks[req.parentSession.BotID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.externalLocks[req.parentSession.BotID] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// lockExternalSlots first settles what the bot's children left unconfirmed,
// then serializes the bot's slot accounting and reads the children whose
// durable execution record still holds a slot. A child releases its slot here
// only after that record is written, so the lock keeps a release from slipping
// between the read and the reservation.
func (p *SpawnProvider) lockExternalSlots(ctx context.Context, req *agentRequest) ([]string, func(), error) {
	if !runtimekind.IsExternal(req.runtimeType) || req.externalSlot {
		return nil, p.coord.lockExternal(req), nil
	}
	if err := p.externalSubagents.SettleExternalSubagents(ctx, req.parentSession.BotID); err != nil {
		return nil, func() {}, err
	}
	unlock := p.coord.lockExternal(req)
	held, err := p.externalSubagents.HeldExternalSubagents(ctx, req.parentSession.BotID)
	if err != nil {
		unlock()
		return nil, func() {}, err
	}
	return held, unlock, nil
}

func (p *SpawnProvider) reserveExternalSlotLocked(req *agentRequest, held []string) error {
	if !runtimekind.IsExternal(req.runtimeType) || req.externalSlot {
		return nil
	}
	botID := req.parentSession.BotID
	running := p.coord.externalRunning[botID]
	children := make(map[string]struct{}, len(running))
	for _, child := range running {
		children[child] = struct{}{}
	}
	occupied := len(running)
	for _, id := range held {
		if _, ok := children[id]; !ok {
			occupied++
		}
	}
	if occupied >= background.MaxRunningSpawnTasks {
		return fmt.Errorf("external subagent limit reached: at most %d concurrent children per bot on this server", background.MaxRunningSpawnTasks)
	}
	if running == nil {
		if p.coord.externalRunning == nil {
			p.coord.externalRunning = make(map[string]map[*agentRequest]string)
		}
		running = make(map[*agentRequest]string)
		p.coord.externalRunning[botID] = running
	}
	running[req] = req.agentSessionID
	req.externalSlot = true
	return nil
}

func (p *SpawnProvider) releaseExternalSlotLocked(req *agentRequest) {
	if !req.externalSlot {
		return
	}
	botID := req.parentSession.BotID
	delete(p.coord.externalRunning[botID], req)
	if len(p.coord.externalRunning[botID]) == 0 {
		delete(p.coord.externalRunning, botID)
	}
	req.externalSlot = false
}

func (p *SpawnProvider) watchQueuedAgentRequest(key string, req *agentRequest) {
	go func() {
		_, outcome, err := p.bgManager.WaitForSessionTask(req.queueCtx, req.parentSession.BotID, req.parentSession.SessionID, req.taskID, 0)
		if err == nil && outcome != background.WaitKilled {
			return
		}
		req.queueCancel()
		removed := false
		p.coord.mu.Lock()
		if state := p.coord.states[key]; state != nil {
			for i, candidate := range state.queue {
				if candidate == req {
					state.queue = append(state.queue[:i], state.queue[i+1:]...)
					removed = true
					break
				}
			}
		}
		p.coord.mu.Unlock()
		if removed {
			result := rejectedAgentRun(req, req.queueCtx.Err())
			p.bgManager.CompleteAgentTask(req.taskID, background.AgentTaskResult{AgentID: req.agentID, AgentSessionID: req.agentSessionID, Message: req.message, Status: background.TaskFailed, Error: result.Error})
		}
	}()
}

func (p *SpawnProvider) runQueuedAgentRequest(ctx context.Context, key string, req *agentRequest) {
	reject := func(err error) {
		p.recordAdmissionFailure(ctx, req, err)
		p.completeAgentRequest(key, req, rejectedAgentRun(req, err))
	}
	if runtime, ok := p.admitter.(SubagentRuntimeService); ok {
		if err := runtime.WaitSubagentIdle(ctx, req.parentSession, req.agentSessionID); err != nil {
			reject(err)
			return
		}
	}
	if err := ctx.Err(); err != nil {
		reject(err)
		return
	}
	held, unlockExternal, err := p.lockExternalSlots(ctx, req)
	if err == nil {
		p.coord.mu.Lock()
		err = p.reserveExternalSlotLocked(req, held)
		p.coord.mu.Unlock()
		unlockExternal()
	}
	if err != nil {
		reject(err)
		return
	}
	runCtx, ok, err := p.bgManager.MarkAgentTaskRunning(ctx, req.taskID)
	if err != nil || !ok {
		if err == nil {
			err = context.Canceled
		}
		reject(err)
		return
	}
	p.coord.mu.Lock()
	if state := p.coord.states[key]; state != nil {
		state.waiting = false
	}
	p.coord.mu.Unlock()
	p.runAgentRequest(runCtx, key, req)
}

func (p *SpawnProvider) admitAgentRunWhenReady(ctx context.Context, req *agentRequest) (context.Context, func(agentRunResult), error) {
	runtime, observed := p.admitter.(SubagentRuntimeService)
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if observed {
			if err := runtime.WaitSubagentIdle(ctx, req.parentSession, req.agentSessionID); err != nil {
				return nil, nil, err
			}
			record, err := p.findAgent(ctx, req.parentSession, req.agentID)
			if err != nil {
				return nil, nil, err
			}
			if record.SessionID != req.agentSessionID || record.RuntimeType != req.runtimeType || record.BotAgentID != req.botAgentID {
				return nil, nil, errors.New("subagent runtime binding changed")
			}
			if !runtimekind.IsExternal(req.runtimeType) {
				config, err := p.loadSubagentConfig(ctx, record)
				if err != nil {
					return nil, nil, err
				}
				model, err := p.modelResolver(ctx, req.parentSession, config.ModelUUID, config.ModelID, config.ProviderName)
				if err != nil {
					return nil, nil, err
				}
				req.config, req.runtime = config, model
			}
		}
		runCtx, finish, err := p.admitAgentRun(ctx, req)
		if !observed || !errors.Is(err, turn.ErrSessionBusy) {
			return runCtx, finish, err
		}
	}
	return nil, nil, turn.ErrSessionBusy
}
