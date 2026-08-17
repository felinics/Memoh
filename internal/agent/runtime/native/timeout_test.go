package native

import (
	"context"
	"errors"
	"testing"
)

func TestIsTimeoutStreamError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "http client timeout", err: errors.New("Client.Timeout exceeded while awaiting headers"), want: true},
		{name: "provider timeout", err: errors.New("request timeout waiting for response headers"), want: true},
		{name: "ordinary provider error", err: errors.New("api error 500"), want: false},
		{name: "cancelled", err: context.Canceled, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTimeoutStreamError(tc.err); got != tc.want {
				t.Fatalf("IsTimeoutStreamError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
