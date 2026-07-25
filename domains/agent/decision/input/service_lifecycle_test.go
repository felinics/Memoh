package input

import (
	"context"
	"log/slog"
	"testing"
)

type lifecyclePersistence struct {
	cancelInput CancelSessionInput
	cancelRows  []Record
	fenceErr    error
	cancelCall  int
}

func (*lifecyclePersistence) ChannelIdentityExists(context.Context, string) (bool, error) {
	return false, nil
}

func (*lifecyclePersistence) Create(context.Context, CreateRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) Get(context.Context, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) GetRespondable(context.Context, ResolveRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) GetBySessionToolCall(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) GetPendingBySessionShortID(context.Context, string, string, int) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) GetPendingByReplyMessage(context.Context, string, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) GetLatestPendingBySession(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) UpdateInteraction(context.Context, InteractionRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) Submit(context.Context, ResultRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) Cancel(context.Context, ResultRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (p *lifecyclePersistence) CancelPendingBySession(_ context.Context, in CancelSessionInput) ([]Record, error) {
	p.cancelInput = in
	p.cancelCall++
	return p.cancelRows, nil
}

func (*lifecyclePersistence) Fail(context.Context, ResultRecordInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) UpdatePrompt(context.Context, UpdatePromptInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) UpdateAssistantMessage(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) UpdateToolResultMessage(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*lifecyclePersistence) ListPendingBySession(context.Context, string, string) ([]Record, error) {
	return nil, nil
}

func (*lifecyclePersistence) ListBySession(context.Context, string, string) ([]Record, error) {
	return nil, nil
}

func (*lifecyclePersistence) ListBySessionToolCalls(context.Context, string, string, []string) ([]Record, error) {
	return nil, nil
}

func (p *lifecyclePersistence) InInputCreateTransaction(_ context.Context, _, _ string, fn func(Store) error) error {
	return fn(p)
}

func (p *lifecyclePersistence) InInputFenceTransaction(_ context.Context, _, _ string, fn func(Store) error) error {
	if p.fenceErr != nil {
		return p.fenceErr
	}
	return fn(p)
}

func TestCancelPendingForSession(t *testing.T) {
	persistence := &lifecyclePersistence{cancelRows: []Record{{
		ID:    "33333333-3333-3333-3333-333333333333",
		BotID: storeTestBotID, SessionID: storeTestSessionID,
		ToolCallID: "ask-1", ToolName: ToolNameAskUser, Status: StatusCanceled,
	}}}
	svc := NewService(slog.New(slog.DiscardHandler), persistence)
	cancelled, err := svc.CancelPendingForSession(t.Context(), storeTestBotID, storeTestSessionID, "runtime closed")
	if err != nil {
		t.Fatalf("CancelPendingForSession() error = %v", err)
	}
	if len(cancelled) != 1 || cancelled[0].Status != StatusCanceled {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	if persistence.cancelCall != 1 || len(persistence.cancelInput.ResultJSON) == 0 {
		t.Fatalf("cancel input = %#v, calls = %d", persistence.cancelInput, persistence.cancelCall)
	}
	if _, err := svc.CancelPendingForSession(t.Context(), storeTestBotID, "not-a-uuid", "r"); err == nil {
		t.Fatal("malformed session id accepted")
	}
}
