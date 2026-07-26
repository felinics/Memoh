package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/memohai/memoh/domains/model/search"
	"github.com/memohai/memoh/internal/apperror"
)

func TestSearchProviderError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantKind apperror.Kind
		wantCode apperror.Code
	}{
		{
			name:     "provider type conflict",
			err:      search.ErrProviderTypeConflict,
			wantKind: apperror.KindConflict,
			wantCode: apperror.CodeSearchProviderTypeConflict,
		},
		{
			name:     "wrapped provider type conflict",
			err:      fmt.Errorf("wrapped: %w", search.ErrProviderTypeConflict),
			wantKind: apperror.KindConflict,
			wantCode: apperror.CodeSearchProviderTypeConflict,
		},
		{
			name:     "provider name conflict",
			err:      search.ErrProviderNameTaken,
			wantKind: apperror.KindConflict,
			wantCode: apperror.CodeProviderNameTaken,
		},
		{
			// An unrecognized failure stays internal and unnamed: inventing a
			// code here is what turns a catalog into a dumping ground.
			name:     "unrecognized failure",
			err:      errors.New("connection reset"),
			wantKind: apperror.KindInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := searchProviderError("create search provider", tt.err)
			if kind := apperror.KindOf(got); kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", kind, tt.wantKind)
			}
			if code := apperror.CodeOf(got); code != tt.wantCode {
				t.Fatalf("code = %q, want %q", code, tt.wantCode)
			}
			if op := apperror.OpOf(got); op != "create search provider" {
				t.Fatalf("op = %q, want the caller's op", op)
			}
			if cause := apperror.CauseOf(got); !errors.Is(cause, tt.err) {
				t.Fatalf("private cause = %v, want %v", cause, tt.err)
			}
		})
	}
}
