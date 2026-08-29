package queue

import (
	"context"
	"errors"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

type admissionStub struct{ run string }

func (a admissionStub) AdmitQueue(context.Context, string, string) (Admission, error) {
	return Admission{RunID: a.run, Active: a.run != ""}, nil
}

func handle() sessionruntime.RunHandle {
	return sessionruntime.RunHandle{BotID: "b", SessionID: "s", RunID: "r", OwnerID: "o", Generation: "g", FencingToken: 7}
}

func TestSteerFIFOAndAcceptedOnlyReorder(t *testing.T) {
	q := NewSteerQueue(admissionStub{run: "r"})
	for _, id := range []string{"a", "b", "c"} {
		if _, err := q.Enqueue(context.Background(), "b", "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Reorder("c", "a"); err != nil {
		t.Fatal(err)
	}
	_, firstRef, err := q.Claim(handle())
	if err != nil || firstRef.ItemID != "c" {
		t.Fatalf("reordered first claim = %#v, %v", firstRef, err)
	}
	if err := q.Reorder("b", "c"); !errors.Is(err, ErrNotPending) {
		t.Fatal("claimed item should not be a reorder destination")
	}
	if err := q.Apply(firstRef, handle()); err != nil {
		t.Fatal(err)
	}
	_, ref, err := q.Claim(handle())
	if err != nil || ref.ItemID != "a" {
		t.Fatalf("FIFO claim = %#v, %v", ref, err)
	}
}

func TestFollowUpFIFOAndAcceptedOnlyReorder(t *testing.T) {
	q := NewFollowUpQueue(admissionStub{run: "r"})
	for _, id := range []string{"a", "b", "c"} {
		if _, err := q.Enqueue(context.Background(), "b", "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Reorder("c", "a"); err != nil {
		t.Fatal(err)
	}
	assigned, err := q.AssignNext("continuation-1")
	if err != nil || assigned.ID != "c" {
		t.Fatalf("reordered first assignment = %#v, %v", assigned, err)
	}
	if err := q.Reorder("c", "a"); !errors.Is(err, ErrNotPending) {
		t.Fatal("assigned follow-up should not reorder")
	}
	next, err := q.AssignNext("continuation-2")
	if err != nil || next.ID != "a" {
		t.Fatalf("next follow-up assignment = %#v, %v", next, err)
	}
}

func TestQueuesAreTypeIsolated(t *testing.T) {
	s := NewSteerQueue(admissionStub{run: "r"})
	f := NewFollowUpQueue(admissionStub{run: "r"})
	if _, err := s.Enqueue(context.Background(), "b", "s", "x", []byte("s")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Enqueue(context.Background(), "b", "s", "x", []byte("f")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(SteerItemID("x")); !ok {
		t.Fatal("steer missing")
	}
	if _, ok := f.Get(FollowUpItemID("x")); !ok {
		t.Fatal("follow-up missing")
	}
}

func TestClaimFencing(t *testing.T) {
	q := NewSteerQueue(admissionStub{run: "r"})
	if _, err := q.Enqueue(context.Background(), "b", "s", "x", []byte("x")); err != nil {
		t.Fatal(err)
	}
	_, ref, err := q.Claim(handle())
	if err != nil {
		t.Fatal(err)
	}
	bad := handle()
	bad.FencingToken = 8
	if err := q.Apply(ref, bad); !errors.Is(err, ErrInvalidReference) {
		t.Fatal("stale claim applied")
	}
	if err := q.Apply(ref, handle()); err != nil {
		t.Fatal(err)
	}
}

func TestQueueEditCancelAndAppendOnlyPending(t *testing.T) {
	q := NewSteerQueue(admissionStub{run: "r"})
	for _, id := range []string{"a", "b", "c"} {
		if _, err := q.Enqueue(context.Background(), "b", "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Update("b", []byte("edited")); err != nil {
		t.Fatal(err)
	}
	if err := q.Reorder("a", ""); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel("c"); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel("c"); !errors.Is(err, ErrNotPending) {
		t.Fatal("terminal item should not be canceled twice")
	}
	_, ref, err := q.Claim(handle())
	if err != nil || ref.ItemID != "b" {
		t.Fatalf("claim after append/edit = %#v, %v", ref, err)
	}
	if _, err := q.Update("b", []byte("late")); !errors.Is(err, ErrNotPending) {
		t.Fatal("claimed item should not be edited")
	}
}

func TestMemoryCoordinatorFinalBoundaryRunsWithoutHistory(t *testing.T) {
	steer := NewSteerQueue(admissionStub{run: "r"})
	follow := NewFollowUpQueue(admissionStub{run: "r"})
	if _, err := steer.Enqueue(context.Background(), "b", "s", "steer", []byte("next")); err != nil {
		t.Fatal(err)
	}
	coordinator := NewMemoryCoordinator(steer, follow)
	result, err := coordinator.CommitStep(context.Background(), CommitStepRequest{
		Run: handle(), StepIndex: 0, Kind: StepFinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != ContinueWithSteer || result.Steer == nil || result.Steer.ID != "steer" {
		t.Fatalf("empty final boundary result = %#v", result)
	}
}
