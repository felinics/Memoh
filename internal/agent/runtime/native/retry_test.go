package native

import (
	"context"
	"errors"
	"testing"
)

func TestRetryableStreamErrorSeparatesProviderStatusFromApplicationTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "provider 504", err: errors.New("api error 504: gateway timeout"), want: true},
		{name: "provider 524", err: errors.New("api error 524: origin timeout"), want: true},
		{name: "timeout wording alone", err: errors.New("request timeout label only"), want: false},
		{name: "application deadline", err: context.DeadlineExceeded, want: false},
		{name: "explicit cancellation", err: context.Canceled, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableStreamError(tt.err); got != tt.want {
				t.Fatalf("isRetryableStreamError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
