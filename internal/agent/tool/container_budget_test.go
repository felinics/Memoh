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
