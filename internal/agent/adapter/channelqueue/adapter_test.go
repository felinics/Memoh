package channelqueue

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/application"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/channel/inbound"
)

func TestMapAdmissionErrorKeepsChannelCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{sessionruntime.ErrQueueSteerUnsupported, inbound.QueueCommandCodeUnsupported},
		{sessionruntime.ErrQueueNoActiveRun, inbound.QueueCommandCodeNoActiveRun},
		{sessionruntime.ErrQueueInvocationConflict, inbound.QueueCommandCodeConflict},
		{sessionruntime.ErrQueueAdmissionOverloaded, inbound.QueueCommandCodeOverloaded},
		{sessionruntime.ErrQueueCapacityExceeded, inbound.QueueCommandCodeCapacity},
		{sessionruntime.ErrQueueInvalidReference, inbound.QueueCommandCodeInvalid},
		{application.ErrQueueInputIncomplete, inbound.QueueCommandCodeUnavailable},
	}
	for _, tc := range cases {
		if got := inbound.QueueCommandErrorCode(mapAdmissionError(tc.err)); got != tc.want {
			t.Errorf("mapAdmissionError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	other := errors.New("boom")
	if got := mapAdmissionError(other); !errors.Is(got, other) {
		t.Fatalf("unclassified error was rewritten: %v", got)
	}
	if mapAdmissionError(nil) != nil {
		t.Fatal("nil error was rewritten")
	}
}
