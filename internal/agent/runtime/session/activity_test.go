package sessionruntime

import (
	"context"
	"errors"
	"testing"

	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestAdmissionInvalidatesTranscriptBeforeReturning(t *testing.T) {
	for _, kind := range []string{RunOperationEdit, RunOperationRetry} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmitFixture(t)
			called := false
			var notifiedBot, notifiedSession string
			f.manager.SetAdmissionObserver(func(botID, sessionID string) {
				called = true
				notifiedBot, notifiedSession = botID, sessionID
				snapshot, err := f.manager.Snapshot(context.Background(), botID, sessionID)
				if err != nil || snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Operation == nil {
					t.Errorf("admission observer ran without a readable replacement snapshot: %v", err)
				}
			})
			input := f.input("inv-replace", kind)
			input.Execution.Admission = func(context.Context, RunHandle) (RunAdmissionView, error) {
				return RunAdmissionView{Operation: &RunOperationView{
					Kind: kind, ReplaceFromMessageID: "old-message",
					ReplacementUserTurn: &chatview.UITurn{Role: "user", Text: "edited"},
				}}, nil
			}
			if _, err := f.manager.Admit(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			// No model output or message persistence has occurred. Observers
			// must already have been told when admission returns; the
			// composition root turns this callback into a hub publication.
			if !called || notifiedBot != testBotID || notifiedSession != testSessionID {
				t.Fatalf("accepted replacement did not notify observer: called=%v bot=%q session=%q",
					called, notifiedBot, notifiedSession)
			}
		})
	}
}

func TestFailedAdmissionDoesNotNotifyTranscriptObserver(t *testing.T) {
	f := newAdmitFixture(t)
	called := false
	f.manager.SetAdmissionObserver(func(string, string) { called = true })
	input := f.input("inv-invalid", "edit")
	input.Execution.Admission = func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, errors.New("invalid replacement")
	}
	if _, err := f.manager.Admit(context.Background(), input); err == nil {
		t.Fatal("expected admission rejection")
	}
	if called {
		t.Fatal("rejected admission notified transcript observer")
	}
}
