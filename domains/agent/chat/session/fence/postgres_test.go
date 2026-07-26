package fence

import (
	"context"
	"errors"
	"testing"
)

const (
	testBotID     = "11111111-1111-1111-1111-111111111111"
	testSessionID = "22222222-2222-2222-2222-222222222222"
)

type testPersistence struct {
	order              []string
	current            int64
	lockErr            error
	activated          int64
	claimedApproval    string
	claimedInput       string
	preservedApproval  string
	preservedInput     string
	transactionStarted bool
}

func (p *testPersistence) InRuntimeFenceTransaction(_ context.Context, fn func(Store) error) error {
	p.transactionStarted = true
	return fn(p)
}

func (p *testPersistence) LockBot(context.Context, string) error {
	p.order = append(p.order, "bot")
	return nil
}

func (p *testPersistence) LockForActivation(context.Context, Fence) (int64, error) {
	p.order = append(p.order, "session")
	return p.current, p.lockErr
}

func (p *testPersistence) Activate(_ context.Context, fence Fence) (int64, error) {
	p.activated = fence.Token
	return p.activated, nil
}

func (p *testPersistence) ClaimToolApproval(_ context.Context, id string, _ Fence) error {
	p.claimedApproval = id
	return nil
}

func (p *testPersistence) ClaimUserInput(_ context.Context, id string, _ Fence) error {
	p.claimedInput = id
	return nil
}

func (p *testPersistence) SupersedeToolApprovals(_ context.Context, _ Fence, preserveID, _ string) error {
	p.preservedApproval = preserveID
	return nil
}

func (p *testPersistence) SupersedeUserInputs(_ context.Context, _ Fence, preserveID string, _ []byte) error {
	p.preservedInput = preserveID
	return nil
}

func (p *testPersistence) Lock(context.Context, Fence) error {
	p.order = append(p.order, "fence")
	return p.lockErr
}

func TestInTransactionRequiresRealTransactor(t *testing.T) {
	ctx := WithContext(t.Context(), Fence{BotID: testBotID, SessionID: testSessionID, Token: 1})
	if err := InTransaction(ctx, nil, testBotID, testSessionID, func(Store) error { return nil }); !errors.Is(err, ErrTransactionsUnsupported) {
		t.Fatalf("InTransaction() error = %v", err)
	}
}

func TestInTransactionLocksBotBeforeFence(t *testing.T) {
	persistence := &testPersistence{}
	ctx := WithContext(t.Context(), Fence{BotID: testBotID, SessionID: testSessionID, Token: 1})
	called := false
	err := InTransaction(ctx, persistence, testBotID, testSessionID, func(Store) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("InTransaction() error = %v", err)
	}
	if !called || len(persistence.order) != 2 || persistence.order[0] != "bot" || persistence.order[1] != "fence" {
		t.Fatalf("order = %#v, called = %v", persistence.order, called)
	}
}

func TestInTransactionMapsMissingFenceToStale(t *testing.T) {
	persistence := &testPersistence{lockErr: ErrRecordNotFound}
	ctx := WithContext(t.Context(), Fence{BotID: testBotID, SessionID: testSessionID, Token: 1})
	err := InTransaction(ctx, persistence, testBotID, testSessionID, func(Store) error {
		t.Fatal("mutation callback called")
		return nil
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("InTransaction() error = %v", err)
	}
}

func TestActivatePreservesSelectedDecision(t *testing.T) {
	tests := []struct {
		name string
		kind string
	}{
		{name: "approval", kind: DecisionToolApproval},
		{name: "input", kind: DecisionUserInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persistence := &testPersistence{current: 1}
			preservedID := "33333333-3333-3333-3333-333333333333"
			err := ActivateWithOptions(t.Context(), persistence, Fence{
				BotID: testBotID, SessionID: testSessionID, Token: 2,
			}, ActivationOptions{PreserveDecision: &PreservedDecision{Kind: tt.kind, ID: preservedID}})
			if err != nil {
				t.Fatalf("ActivateWithOptions() error = %v", err)
			}
			if tt.kind == DecisionToolApproval {
				if persistence.claimedApproval != preservedID || persistence.preservedApproval != preservedID {
					t.Fatalf("approval preserve = %#v", persistence)
				}
			} else if persistence.claimedInput != preservedID || persistence.preservedInput != preservedID {
				t.Fatalf("input preserve = %#v", persistence)
			}
			if persistence.activated != 2 || persistence.order[0] != "bot" || persistence.order[1] != "session" {
				t.Fatalf("activation = %#v", persistence)
			}
		})
	}
}
