package approval

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeFlowService struct {
	evaluation   Evaluation
	evalErr      error
	created      Request
	createErr    error
	decided      Request
	waitErr      error
	waitBlocks   bool
	rejectCalls  []string
	rejectResult Request
	rejectErr    error
	getResult    Request
	getErr       error
	getCalls     int
	createCalls  int
	createdInput CreatePendingInput
	waitCalls    int
	waiters      int
}

func (f *fakeFlowService) EvaluatePolicy(context.Context, CreatePendingInput) (Evaluation, error) {
	return f.evaluation, f.evalErr
}

func (f *fakeFlowService) CreatePending(_ context.Context, input CreatePendingInput) (Request, error) {
	f.createCalls++
	f.createdInput = input
	if f.createErr != nil {
		return Request{}, f.createErr
	}
	req := f.created
	if req.ID == "" {
		req.ID = "approval-1"
	}
	req.ToolCallID = input.ToolCallID
	req.ToolName = input.ToolName
	req.ExecutionLocation = input.ExecutionLocation
	if req.Status == "" {
		req.Status = StatusPending
	}
	return req, f.createErr
}

func (f *fakeFlowService) Reject(_ context.Context, approvalID, _, reason string) (Request, error) {
	f.rejectCalls = append(f.rejectCalls, approvalID+":"+reason)
	if f.rejectErr != nil {
		return Request{}, f.rejectErr
	}
	rejected := f.rejectResult
	rejected.Status = StatusRejected
	if rejected.DecisionReason == "" {
		rejected.DecisionReason = reason
	}
	return rejected, nil
}

func (f *fakeFlowService) Get(context.Context, string) (Request, error) {
	f.getCalls++
	if f.getErr != nil {
		return Request{}, f.getErr
	}
	return f.getResult, nil
}

func (f *fakeFlowService) WaitForDecision(ctx context.Context, _ string) (Request, error) {
	f.waitCalls++
	if f.waitBlocks {
		<-ctx.Done()
		return Request{}, ctx.Err()
	}
	return f.decided, f.waitErr
}

func (f *fakeFlowService) RegisterWaiter(string) func() {
	f.waiters++
	return func() {
		f.waiters--
	}
}

func flowInput() FlowRequest {
	return FlowRequest{
		Input: CreatePendingInput{
			BotID:                        "bot-1",
			SessionID:                    "session-1",
			ToolCallID:                   "call-1",
			ToolName:                     "exec",
			RequestedByChannelIdentityID: "channel-1",
		},
		Interactive: true,
	}
}

func flowInputFor(svc *fakeFlowService) FlowRequest {
	flow := flowInput()
	flow.RegisterWaiter = svc.RegisterWaiter
	return flow
}

func TestRunFlowPolicyBypass(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{evaluation: Evaluation{Decision: DecisionBypass}}
	flow := flowInputFor(svc)
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || !result.Approved {
		t.Fatalf("RunFlow() = %+v, %v; want approved", result, err)
	}
}

func TestRunFlowPolicyDenyDoesNotCreatePendingRequest(t *testing.T) {
	t.Parallel()

	svc := &fakeFlowService{evaluation: Evaluation{Decision: DecisionDeny}}
	flow := flowInputFor(svc)
	emitted := false
	flow.Emit = func(Request) bool {
		emitted = true
		return true
	}
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil {
		t.Fatalf("RunFlow() error = %v", err)
	}
	if result.Approved || result.Status != StatusRejected || result.DecisionReason != PolicyDeniedReason {
		t.Fatalf("RunFlow() = %+v, want policy rejection", result)
	}
	if svc.createCalls != 0 || svc.waitCalls != 0 || emitted {
		t.Fatalf("deny created approval flow: createCalls=%d waitCalls=%d emitted=%v", svc.createCalls, svc.waitCalls, emitted)
	}
}

func TestRunFlowNonInteractiveAutoRejects(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{evaluation: Evaluation{Decision: DecisionNeedsApproval}}
	flow := flowInputFor(svc)
	flow.Interactive = false
	flow.NonInteractiveReason = "no stream"
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || result.Approved {
		t.Fatalf("RunFlow() = %+v, %v; want rejected", result, err)
	}
	if result.Status != StatusRejected || result.DecisionReason != "no stream" || result.DecidedByUser {
		t.Fatalf("result = %+v, want system rejection with reason", result)
	}
	if len(svc.rejectCalls) != 1 {
		t.Fatalf("reject calls = %v, want 1", svc.rejectCalls)
	}
}

func TestRunFlowApprovedEmitsPendingAndDecision(t *testing.T) {
	t.Parallel()
	location := &ExecutionLocation{
		TargetID: "target-1",
		Kind:     "remote",
		Name:     "Office Mac",
	}
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval, ExecutionLocation: location},
		decided:    Request{ID: "approval-1", Status: StatusApproved, DecidedByUser: true},
	}
	var emitted []Request
	flow := flowInputFor(svc)
	flow.Emit = func(req Request) bool {
		if req.Status == StatusPending && svc.waiters == 0 {
			t.Fatal("pending approval emitted before waiter was registered")
		}
		emitted = append(emitted, req)
		return true
	}
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || !result.Approved || !result.DecidedByUser {
		t.Fatalf("RunFlow() = %+v, %v; want approved by user", result, err)
	}
	if len(emitted) != 2 || emitted[0].Status != StatusPending || emitted[1].Status != StatusApproved {
		t.Fatalf("emitted = %+v, want pending then approved", emitted)
	}
	if emitted[1].ToolCallID != "call-1" || emitted[1].ToolName != "exec" {
		t.Fatalf("decision snapshot lost request identity: %+v", emitted[1])
	}
	if svc.createdInput.ExecutionLocation != location || emitted[0].ExecutionLocation != location || emitted[1].ExecutionLocation != location {
		t.Fatalf("execution location was not preserved: input=%+v emitted=%+v", svc.createdInput.ExecutionLocation, emitted)
	}
	if svc.waiters != 0 {
		t.Fatalf("waiter count after RunFlow = %d, want released", svc.waiters)
	}
}

func TestRunFlowRejectsReusedTerminalRequest(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		created:    Request{ID: "approval-1", Status: StatusApproved},
	}
	_, err := RunFlow(context.Background(), svc, flowInputFor(svc))
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("RunFlow() error = %v, want ErrAlreadyDecided", err)
	}
	if svc.waitCalls != 0 || len(svc.rejectCalls) != 0 {
		t.Fatalf("flow used terminal request: waitCalls=%d rejectCalls=%v", svc.waitCalls, svc.rejectCalls)
	}
}

func TestRunFlowEmptyDecisionStatusDefaultsToRejected(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		decided:    Request{ID: "approval-1", Status: "", DecisionReason: "because"},
	}
	result, err := RunFlow(context.Background(), svc, flowInputFor(svc))
	if err != nil || result.Approved {
		t.Fatalf("RunFlow() = %+v, %v; want rejected", result, err)
	}
	if result.Status != StatusRejected || result.DecisionReason != "because" {
		t.Fatalf("result = %+v, want rejected/because", result)
	}
}

func TestRunFlowTimeoutRejectsAndEmits(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		waitBlocks: true,
	}
	var emitted []Request
	flow := flowInputFor(svc)
	flow.WaitTimeout = 30 * time.Millisecond
	flow.Emit = func(req Request) bool {
		emitted = append(emitted, req)
		return true
	}
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || result.Approved {
		t.Fatalf("RunFlow() = %+v, %v; want timeout rejection", result, err)
	}
	if result.Status != StatusRejected || result.DecisionReason != "tool approval timed out" || result.DecidedByUser {
		t.Fatalf("result = %+v, want timed-out system rejection", result)
	}
	if len(svc.rejectCalls) != 1 {
		t.Fatalf("reject calls = %v, want 1", svc.rejectCalls)
	}
	if len(emitted) != 2 || emitted[1].Status != StatusRejected {
		t.Fatalf("emitted = %+v, want pending then rejected", emitted)
	}
}

func TestRunFlowTimeoutHonorsConcurrentTerminalDecision(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		waitBlocks: true,
		rejectErr:  ErrAlreadyDecided,
		getResult:  Request{ID: "approval-1", Status: StatusApproved, DecisionReason: "ok", DecidedByUser: true},
	}
	var emitted []Request
	flow := flowInputFor(svc)
	flow.WaitTimeout = 30 * time.Millisecond
	flow.Emit = func(req Request) bool {
		emitted = append(emitted, req)
		return true
	}
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || !result.Approved || !result.DecidedByUser {
		t.Fatalf("RunFlow() = %+v, %v; want concurrent approval", result, err)
	}
	if svc.getCalls != 1 {
		t.Fatalf("get calls = %d, want 1 terminal recovery read", svc.getCalls)
	}
	if len(emitted) != 2 || emitted[0].Status != StatusPending || emitted[1].Status != StatusApproved {
		t.Fatalf("emitted = %+v, want pending then approved", emitted)
	}
}

func TestRunFlowTimeoutHonorsConcurrentSystemRejection(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		waitBlocks: true,
		rejectErr:  ErrAlreadyDecided,
		getResult:  Request{ID: "approval-1", Status: StatusRejected, DecisionReason: "tool approval timed out"},
	}
	flow := flowInputFor(svc)
	flow.WaitTimeout = 30 * time.Millisecond
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || result.Approved || result.DecidedByUser {
		t.Fatalf("RunFlow() = %+v, %v; want concurrent system rejection", result, err)
	}
	if got := RejectionMessage(result); got != "tool execution was not approved: tool approval timed out" {
		t.Fatalf("RejectionMessage() = %q, want system rejection wording", got)
	}
}

func TestRunFlowUndeliveredPendingRejectsWithoutWaiting(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		decided:    Request{ID: "approval-1", Status: StatusApproved, DecidedByUser: true},
	}
	var emitted []Request
	flow := flowInputFor(svc)
	flow.Emit = func(req Request) bool {
		emitted = append(emitted, req)
		return false
	}
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || result.Approved {
		t.Fatalf("RunFlow() = %+v, %v; want undelivered rejection", result, err)
	}
	if result.Status != StatusRejected || result.DecisionReason != "tool approval request was not delivered to the interactive stream" || result.DecidedByUser {
		t.Fatalf("result = %+v, want system rejection for undelivered approval", result)
	}
	if svc.waitCalls != 0 {
		t.Fatalf("wait calls = %d, want 0 after delivery failure", svc.waitCalls)
	}
	if len(svc.rejectCalls) != 1 {
		t.Fatalf("reject calls = %v, want 1", svc.rejectCalls)
	}
	if len(emitted) != 1 || emitted[0].Status != StatusPending {
		t.Fatalf("emitted = %+v, want only pending delivery attempt", emitted)
	}
}

func TestRunFlowCallerCancellationPropagates(t *testing.T) {
	t.Parallel()
	svc := &fakeFlowService{
		evaluation: Evaluation{Decision: DecisionNeedsApproval},
		waitBlocks: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	flow := flowInputFor(svc)
	flow.WaitTimeout = time.Minute
	_, err := RunFlow(ctx, svc, flow)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunFlow() err = %v, want context.Canceled", err)
	}
	if len(svc.rejectCalls) != 1 || svc.rejectCalls[0] != "approval-1:tool approval aborted" {
		t.Fatalf("reject calls = %v, want aborted cleanup", svc.rejectCalls)
	}
}

func TestRejectionMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result FlowResult
		want   string
	}{
		{
			name:   "user rejected with reason",
			result: FlowResult{Status: StatusRejected, DecidedByUser: true, DecisionReason: "too risky"},
			want:   "tool execution rejected by user: too risky",
		},
		{
			name:   "user rejected without reason",
			result: FlowResult{Status: StatusRejected, DecidedByUser: true},
			want:   "tool execution rejected by user",
		},
		{
			name:   "timeout reject is a system outcome",
			result: FlowResult{Status: StatusRejected, DecisionReason: "tool approval timed out"},
			want:   "tool execution was not approved: tool approval timed out",
		},
		{
			name:   "non-rejected terminal status without reason",
			result: FlowResult{Status: "expired"},
			want:   "tool execution was not approved: expired",
		},
		{
			name:   "no status no reason",
			result: FlowResult{},
			want:   "tool execution was not approved",
		},
	}
	for _, tc := range cases {
		if got := RejectionMessage(tc.result); got != tc.want {
			t.Fatalf("%s: RejectionMessage() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
