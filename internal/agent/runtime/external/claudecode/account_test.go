package claudecode

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
)

// A model's own weekly window is shown but does not block the account; the
// shared windows do.
func TestAccountQuotaFromUsage(t *testing.T) {
	var usage usageResponse
	if err := json.Unmarshal([]byte(`{
		"rate_limits_available": true,
		"rate_limits": {
			"five_hour": {"utilization": 12.6, "resets_at": "2026-11-02T18:00:00.000Z"},
			"seven_day": {"utilization": 40, "resets_at": null},
			"model_scoped": [{"display_name": "Fable", "utilization": 100, "resets_at": "2026-11-05T00:00:00Z"}]
		}
	}`), &usage); err != nil {
		t.Fatal(err)
	}
	quota := accountQuota(usage)
	if quota.LimitReached || len(quota.Windows) != 3 {
		t.Fatalf("quota = %+v", quota)
	}
	five, week, model := quota.Windows[0], quota.Windows[1], quota.Windows[2]
	if five.UsedPercent != 13 || five.WindowMinutes != 300 || five.ResetsAt == nil || !five.ResetsAt.Equal(time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("five-hour window = %+v", five)
	}
	if week.UsedPercent != 40 || week.WindowMinutes != 7*24*60 || week.ResetsAt != nil || week.Model != "" {
		t.Fatalf("weekly window = %+v", week)
	}
	if model.Model != "Fable" || model.UsedPercent != 100 {
		t.Fatalf("model window = %+v", model)
	}

	*usage.RateLimits.FiveHour.Utilization = 100
	if !accountQuota(usage).LimitReached {
		t.Fatal("a used-up five-hour window must block the account")
	}
}

// A Claude account login reports its plan and email, not a token source.
func TestInitializeAccountSignedIn(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"email":"a@example.invalid","subscriptionType":"max","organization":"Example"}`: true,
		`{"tokenSource":"CLAUDE_CODE_OAUTH_TOKEN"}`:                                       true,
		`{"tokenSource":"none"}`:                                                          false,
		`{}`:                                                                              false,
	} {
		var account initializeAccount
		if err := json.Unmarshal([]byte(raw), &account); err != nil {
			t.Fatal(err)
		}
		if account.signedIn() != want {
			t.Errorf("%s: signedIn = %v, want %v", raw, !want, want)
		}
	}
}

// A sign-in waiting for its code is a process of the Agent like any other: a
// newer sign-in replaces it, and a sign-out's drain closes it without
// counting it at work, ending the sign-in.
func TestSignInIsAnAgentProcess(t *testing.T) {
	procs := agentproc.New(agentproc.Options{})
	t.Cleanup(func() { procs.Close(context.Background()) })
	d := &Driver{procs: procs, logger: slog.Default(), signIns: map[string]*signIn{}}
	start := func(context.Context) (cliProcess, error) { return newPipeProcess(), nil }
	newSignIn := func() *signIn {
		cli, err := d.leaseControlCLI(context.Background(), "bot", "agent", start)
		if err != nil {
			t.Fatal(err)
		}
		s := &signIn{id: uuid.NewString(), botID: "bot", botAgentID: "agent", cli: cli}
		d.addSignIn(s)
		return s
	}
	exited := func(s *signIn) {
		t.Helper()
		select {
		case <-s.cli.sess.Done():
		case <-time.After(time.Second):
			t.Fatal("the sign-in's process is still running")
		}
		for deadline := time.Now().Add(time.Second); d.signIn(s.id, "agent") != nil; {
			if time.Now().After(deadline) {
				t.Fatal("the sign-in outlived its process")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	first := newSignIn()
	second := newSignIn()
	exited(first)

	busy := func(p agentproc.Process) bool {
		b, ok := p.(interface{ Busy() bool })
		return ok && b.Busy()
	}
	unblock, err := procs.Drain(context.Background(), agentProcesses("bot", "agent"), busy)
	if err != nil {
		t.Fatalf("drain refused a sign-in waiting for its code: %v", err)
	}
	unblock()
	exited(second)
}
