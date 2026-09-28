package turn

import "testing"

func TestRunTerminalEventRoundTrip(t *testing.T) {
	payload, err := NewRunTerminalEvent(" failed ", " agent.provider_overloaded ")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"type":"run_terminal","state":"failed","error_code":"agent.provider_overloaded"}` {
		t.Fatalf("payload = %s", payload)
	}
	terminal, ok := RunTerminalFrom(Event{Kind: EventRunTerminal, Payload: payload})
	if !ok || terminal.State != RunStateFailed || terminal.ErrorCode != "agent.provider_overloaded" || !terminal.Failed() {
		t.Fatalf("terminal = %+v, ok = %v", terminal, ok)
	}
	completed, _ := NewRunTerminalEvent(RunStateCompleted, "")
	if string(completed) != `{"type":"run_terminal","state":"completed"}` {
		t.Fatalf("completed payload = %s", completed)
	}
}

func TestRunTerminalFromRejectsOtherEvents(t *testing.T) {
	for _, event := range []Event{
		{Kind: "error", Payload: []byte(`{"type":"run_terminal","state":"failed"}`)},
		{Kind: EventRunTerminal, Payload: []byte(`not json`)},
		{Kind: EventRunTerminal, Payload: []byte(`{"type":"run_terminal"}`)},
	} {
		if terminal, ok := RunTerminalFrom(event); ok {
			t.Fatalf("RunTerminalFrom(%s %s) = %+v", event.Kind, event.Payload, terminal)
		}
	}
}

func TestRunTerminalFailedStates(t *testing.T) {
	for state, want := range map[string]bool{
		RunStateCompleted: false,
		RunStateAborted:   false,
		RunStateFailed:    true,
		RunStateLost:      true,
	} {
		if got := (RunTerminal{State: state}).Failed(); got != want {
			t.Fatalf("Failed(%s) = %v, want %v", state, got, want)
		}
	}
}
