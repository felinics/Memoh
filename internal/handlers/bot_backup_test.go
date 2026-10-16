package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botbackup"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimefence"
)

// The history reset an overwrite import takes fails the way a clear does:
// busy only while something else holds the conversation, unavailable without
// coordination, and answered by its fault when a dependency failed.
func TestImportErrorClassifiesHistoryResetFailures(t *testing.T) {
	begin := func(err error) error {
		return errors.Join(botbackup.ErrHistoryResetUnavailable, fmt.Errorf("begin ACP runtime reset for overwrite import: %w", err))
	}
	for _, tc := range []struct {
		name  string
		err   error
		code  apperror.Code
		fault apperror.Fault
	}{
		{"lease taken over", runtimefence.ErrResetLeaseLost, apperror.CodeSessionResetConflict, apperror.FaultClient},
		{"run did not stop in time", begin(sessionruntime.ErrCommandNotAcknowledged), apperror.CodeSessionResetConflict, apperror.FaultClient},
		{"runtime backend failed", begin(errs.WrapDependency(errors.New("runtime backend write failed"), "clear runtime snapshots")), apperror.CodeInternal, apperror.FaultDependency},
		{"coordination unavailable", botbackup.ErrHistoryResetUnavailable, apperror.CodeSessionResetUnavailable, apperror.FaultServer},
		{"transactions unsupported", runtimefence.ErrTransactionsUnsupported, apperror.CodeSessionResetUnavailable, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer, fault := errs.Answer(context.Background(), importError(tc.err))
			if code := apperror.CodeOf(answer); code != tc.code || fault != tc.fault {
				t.Fatalf("importError() answered %q fault %q, want %q / %q", code, fault, tc.code, tc.fault)
			}
		})
	}
}
