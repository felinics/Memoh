package input

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Structured ask_user gestures from channels with native controls (inline
// buttons). They drive the same durable TextInteractionState as plain-text
// replies (AdvanceText) — one state machine, two input surfaces — so an
// interaction survives process restarts regardless of which surface it uses.
type InteractionOpKind string

const (
	// OpSelectOption answers a single_select question and auto-advances;
	// re-selecting the current choice clears it (toggle-off).
	OpSelectOption InteractionOpKind = "select"
	// OpToggleOption flips one multi_select option; never advances.
	OpToggleOption InteractionOpKind = "toggle"
	// OpSetText records a verbatim free-text answer (text question or
	// allow_custom). Unlike AdvanceText it never option-matches the input:
	// the user explicitly chose to type, so the text is taken as-is.
	OpSetText InteractionOpKind = "set_text"
	// OpNavigate moves the question cursor without touching answers.
	OpNavigate InteractionOpKind = "navigate"
	// OpSubmit completes the request, marking unanswered questions skipped.
	OpSubmit InteractionOpKind = "submit"
)

// InteractionReject explains why an op did not apply. Rejections are
// user-correctable (wrong button state, empty text) — not errors.
type InteractionReject string

const (
	RejectNone             InteractionReject = ""
	RejectInvalidOp        InteractionReject = "invalid_op"
	RejectCustomNotAllowed InteractionReject = "custom_not_allowed"
	RejectEmptyText        InteractionReject = "empty_text"
)

type InteractionOp struct {
	Kind InteractionOpKind
	// QuestionIndex/OptionIndex address buttons for select/toggle; indexes
	// are stable because UIPayload question/option order never changes after
	// CreatePending.
	QuestionIndex int
	OptionIndex   int
	// Page is the navigate target.
	Page int
	// QuestionID binds set_text to the question that requested free text,
	// not to the current cursor — navigation may have moved since the
	// prompt was issued.
	QuestionID string
	Text       string
}

type AdvanceInteractionInput struct {
	BotID     string
	RequestID string
	Op        InteractionOp
}

type AdvanceInteractionResult struct {
	// Handled is false when the request no longer exists or is not pending
	// (already answered, canceled, expired) — callers should tell the user
	// the interaction ended rather than surface an error.
	Handled bool
	// Changed reports whether state was persisted; unchanged ops (same-page
	// nav, replay after completion) need no card re-render.
	Changed bool
	Reject  InteractionReject
	Request Request
}

// AdvanceInteraction applies one structured op to the durable interaction
// state under the same optimistic-lock CAS as AdvanceText: each attempt
// re-reads the row and re-applies the op, so a concurrent writer never makes
// a stale gesture win.
func (s *Service) AdvanceInteraction(ctx context.Context, input AdvanceInteractionInput) (AdvanceInteractionResult, error) {
	if s == nil || s.queries == nil {
		return AdvanceInteractionResult{}, errors.New("user input queries not configured")
	}
	resolve := ResolveInput{
		BotID:      input.BotID,
		ExplicitID: strings.TrimSpace(input.RequestID),
	}
	if resolve.ExplicitID == "" {
		return AdvanceInteractionResult{}, errors.New("user input request id is required")
	}
	for attempt := 0; attempt < maxTextInteractionRetries; attempt++ {
		req, err := s.ResolveTarget(ctx, resolve)
		if errors.Is(err, ErrNotFound) {
			return AdvanceInteractionResult{Handled: false}, nil
		}
		if err != nil {
			return AdvanceInteractionResult{}, err
		}
		state, outcome := ApplyInteractionOp(req.UIPayload, req.Interaction, input.Op)
		if outcome.Reject != RejectNone || !outcome.Changed {
			req.Interaction = state
			return AdvanceInteractionResult{Handled: true, Reject: outcome.Reject, Request: req}, nil
		}
		updated, ok, err := s.persistInteraction(ctx, req, state)
		if err != nil {
			return AdvanceInteractionResult{}, err
		}
		if ok {
			return AdvanceInteractionResult{Handled: true, Changed: true, Request: updated}, nil
		}
	}
	return AdvanceInteractionResult{}, errors.New("user input changed concurrently; retry the action")
}

// persistInteraction CAS-writes state at req's revision. ok=false means the
// row moved underneath us (revision bumped, request resolved, or expired) and
// the caller should re-resolve and re-apply its input.
func (s *Service) persistInteraction(ctx context.Context, req Request, state TextInteractionState) (Request, bool, error) {
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return Request{}, false, err
	}
	id, err := db.ParseUUID(req.ID)
	if err != nil {
		return Request{}, false, err
	}
	row, err := s.queries.UpdateUserInputInteraction(ctx, sqlc.UpdateUserInputInteractionParams{
		InteractionJson:     stateJSON,
		ID:                  id,
		InteractionRevision: int32(req.InteractionRevision), //nolint:gosec // revisions cannot approach int32 during one request.
	})
	if err == nil {
		return requestFromRow(row), true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, false, nil
	}
	return Request{}, false, err
}

type InteractionOutcome struct {
	Changed bool
	Reject  InteractionReject
}

// ApplyInteractionOp is the pure transition function for button-driven input.
// It mirrors the plain-text transitions in advanceTextState where the
// semantics overlap (advance-on-answer, complete-on-last, skip fill).
func ApplyInteractionOp(payload UIPayload, state TextInteractionState, op InteractionOp) (TextInteractionState, InteractionOutcome) {
	if len(payload.Questions) == 0 {
		return state, InteractionOutcome{Reject: RejectInvalidOp}
	}
	state = normalizeTextInteraction(payload, state)
	if state.Completed {
		// Crash window: the final answer set was persisted but submission
		// did not run. Report unchanged so the caller re-drives submission
		// instead of mutating an already-complete answer set.
		return state, InteractionOutcome{}
	}
	switch op.Kind {
	case OpNavigate:
		if op.Page < 0 || op.Page >= len(payload.Questions) {
			return state, InteractionOutcome{Reject: RejectInvalidOp}
		}
		if state.QuestionIndex == op.Page {
			return state, InteractionOutcome{}
		}
		state.QuestionIndex = op.Page
		return state, InteractionOutcome{Changed: true}
	case OpSelectOption:
		return applySelectOption(payload, state, op)
	case OpToggleOption:
		return applyToggleOption(payload, state, op)
	case OpSetText:
		return applySetText(payload, state, op)
	case OpSubmit:
		state = completeOrFocusRequired(payload, state)
		return state, InteractionOutcome{Changed: true}
	default:
		return state, InteractionOutcome{Reject: RejectInvalidOp}
	}
}

func applySelectOption(payload UIPayload, state TextInteractionState, op InteractionOp) (TextInteractionState, InteractionOutcome) {
	question, ok := interactionQuestionAt(payload, op.QuestionIndex)
	if !ok || op.OptionIndex < 0 || op.OptionIndex >= len(question.Options) {
		return state, InteractionOutcome{Reject: RejectInvalidOp}
	}
	// Realign the cursor to the tapped question: a late callback may arrive
	// after navigation moved elsewhere.
	state.QuestionIndex = op.QuestionIndex
	optionID := question.Options[op.OptionIndex].ID
	answer, answered := state.Answer(question.ID)
	if answered && len(answer.OptionIDs) == 1 && answer.OptionIDs[0] == optionID && answer.CustomText == "" {
		// Re-tapping the current choice clears it; the user stays to pick
		// again or skip.
		state.Answers = removeTextAnswer(state.Answers, question.ID)
		return state, InteractionOutcome{Changed: true}
	}
	state.Answers = putTextAnswer(state.Answers, QuestionAnswer{QuestionID: question.ID, OptionIDs: []string{optionID}})
	if op.QuestionIndex < len(payload.Questions)-1 {
		state.QuestionIndex = op.QuestionIndex + 1
	} else {
		// Answering the last question completes the set; earlier questions
		// deliberately left blank become skips unless ACP marked one required.
		state = completeOrFocusRequired(payload, state)
	}
	return state, InteractionOutcome{Changed: true}
}

func applyToggleOption(payload UIPayload, state TextInteractionState, op InteractionOp) (TextInteractionState, InteractionOutcome) {
	question, ok := interactionQuestionAt(payload, op.QuestionIndex)
	if !ok || op.OptionIndex < 0 || op.OptionIndex >= len(question.Options) {
		return state, InteractionOutcome{Reject: RejectInvalidOp}
	}
	state.QuestionIndex = op.QuestionIndex
	answer, _ := state.Answer(question.ID)
	answer.QuestionID = question.ID
	answer.Skipped = false
	if question.CustomExclusive {
		answer.CustomText = ""
	}
	answer.OptionIDs = toggleOptionID(answer.OptionIDs, question.Options[op.OptionIndex].ID)
	if len(answer.OptionIDs) == 0 && strings.TrimSpace(answer.CustomText) == "" {
		state.Answers = removeTextAnswer(state.Answers, question.ID)
	} else {
		state.Answers = putTextAnswer(state.Answers, answer)
	}
	return state, InteractionOutcome{Changed: true}
}

func applySetText(payload UIPayload, state TextInteractionState, op InteractionOp) (TextInteractionState, InteractionOutcome) {
	text := strings.TrimSpace(op.Text)
	if text == "" {
		return state, InteractionOutcome{Reject: RejectEmptyText}
	}
	questionIndex := -1
	for idx, question := range payload.Questions {
		if question.ID == strings.TrimSpace(op.QuestionID) {
			questionIndex = idx
			break
		}
	}
	if questionIndex < 0 {
		return state, InteractionOutcome{Reject: RejectInvalidOp}
	}
	question := payload.Questions[questionIndex]
	state.QuestionIndex = questionIndex
	switch question.Kind {
	case QuestionKindText:
		state.Answers = putTextAnswer(state.Answers, QuestionAnswer{QuestionID: question.ID, Text: text})
	case QuestionKindSingleSelect:
		if !question.AllowCustom {
			return state, InteractionOutcome{Reject: RejectCustomNotAllowed}
		}
		state.Answers = putTextAnswer(state.Answers, QuestionAnswer{QuestionID: question.ID, CustomText: text})
	case QuestionKindMultiSelect:
		if !question.AllowCustom {
			return state, InteractionOutcome{Reject: RejectCustomNotAllowed}
		}
		// Custom text normally joins toggled options. ACP can explicitly mark
		// the companion field exclusive, in which case it replaces them.
		answer, _ := state.Answer(question.ID)
		answer.QuestionID = question.ID
		answer.Skipped = false
		if question.CustomExclusive {
			answer.OptionIDs = nil
		}
		answer.CustomText = text
		state.Answers = putTextAnswer(state.Answers, answer)
		return state, InteractionOutcome{Changed: true}
	default:
		state.Answers = putTextAnswer(state.Answers, QuestionAnswer{QuestionID: question.ID, Text: text})
	}
	if questionIndex < len(payload.Questions)-1 {
		state.QuestionIndex = questionIndex + 1
	} else {
		state = completeOrFocusRequired(payload, state)
	}
	return state, InteractionOutcome{Changed: true}
}

func removeTextAnswer(answers []QuestionAnswer, questionID string) []QuestionAnswer {
	out := make([]QuestionAnswer, 0, len(answers))
	for _, answer := range answers {
		if answer.QuestionID == questionID {
			continue
		}
		out = append(out, answer)
	}
	return out
}

func toggleOptionID(ids []string, target string) []string {
	out := make([]string, 0, len(ids)+1)
	found := false
	for _, id := range ids {
		if id == target {
			found = true
			continue
		}
		out = append(out, id)
	}
	if !found {
		out = append(out, target)
	}
	return out
}

func interactionQuestionAt(payload UIPayload, index int) (UIQuestion, bool) {
	if index < 0 || index >= len(payload.Questions) {
		return UIQuestion{}, false
	}
	return payload.Questions[index], true
}
