package botworkspace

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		w    Workspace
		want Action
	}{
		{"present/absent provisions", Workspace{Desired: DesiredPresent, Observed: ObservedAbsent}, ActionProvision},
		{"present/provisioning resumes", Workspace{Desired: DesiredPresent, Observed: ObservedProvisioning}, ActionProvision},
		{"present/running settles", Workspace{Desired: DesiredPresent, Observed: ObservedRunning}, ActionNone},
		{"present/stopped is user intent, no action", Workspace{Desired: DesiredPresent, Observed: ObservedStopped}, ActionNone},
		{"present/failed waits during backoff", Workspace{Desired: DesiredPresent, Observed: ObservedFailed, NextAttemptAt: now.Add(time.Minute)}, ActionWait},
		{"present/failed retries after backoff", Workspace{Desired: DesiredPresent, Observed: ObservedFailed, NextAttemptAt: now.Add(-time.Second)}, ActionProvision},
		{"absent/running tears down", Workspace{Desired: DesiredAbsent, Observed: ObservedRunning}, ActionTeardown},
		{"absent/failed tears down once due", Workspace{Desired: DesiredAbsent, Observed: ObservedFailed, NextAttemptAt: now.Add(-time.Second)}, ActionTeardown},
		{"absent/failed waits for its slow retry", Workspace{Desired: DesiredAbsent, Observed: ObservedFailed, NextAttemptAt: now.Add(10 * time.Minute)}, ActionWait},
		{"absent/removing resumes", Workspace{Desired: DesiredAbsent, Observed: ObservedRemoving}, ActionTeardown},
		{"absent/absent settles once observed", Workspace{Desired: DesiredAbsent, DesiredGeneration: 2, Observed: ObservedAbsent, ObservedGeneration: 2}, ActionNone},
		{"absent with unobserved default column tears down", Workspace{Desired: DesiredAbsent, DesiredGeneration: 1, Observed: ObservedAbsent, ObservedGeneration: 0}, ActionTeardown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.w, now); got != tc.want {
				t.Fatalf("Decide() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNextBackoff(t *testing.T) {
	now := time.Unix(0, 0)
	base, capD := 30*time.Second, 10*time.Minute
	got := []time.Duration{
		NextBackoff(now, 1, base, capD).Sub(now),
		NextBackoff(now, 2, base, capD).Sub(now),
		NextBackoff(now, 3, base, capD).Sub(now),
		NextBackoff(now, 4, base, capD).Sub(now),
		NextBackoff(now, 5, base, capD).Sub(now),
		NextBackoff(now, 9, base, capD).Sub(now),
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt %d backoff = %s, want %s", i+1, got[i], want[i])
		}
	}
}

func TestDeriveBotStatus(t *testing.T) {
	cases := []struct {
		name   string
		w      Workspace
		want   string
		wantOK bool
	}{
		{"first provisioning is creating", Workspace{Desired: DesiredPresent, Observed: ObservedProvisioning}, BotStatusCreating, true},
		{"absent before first success is creating", Workspace{Desired: DesiredPresent, Observed: ObservedAbsent}, BotStatusCreating, true},
		{"running is ready", Workspace{Desired: DesiredPresent, Observed: ObservedRunning}, BotStatusReady, true},
		{"stopped is ready", Workspace{Desired: DesiredPresent, Observed: ObservedStopped}, BotStatusReady, true},
		{"failed before first success is failed", Workspace{Desired: DesiredPresent, Observed: ObservedFailed}, BotStatusFailed, true},
		{"failed after being ready stays ready", Workspace{Desired: DesiredPresent, Observed: ObservedFailed, EverReady: true}, BotStatusReady, true},
		{"vanished after being ready stays ready", Workspace{Desired: DesiredPresent, Observed: ObservedAbsent, EverReady: true}, BotStatusReady, true},
		{"absent intent leaves bot status alone", Workspace{Desired: DesiredAbsent, Observed: ObservedRemoving}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DeriveBotStatus(tc.w)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("DeriveBotStatus() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestSettled(t *testing.T) {
	if (Workspace{Desired: DesiredPresent, DesiredGeneration: 2, Observed: ObservedRunning, ObservedGeneration: 1}).Settled() {
		t.Fatal("stale generation must not be settled")
	}
	if (Workspace{Desired: DesiredPresent, DesiredGeneration: 1, Observed: ObservedProvisioning, ObservedGeneration: 1}).Settled() {
		t.Fatal("transitional state must not be settled")
	}
	if !(Workspace{Desired: DesiredPresent, DesiredGeneration: 1, Observed: ObservedFailed, ObservedGeneration: 1}).Settled() {
		t.Fatal("failed is a settled answer to a present intent")
	}
	failedTeardown := Workspace{Desired: DesiredAbsent, DesiredGeneration: 1, Observed: ObservedFailed, ObservedGeneration: 1, Attempts: 3}
	if !failedTeardown.Settled() || !failedTeardown.Final(3) {
		t.Fatalf("a teardown failure past its fast retries must be settled and final: settled=%v final=%v", failedTeardown.Settled(), failedTeardown.Final(3))
	}
	if (Workspace{Desired: DesiredAbsent, DesiredGeneration: 1, Observed: ObservedRemoving, ObservedGeneration: 1}).Settled() {
		t.Fatal("removing is not a settled answer to an absent intent")
	}
	if !(Workspace{Desired: DesiredAbsent, DesiredGeneration: 1, Observed: ObservedAbsent, ObservedGeneration: 1}).Settled() {
		t.Fatal("absent/absent must be settled")
	}
}
