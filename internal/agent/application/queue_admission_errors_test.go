package application

import (
	"errors"
	"fmt"
	"testing"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

func TestQueueAdmissionFailureOf(t *testing.T) {
	cases := []struct {
		err  error
		want QueueAdmissionFailure
	}{
		{nil, QueueAdmissionNotRefused},
		{errors.New("boom"), QueueAdmissionNotRefused},
		{sessionruntime.ErrLiveQueueUnavailable, QueueAdmissionNotRefused},
		{sessionruntime.ErrQueueNotPending, QueueAdmissionNotRefused},
		{sessionruntime.ErrQueueSteerUnsupported, QueueAdmissionSteerUnsupported},
		{sessionruntime.ErrQueueNoActiveRun, QueueAdmissionNoActiveRun},
		{sessionruntime.ErrQueueInvocationConflict, QueueAdmissionInvocationConflict},
		{sessionruntime.ErrQueueAdmissionOverloaded, QueueAdmissionOverloaded},
		{sessionruntime.ErrQueueCapacityExceeded, QueueAdmissionCapacityExceeded},
		{sessionruntime.ErrQueueInvalidReference, QueueAdmissionInvalidReference},
		{fmt.Errorf("enqueue: %w", ErrQueueInputIncomplete), QueueAdmissionUnavailable},
	}
	for _, tc := range cases {
		if got := QueueAdmissionFailureOf(tc.err); got != tc.want {
			t.Errorf("QueueAdmissionFailureOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
