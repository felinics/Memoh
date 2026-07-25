package workspace

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestWorkspaceTargetContextIsRequestScoped(t *testing.T) {
	t.Parallel()

	base := context.Background()
	first := WithWorkspaceTarget(base, " target-1 ")
	second := WithWorkspaceTarget(base, "target-2")

	if got := WorkspaceTargetFromContext(base); got != "" {
		t.Fatalf("base target = %q, want empty", got)
	}
	if got := WorkspaceTargetFromContext(first); got != "target-1" {
		t.Fatalf("first target = %q, want target-1", got)
	}
	if got := WorkspaceTargetFromContext(second); got != "target-2" {
		t.Fatalf("second target = %q, want target-2", got)
	}
	if got := WorkspaceTargetFromContext(WithWorkspaceTarget(first, "")); got != "" {
		t.Fatalf("cleared target = %q, want empty", got)
	}

	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			ctx := first
			want := "target-1"
			if index%2 == 1 {
				ctx = second
				want = "target-2"
			}
			for range 100 {
				if got := WorkspaceTargetFromContext(ctx); got != want {
					errs <- fmt.Errorf("worker %d target = %q, want %q", index, got, want)
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
