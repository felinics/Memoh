package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/agent/background"
	"github.com/memohai/memoh/internal/agent/turn"
)

// SubagentAdmitter is the durable admission gate a spawned agent's turn passes
// through before it executes.
//
// A subagent occupies a thread exactly like any other turn, so it takes the
// same durable slot on the same terms: the run is recorded before anything
// runs, its owner holds a fencing token, and the returned finish closes the
// record. Declaring the port here rather than importing the runtime keeps the
// dependency pointing the one way it can — the runtime is built on top of the
// agent, which is built on top of these tools.
type SubagentAdmitter interface {
	// AdmitSubagentRun returns the context the run must execute in, the
	// admitted identity, and the terminal write that releases the thread's
	// slot, or an error naming why nothing was started. turn.ErrSessionBusy
	// means the agent is already working; turn.ErrDuplicateTurn means this
	// task already has a run.
	AdmitSubagentRun(ctx context.Context, botID, threadID, invocationID string, submission []byte) (context.Context, SubagentAdmission, func(error), error)
}

// SubagentAdmission names what admission allocated for the run. The turn
// identity matters to persistence: the task's user message must file as that
// turn's request (SR-TURN-001) so history and the live runtime view agree on
// which turn the run writes into — minting a second turn here would split one
// run across two turns in every subscriber's transcript.
type SubagentAdmission struct {
	RunID        string
	TurnID       string
	TurnPosition int64
}

// SetSubagentAdmitter injects the admission gate. Setter injection for the same
// reason as the agent itself: this provider and the runtime that admits its
// runs are wired into one graph and each is reachable from the other's
// dependencies.
func (p *SpawnProvider) SetSubagentAdmitter(admitter SubagentAdmitter) {
	p.admitter = admitter
}

// admitAgentRun claims the agent's thread for this task and returns the run
// context plus the write that ends the run's record. The admitted identity is
// recorded on the request so persistence can file the task's messages under
// the turn admission allocated.
//
// The finish takes the result rather than an error because how a run ended is
// not always visible in one: a killed task carries the cancellation as its
// error text, and recording that as a failure would misname a deliberate stop.
func (p *SpawnProvider) admitAgentRun(ctx context.Context, req *agentRequest) (context.Context, func(agentRunResult), error) {
	if p.admitter == nil {
		return nil, nil, errors.New("session runtime is not available")
	}
	submission, err := subagentSubmission(req)
	if err != nil {
		return nil, nil, fmt.Errorf("encode subagent submission: %w", err)
	}
	runCtx, admission, finish, err := p.admitter.AdmitSubagentRun(
		ctx,
		req.parentSession.BotID,
		req.agentSessionID,
		subagentInvocationID(req.taskID),
		submission,
	)
	if err != nil {
		return nil, nil, err
	}
	req.admission = admission
	return runCtx, func(result agentRunResult) {
		if result.Status == string(background.TaskKilled) {
			// A kill cancels the run's context, which is what the terminal write
			// reads to record an abort. Passing the cancellation as an error
			// instead would file a deliberate stop as a failure.
			finish(nil)
			return
		}
		if strings.TrimSpace(result.Error) != "" {
			finish(errors.New(result.Error))
			return
		}
		finish(nil)
	}, nil
}

// subagentInvocationID is the task's retry identity. The background task id is
// minted once per submitted message and outlives the internal retry loop, so
// every attempt at one task is the same invocation rather than a new turn.
func subagentInvocationID(taskID string) string {
	return "subagent:" + strings.TrimSpace(taskID)
}

// subagentSubmission encodes what was submitted, and only that. Its fingerprint
// decides whether a repeated invocation is the same submission or a conflict,
// so it carries nothing that varies between two attempts at one task.
func subagentSubmission(req *agentRequest) ([]byte, error) {
	return json.Marshal(struct {
		BotID    string `json:"bot_id"`
		ThreadID string `json:"thread_id"`
		AgentID  string `json:"agent_id"`
		Message  string `json:"message"`
	}{
		BotID:    strings.TrimSpace(req.parentSession.BotID),
		ThreadID: strings.TrimSpace(req.agentSessionID),
		AgentID:  strings.TrimSpace(req.agentID),
		Message:  req.message,
	})
}

// rejectedAgentRun describes a task that never started, in the shape the
// background record and the parent model both read.
func rejectedAgentRun(req *agentRequest, cause error) agentRunResult {
	return agentRunResult{
		AgentID:   req.agentID,
		SessionID: req.agentSessionID,
		TaskID:    req.taskID,
		ModelID:   req.config.ModelID,
		Provider:  req.config.ProviderName,
		Fork:      req.config.Forked,
		Message:   req.message,
		Error:     subagentAdmissionMessage(cause),
	}
}

// subagentAdmissionMessage turns a refusal into something the parent model can
// act on. Busy is the one worth naming: the agent is working, and sending the
// message again after it reports back is the whole remedy.
func subagentAdmissionMessage(cause error) string {
	switch {
	case errors.Is(cause, turn.ErrSessionBusy):
		return "agent is already running a turn; wait for it to report back, then send the message again"
	case errors.Is(cause, turn.ErrDuplicateTurn):
		return "this task was already started; use get_background_status(task_id) to read its result"
	default:
		return cause.Error()
	}
}
