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

// The adapter hands the application everything the channel boundary
// recorded, the route included, so a queued follow-up replays the turn the
// channel would have started.
func TestEnqueueCarriesTheChannelRoute(t *testing.T) {
	adapter := New(&application.Service{})
	in := inbound.QueueCommandInput{
		TeamID: "team-1", BotID: "bot-1", SessionID: "session-1", InvocationID: "invoke-1",
		UserID: "user-1", ChannelIdentityID: "identity-1", Text: "hello",
		RouteID: "route-1", CurrentChannel: "cli", ReplyTarget: "target-1", ConversationType: "private",
	}
	var got application.QueueInput
	if err := adapter.enqueue(in, func(queued application.QueueInput) error { got = queued; return nil }); err != nil {
		t.Fatal(err)
	}
	want := application.QueueInput{
		TeamID: "team-1", BotID: "bot-1", SessionID: "session-1", InvocationID: "invoke-1",
		UserID: "user-1", SourceChannelIdentityID: "identity-1", Text: "hello",
		RouteID: "route-1", CurrentChannel: "cli", ReplyTarget: "target-1", ConversationType: "private",
	}
	if got != want {
		t.Fatalf("queued input = %+v, want %+v", got, want)
	}
}
