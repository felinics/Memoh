package tools

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestCommandExecutionBudgets(t *testing.T) {
	for _, args := range []execArgs{
		{MaxDurationSeconds: intPtr(0)},
		{MaxDurationSeconds: intPtr(29)},
		{MaxDurationSeconds: intPtr(86401)},
		{BackgroundMode: "service", MaxDurationSeconds: intPtr(20)},
		{BackgroundMode: "invalid"},
	} {
		if _, err := execBudget(args.MaxDurationSeconds, args.BackgroundMode, true); err == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
	if _, err := execBudget(nil, "service", false); err == nil {
		t.Fatal("foreground service accepted")
	}
	if budget, err := execBudget(intPtr(30), "", false); err != nil || budget != 30*time.Second {
		t.Fatalf("30-second command budget = %v, %v", budget, err)
	}
	if budget, err := videoMonitorBudget(intPtr(1)); err != nil || budget != time.Second {
		t.Fatalf("one-second video monitor budget = %v, %v", budget, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if wait := foregroundWaitBeforeBudget(ctx, 30); wait < 28*time.Second || wait >= 30*time.Second {
		t.Fatalf("foreground wait = %v, want a margin before the 30-second budget", wait)
	}
	if wait := foregroundWaitBeforeBudget(ctx, 600); wait < 28*time.Second || wait >= 30*time.Second {
		t.Fatalf("custom foreground wait = %v, want the same hard-budget margin", wait)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := execBudgetContext(context.Background(), []time.Duration{time.Hour})
		defer cancel()
		time.Sleep(31 * time.Minute)
		if ctx.Err() != nil {
			t.Fatal("legacy 30m limit still applies")
		}
		time.Sleep(30 * time.Minute)
		if ctx.Err() == nil {
			t.Fatal("execution budget did not expire")
		}
	})
}
