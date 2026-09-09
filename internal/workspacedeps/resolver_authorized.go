package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/background"
)

// ValidateOperationSession ensures optional result delivery cannot cross bots.
// The HTTP entry point separately requires Manage permission and a confirmed
// definition revision. A missing validator must never accept a routing target.
func (s *Service) ValidateOperationSession(ctx context.Context, botID, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if s.operationSessionValidator == nil {
		return errors.New("dependency operation session validation is unavailable")
	}
	return s.operationSessionValidator(ctx, botID, sessionID)
}

// RunAuthorizedOperation tracks an explicitly confirmed management operation.
// The caller has authorized Manage access and pinned the script preview revision;
// this method preserves that context rather than resolving a newer definition.
// Only its optional, validated origin session receives lifecycle messages.
func (s *Service) RunAuthorizedOperation(ctx context.Context, botID, targetID, depID, sessionID, description string, run func(context.Context, LogSink) (OperationResult, error), sink LogSink) (OperationResult, error) {
	if err := s.ValidateOperationSession(ctx, botID, sessionID); err != nil {
		return OperationResult{}, err
	}
	if run == nil {
		return OperationResult{}, errors.New("dependency operation is required")
	}
	if revision, _ := ctx.Value(revisionContextKey{}).(string); strings.TrimSpace(revision) == "" {
		return OperationResult{}, errors.New("dependency operation requires a confirmed definition revision")
	}
	key := InstallationKey{BotID: botID, WorkspaceTargetID: normalizeTargetID(targetID), DependencyID: strings.TrimSpace(depID)}
	reserved, release, err := s.reserveOperation(ctx, key)
	if err != nil {
		return OperationResult{}, err
	}
	defer release()
	ctx = reserved
	if s.background == nil {
		return run(ctx, sink)
	}
	type outcome struct {
		result OperationResult
		err    error
	}
	done := make(chan outcome, 1)
	registered := make(chan struct{})
	var taskID string
	taskID = s.background.SpawnManaged(ctx, botID, sessionID, description, func(runCtx context.Context, log func(string, string)) (runErr error) {
		<-registered
		result := OperationResult{}
		defer func() {
			if recovered := recover(); recovered != nil {
				runErr = fmt.Errorf("dependency operation panicked: %v", recovered)
			}
			if errors.Is(runErr, ErrOperationUncertain) {
				runErr = errors.Join(runErr, background.ErrManagedOutcomeUnknown)
			}
			s.resolverMu.Lock()
			if s.installs[key] == taskID {
				delete(s.installs, key)
			}
			s.resolverMu.Unlock()
			done <- outcome{result: result, err: runErr}
		}()
		result, runErr = run(runCtx, LogFunc(func(stream, line string) {
			log(stream, line+"\n")
			if sink != nil {
				sink.Log(stream, line)
			}
		}))
		return runErr
	})
	s.resolverMu.Lock()
	s.installs[key] = taskID
	s.resolverMu.Unlock()
	close(registered)
	result := <-done
	return result.result, result.err
}
