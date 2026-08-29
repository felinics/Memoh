package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// queueStepTransaction is the application adapter between a completed native
// model step and the durable queue coordinator transaction. It owns history
// persistence inside that transaction and the delivery of any resulting
// claimed input; it does not own the native model loop itself.
type queueStepTransaction struct {
	service              *Service
	req                  ChatRequest
	coordinator          sessionqueue.Coordinator
	txPersister          messagepkg.AgentStepTxPersister
	replacementPersister messagepkg.AgentReplacementTxPersister
	modelID              string
	run                  sessionruntime.RunHandle
	pendingSteer         *sessionqueue.SteerClaimRef
	pendingFollowUp      *sessionqueue.FollowUpClaimRef
	// A newly claimed steer is not durable history yet. It becomes history only
	// when the following step applies the claim in the coordinator transaction.
	pendingSteerNeedsPersistence bool
}

type queueStepOutcome struct {
	persisted            []messagepkg.Message
	result               sessionqueue.CommitStepResult
	appliedSteerItemID   string
	claimedSteer         *sessionqueue.SteerItem
	replacementFinalized bool
}

func newQueueStepTransaction(s *Service, req ChatRequest, modelID string) *queueStepTransaction {
	if s == nil || s.queueCoordinator == nil || s.messageService == nil {
		return nil
	}
	txPersister, ok := s.messageService.(messagepkg.AgentStepTxPersister)
	if !ok {
		return nil
	}
	replacementPersister, _ := s.messageService.(messagepkg.AgentReplacementTxPersister)
	if req.TurnReplacement != nil && replacementPersister == nil {
		return nil
	}
	return &queueStepTransaction{
		service:                      s,
		req:                          req,
		coordinator:                  s.queueCoordinator,
		txPersister:                  txPersister,
		replacementPersister:         replacementPersister,
		modelID:                      modelID,
		run:                          req.RunHandle,
		pendingSteer:                 req.QueueSteerClaim,
		pendingFollowUp:              req.QueueFollowUpClaim,
		pendingSteerNeedsPersistence: req.QueueSteerClaim != nil,
	}
}

func (q *queueStepTransaction) commit(
	ctx context.Context,
	stepIndex int,
	commitHash string,
	kind sessionqueue.StepKind,
	agentStep messagepkg.AgentStep,
	previouslyPersisted []messagepkg.Message,
) (queueStepOutcome, error) {
	var outcome queueStepOutcome
	if q == nil {
		return outcome, errors.New("queue step transaction is unavailable")
	}
	needsAppliedSteerPersistence := q.pendingSteerNeedsPersistence
	if needsAppliedSteerPersistence && q.pendingSteer != nil {
		outcome.appliedSteerItemID = string(q.pendingSteer.ItemID)
	}
	result, err := q.coordinator.CommitStep(ctx, sessionqueue.CommitStepRequest{
		Run: q.run, StepIndex: int64(stepIndex), CommitHash: commitHash, Kind: kind,
		Persist: func(txCtx context.Context, queries dbstore.Queries) error {
			if len(agentStep.Messages) == 0 {
				return nil
			}
			var persistErr error
			if q.req.TurnReplacement != nil {
				var persisted []messagepkg.Message
				persisted, persistErr = q.replacementPersister.PersistAgentReplacementStepTx(txCtx, queries, agentStep)
				outcome.persisted = append(outcome.persisted, persisted...)
			} else {
				var persisted []messagepkg.Message
				persisted, persistErr = q.txPersister.PersistAgentStepTx(txCtx, queries, agentStep)
				outcome.persisted = append(outcome.persisted, persisted...)
			}
			return persistErr
		},
		FinalizeHistory: func(txCtx context.Context, queries dbstore.Queries) error {
			if q.req.TurnReplacement == nil {
				return nil
			}
			allPersisted := make([]messagepkg.Message, 0, len(previouslyPersisted)+len(outcome.persisted))
			allPersisted = append(allPersisted, previouslyPersisted...)
			allPersisted = append(allPersisted, outcome.persisted...)
			requestMessageID := ""
			if q.req.TurnReplacement != nil {
				requestMessageID = strings.TrimSpace(q.req.TurnReplacement.RequestMessageID)
			}
			if requestMessageID == "" {
				// Synthetic steer user turns may have been applied by an earlier
				// step. Replacement finalization must still anchor to the first
				// request user, never to the latest steer input.
				requestMessageID = firstUserID(allPersisted)
			}
			assistantMessageID := firstAssistantID(allPersisted)
			if assistantMessageID == "" {
				return errors.New("replacement assistant message was not persisted")
			}
			return q.replacementPersister.FinalizeAgentReplacementTx(
				txCtx, queries, q.req.ThreadID, *q.req.TurnReplacement, requestMessageID, assistantMessageID,
			)
		},
		PersistAppliedSteer: func(txCtx context.Context, queries dbstore.Queries, item sessionqueue.SteerItem) error {
			// A claim is only an execution capability. Persist the synthetic user
			// turn when that capability is applied, atomically with the queue CAS.
			// This also covers tool-loop injections: the provider saw the message
			// at PrepareStep, while this callback gives it durable history exactly
			// once.
			if !needsAppliedSteerPersistence {
				return nil
			}
			steerReq := q.req
			steerReq.Query = ""
			steerReq.RawQuery = ""
			steerReq.UserVisibleText = ""
			steerReq.ExternalMessageID = ""
			steerReq.EventID = ""
			steerReq.TurnID = ""
			steerReq.TurnPosition = nil
			steerReq.UserMessagePersisted = false
			steerReq.PersistedUserMessageID = ""
			steerReq.ReusePersistedUserMessage = false
			steerReq.SkipHistoryTurn = false
			text := continuationPayloadText(item.Payload)
			if text == "" {
				return nil
			}
			// Tool-boundary steers are captured by native PrepareStep and
			// decorated onto the following StepResult. That message is already
			// persisted by the normal step callback. Only a final-boundary
			// steer reopened through NextModelInputs needs a synthetic history
			// row; writing both paths creates duplicate user turns.
			if containsUserText(agentStep.Messages, text) {
				return nil
			}
			inputs, buildErr := q.service.buildPersistInputs(txCtx, steerReq, []ModelMessage{{Role: "user", Content: newTextContent(text)}}, q.modelID, storeRoundOptions{})
			if buildErr != nil {
				return buildErr
			}
			persisted, persistErr := q.txPersister.PersistAgentStepTx(txCtx, queries, messagepkg.AgentStep{RunID: q.req.RunID, Messages: inputs})
			if persistErr == nil {
				outcome.persisted = append(outcome.persisted, persisted...)
			}
			return persistErr
		},
		Steer: q.pendingSteer, FollowUp: q.pendingFollowUp,
	})
	if err != nil {
		return outcome, err
	}

	// Claim references are single-use capabilities. CommitStep consumes the
	// supplied references before it optionally returns a newly claimed item.
	q.pendingSteer = result.SteerClaim
	q.pendingFollowUp = result.FollowUpClaim
	if needsAppliedSteerPersistence {
		q.pendingSteerNeedsPersistence = false
	}
	if result.SteerClaim != nil && result.Steer != nil {
		q.pendingSteerNeedsPersistence = true
	}
	outcome.result = result
	if result.SteerClaim != nil && result.Steer != nil {
		claimed := *result.Steer
		outcome.claimedSteer = &claimed
	}

	// A final boundary is restarted with NextModelInputs. Sending the same
	// claim through InjectCh as well would admit it twice on the continuation.
	if kind != sessionqueue.StepFinal && result.SteerClaim != nil && q.req.QueueInjectCh != nil && result.Steer != nil {
		text := continuationPayloadText(result.Steer.Payload)
		select {
		case q.req.QueueInjectCh <- turn.InjectMessage{Text: text, HeaderifiedText: text}:
		default:
			return outcome, errors.New("steer queue injection channel is full")
		}
	}
	if result.Action == sessionqueue.StartContinuation {
		q.service.startQueueContinuation(ctx, result)
	}
	if q.req.TurnReplacement != nil &&
		(result.Action == sessionqueue.StartContinuation || result.Action == sessionqueue.StopCurrent) {
		outcome.replacementFinalized = true
	}
	return outcome, nil
}

func containsUserText(messages []messagepkg.PersistInput, text string) bool {
	want := strings.TrimSpace(text)
	if want == "" {
		return false
	}
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			continue
		}
		got := ""
		var encoded ModelMessage
		if err := json.Unmarshal(message.Content, &encoded); err == nil {
			got = encoded.TextContent()
		} else {
			got = (ModelMessage{Content: message.Content}).TextContent()
		}
		if strings.TrimSpace(got) == want {
			return true
		}
	}
	return false
}
