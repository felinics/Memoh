package claudecode

import (
	"context"
	"errors"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
)

func (t *turnRunner) writeLine(ctx context.Context, line []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		t.writeMu.Lock()
		defer t.writeMu.Unlock()
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		_, err := t.proc.Write(append(line, '\n'))
		done <- errs.WrapDependency(err, "")
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	case <-t.ctx.Done():
	}
	cause := ctx.Err()
	if cause == nil {
		cause = t.ctx.Err()
	}
	cancel()
	stopErr := stopCLI(ctx, t.proc)
	t.observeExit(stopErr)
	t.finish()
	return errors.Join(cause, stopErr)
}

func (t *turnRunner) finishEarlyPrompt(ctx context.Context, storedSessionID string, cause error) (external.PromptResult, error) {
	t.observeExit(stopCLI(ctx, t.proc))
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-t.readDone:
	case <-timer.C:
		t.observeExit(external.ErrTerminationUnconfirmed)
	}
	t.close()
	result, resultErr := t.buildResult(storedSessionID)
	if result.TerminationUnconfirmed {
		cause = errors.Join(cause, resultErr)
	}
	return result, cause
}
