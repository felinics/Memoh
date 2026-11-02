package tools

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/chat/attention"
)

type fakeScheduleNotifier struct {
	fire   attention.ScheduleFire
	found  bool
	marked bool
	marks  []attention.Kind
}

func (f *fakeScheduleNotifier) ActiveScheduleFire(context.Context, string) (attention.ScheduleFire, bool, error) {
	return f.fire, f.found, nil
}

func (f *fakeScheduleNotifier) Mark(_ context.Context, _ string, kind attention.Kind) (bool, error) {
	f.marks = append(f.marks, kind)
	return f.marked, nil
}

func scheduleSession() SessionContext {
	return SessionContext{BotID: "bot", SessionID: "session", SessionType: sessionmode.Schedule}
}

func TestNotifyUserRegistersOnlyForSchedulesThatAsk(t *testing.T) {
	important := attention.ScheduleFire{NotifyPolicy: attention.NotifyImportant}
	cases := []struct {
		name     string
		session  func() SessionContext
		notifier *fakeScheduleNotifier
		want     bool
	}{
		{"important schedule run", scheduleSession, &fakeScheduleNotifier{fire: important, found: true}, true},
		{"every-run schedule", scheduleSession, &fakeScheduleNotifier{fire: attention.ScheduleFire{NotifyPolicy: attention.NotifyEveryRun}, found: true}, false},
		{"no fire running", scheduleSession, &fakeScheduleNotifier{}, false},
		{"chat session", func() SessionContext { s := scheduleSession(); s.SessionType = sessionmode.Chat; return s }, &fakeScheduleNotifier{fire: important, found: true}, false},
		{"subagent", func() SessionContext { s := scheduleSession(); s.IsSubagent = true; return s }, &fakeScheduleNotifier{fire: important, found: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools, err := NewNotifyProvider(nil, tc.notifier).Tools(t.Context(), tc.session())
			if err != nil {
				t.Fatalf("Tools: %v", err)
			}
			if got := len(tools) == 1 && tools[0].Name == ToolNotifyUser().String(); got != tc.want {
				t.Fatalf("registered = %v, want %v (%d tools)", got, tc.want, len(tools))
			}
		})
	}
}

func TestNotifyUserMarksAnUpdateOncePerFire(t *testing.T) {
	cases := []struct {
		name      string
		notified  bool
		markable  bool
		status    string
		wantMarks int
	}{
		{"first call", false, true, "notified", 1},
		{"already notified this fire", true, true, "already_notified", 0},
		{"session without unread state", false, false, "not_applicable", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notifier := &fakeScheduleNotifier{
				fire:   attention.ScheduleFire{NotifyPolicy: attention.NotifyImportant, Notified: tc.notified},
				found:  true,
				marked: tc.markable,
			}
			tools, err := NewNotifyProvider(nil, notifier).Tools(t.Context(), scheduleSession())
			if err != nil || len(tools) != 1 {
				t.Fatalf("Tools = %d, %v", len(tools), err)
			}
			out, err := tools[0].Execute(&toolexec.ToolExecContext{Context: t.Context()}, toolexec.ArgumentsFromValue(map[string]any{}))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			value, _ := toolexec.OutputValue(out).(map[string]any)
			if value["status"] != tc.status {
				t.Fatalf("status = %v, want %s", value["status"], tc.status)
			}
			if len(notifier.marks) != tc.wantMarks {
				t.Fatalf("marks = %v, want %d", notifier.marks, tc.wantMarks)
			}
			for _, kind := range notifier.marks {
				if kind != attention.KindUpdate {
					t.Fatalf("marked %q, want update", kind)
				}
			}
		})
	}
}
