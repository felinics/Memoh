package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/memohai/memoh/domains/model/search"
	"github.com/memohai/memoh/internal/apperror"
)

func TestSearchProviderHTTPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode apperror.Code
	}{
		{
			name:     "provider type conflict",
			err:      search.ErrProviderTypeConflict,
			wantCode: apperror.CodeSearchProviderTypeConflict,
		},
		{
			name:     "wrapped provider type conflict",
			err:      fmt.Errorf("wrapped: %w", search.ErrProviderTypeConflict),
			wantCode: apperror.CodeSearchProviderTypeConflict,
		},
		{
			name:     "provider name conflict",
			err:      search.ErrProviderNameTaken,
			wantCode: apperror.CodeProviderNameTaken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := searchProviderHTTPError(tt.err)
			if code := apperror.CodeOf(got); code != tt.wantCode {
				t.Fatalf("code = %q, want %q", code, tt.wantCode)
			}
			if cause := apperror.CauseOf(got); !errors.Is(cause, tt.err) {
				t.Fatalf("private cause = %v, want %v", cause, tt.err)
			}
		})
	}
}
