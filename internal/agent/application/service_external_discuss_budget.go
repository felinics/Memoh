package application

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

func (s *Service) prepareExternalDiscussContext(ctx context.Context, req ChatRequest, markdown string, imageCount int) (ChatRequest, error) {
	if req.discussMessages == nil {
		return req, nil
	}
	if err := ctx.Err(); err != nil {
		return req, err
	}
	admitted, admission := admitDiscussAgentContext(req.discussMessages, s.contextAbsoluteMaxTokens(), len(markdown)+len(req.discussPromptPrefix)+len(req.discussPromptSuffix), imageCount)
	if admission.ProtectedOverflow || admission.DroppedMessages > 0 || len(req.discussOmittedSources) > 0 || max(0, req.discussContextTokens-admission.ProtectedTokens) > admission.RecoveryBudgetTokens {
		if admission.RecoveryBudgetTokens <= 0 {
			return req, apperror.New(apperror.CodeContextProtectedOverflow, nil)
		}
		cause := errors.New("context recovery budget exhausted")
		if !req.discussRecoveryExhausted {
			recovery := req
			recovery.discussCurrentSources = admission.ProtectedSources
			cause = s.compactForBudget(ctx, recovery, max(req.discussContextTokens, admission.EstimatedTokens), admission.RecoveryBudgetTokens, "")
			if err := ctx.Err(); err != nil {
				return req, err
			}
			if cause == nil {
				return req, native.ErrContextRecompose
			}
		}
		return req, apperror.Wrap(budgetFailureCode(cause), cause, nil)
	}
	req.Query = req.discussPromptPrefix + discussAgentFullContextPrompt(admitted) + req.discussPromptSuffix
	req.RawQuery = req.Query
	req.discussMessages = admitted
	req.discussOmittedSources = append(append([]turn.ContextMessageSource(nil), req.discussOmittedSources...), admission.OmittedSources...)
	return req, nil
}
