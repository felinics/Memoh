package qq

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func preparedQQEvent(event channel.StreamEvent) channel.PreparedStreamEvent {
	prepared := channel.PreparedStreamEvent{
		Type:   event.Type,
		Delta:  event.Delta,
		Error:  event.Error,
		Status: event.Status,
		Phase:  event.Phase,
	}
	if len(event.Attachments) > 0 {
		prepared.Attachments = make([]channel.PreparedAttachment, 0, len(event.Attachments))
		for _, att := range event.Attachments {
			prepared.Attachments = append(prepared.Attachments, channel.PreparedAttachment{
				Logical:   att,
				Kind:      channel.PreparedAttachmentUpload,
				Name:      att.Name,
				Mime:      att.Mime,
				PublicURL: att.URL,
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("test")), nil
				},
			})
		}
	}
	if event.Final != nil {
		prepared.Final = &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{Message: event.Final.Message},
		}
	}
	return prepared
}

func TestQQOutboundStreamFlushesBufferedTextOnFinal(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted})); err != nil {
		t.Fatalf("push status: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "Hi "})); err != nil {
		t.Fatalf("push delta1: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "there"})); err != nil {
		t.Fatalf("push delta2: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one send, got %d", len(sent))
	}
	if sent[0].Target != "c2c:user-openid" {
		t.Fatalf("unexpected target: %s", sent[0].Target)
	}
	if sent[0].Message.PlainText() != "Hi there" {
		t.Fatalf("unexpected text: %q", sent[0].Message.PlainText())
	}
}

func TestQQOutboundStreamFinalUsesExplicitMessageAndBufferedAttachments(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "group:group-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type:        channel.StreamEventAttachment,
		Attachments: []channel.Attachment{{Type: channel.AttachmentImage, URL: "https://example.com/a.png"}},
	})); err != nil {
		t.Fatalf("push attachment: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{Message: channel.Message{
			Text: "done",
		}},
	})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one send, got %d", len(sent))
	}
	if sent[0].Message.PlainText() != "done" {
		t.Fatalf("unexpected text: %q", sent[0].Message.PlainText())
	}
	if len(sent[0].Message.Attachments) != 1 {
		t.Fatalf("unexpected attachments: %d", len(sent[0].Message.Attachments))
	}
}

func TestQQOutboundStreamFinalPrefersBufferedVisibleText(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "visible "})); err != nil {
		t.Fatalf("push delta1: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "answer"})); err != nil {
		t.Fatalf("push delta2: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{Message: channel.Message{
			Text: "internal trace\nvisible answer",
		}},
	})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one send, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); got != "visible answer" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestQQOutboundStreamIgnoresLaterTextOnlyFinalAfterBufferedReply(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "visible answer"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push first final: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{Message: channel.Message{
			Text: "我需要按照用户的要求，在工具调用后完整复述。",
		}},
	})); err != nil {
		t.Fatalf("push second final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); got != "visible answer" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestQQOutboundStreamRejectsAfterClose(t *testing.T) {
	t.Parallel()

	stream := &qqOutboundStream{}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := stream.Push(context.Background(), preparedQQEvent(channel.StreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "x",
	})); err == nil {
		t.Fatal("expected closed error")
	}
}

func TestQQOutboundStreamErrorRedactsRegisteredTokenFragments(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)

	const token = "qq-token-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	redact.SetSecrets("test", token)
	prefixHalf := token[:len(token)/2]

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	err := stream.Push(context.Background(), preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventError, Error: "failed: " + prefixHalf}))
	if err != nil {
		t.Fatalf("push error: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected one outbound message, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); strings.Contains(got, prefixHalf) {
		t.Fatalf("expected redacted token fragment, got %q", got)
	}
}

func TestQQOutboundStreamC2CStreamsCumulativeShards(t *testing.T) {
	t.Parallel()

	var shards []qqStreamShardRequest
	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: 0,
		now:            time.Now,
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
		streamSend: func(_ context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
			shards = append(shards, req)
			return qqStreamShardResponse{ID: "sm-1"}, nil
		},
	}

	ctx := context.Background()
	for _, delta := range []string{"你好", "，世界"} {
		if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: delta})); err != nil {
			t.Fatalf("push delta: %v", err)
		}
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 0 {
		t.Fatalf("expected no legacy sends, got %d", len(sent))
	}
	if len(shards) != 3 {
		t.Fatalf("expected 3 shards, got %d", len(shards))
	}
	if shards[0].StreamMsgID != "" || shards[0].Index != 0 || shards[0].InputState != qqStreamInputGenerating {
		t.Fatalf("unexpected first shard: %+v", shards[0])
	}
	if shards[0].ContentRaw != "你好" || shards[1].ContentRaw != "你好，世界" {
		t.Fatalf("shards must carry cumulative text: %+v", shards)
	}
	for i, shard := range shards {
		if shard.Index != i {
			t.Fatalf("shard %d index = %d", i, shard.Index)
		}
		if i > 0 && shard.StreamMsgID != "sm-1" {
			t.Fatalf("shard %d must reuse server stream id: %+v", i, shard)
		}
	}
	if shards[2].InputState != qqStreamInputDone {
		t.Fatalf("final shard state = %d", shards[2].InputState)
	}
}

func TestQQOutboundStreamC2CShardFailureFallsBackToLegacySend(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: 0,
		now:            time.Now,
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
		streamSend: func(context.Context, qqStreamShardRequest) (qqStreamShardResponse, error) {
			return qqStreamShardResponse{}, errors.New("boom")
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "完整回复"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one legacy send, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); got != "完整回复" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestQQOutboundStreamC2CSingleShardForShortReply(t *testing.T) {
	t.Parallel()

	var shards []qqStreamShardRequest
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: time.Hour,
		now:            time.Now,
		send: func(context.Context, channel.PreparedOutboundMessage) error {
			t.Fatal("legacy send must not run")
			return nil
		},
		streamSend: func(_ context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
			shards = append(shards, req)
			return qqStreamShardResponse{ID: "sm-9"}, nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "短回复"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(shards) != 2 {
		t.Fatalf("expected 2 shards (immediate + final), got %d", len(shards))
	}
	if shards[0].InputState != qqStreamInputGenerating || shards[0].ContentRaw != "短回复" {
		t.Fatalf("unexpected first shard: %+v", shards[0])
	}
	if shards[1].InputState != qqStreamInputDone || shards[1].ContentRaw != "短回复" {
		t.Fatalf("unexpected final shard: %+v", shards[1])
	}
}

func TestQQOutboundStreamC2CThrottlesMiddleShards(t *testing.T) {
	t.Parallel()

	var shards []qqStreamShardRequest
	base := time.Now()
	current := base
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: time.Second,
		now:            func() time.Time { return current },
		send: func(context.Context, channel.PreparedOutboundMessage) error {
			t.Fatal("legacy send must not run")
			return nil
		},
		streamSend: func(_ context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
			shards = append(shards, req)
			return qqStreamShardResponse{ID: "sm-1"}, nil
		},
	}

	ctx := context.Background()
	push := func(delta string) {
		t.Helper()
		if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: delta})); err != nil {
			t.Fatalf("push delta: %v", err)
		}
	}
	push("一")
	push("二") // within interval: dropped
	current = base.Add(2 * time.Second)
	push("三")
	push("四") // within interval: dropped
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(shards) != 3 {
		t.Fatalf("expected 3 shards, got %d", len(shards))
	}
	if shards[1].ContentRaw != "一二三" {
		t.Fatalf("second shard must carry cumulative text: %q", shards[1].ContentRaw)
	}
	if shards[2].InputState != qqStreamInputDone || shards[2].ContentRaw != "一二三四" {
		t.Fatalf("final shard must complete full text: %+v", shards[2])
	}
}

func TestQQOutboundStreamCloseRetiresUnfinishedStream(t *testing.T) {
	t.Parallel()

	var shards []qqStreamShardRequest
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: 0,
		now:            time.Now,
		send: func(context.Context, channel.PreparedOutboundMessage) error {
			t.Fatal("legacy send must not run for a retired stream")
			return nil
		},
		streamSend: func(_ context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
			shards = append(shards, req)
			return qqStreamShardResponse{ID: "sm-1"}, nil
		},
	}

	// A turn that dies before StreamEventFinal: its context is already gone
	// by the time Close runs, which is exactly when the client would be left
	// rendering a permanently unfinished message.
	ctx, cancel := context.WithCancel(context.Background())
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "半截回复"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	cancel()
	if err := stream.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v, want context.Canceled", err)
	}

	if len(shards) != 2 {
		t.Fatalf("expected the delta shard plus a closing shard, got %d: %+v", len(shards), shards)
	}
	closing := shards[1]
	if closing.InputState != qqStreamInputDone {
		t.Fatalf("closing shard state = %d, want %d", closing.InputState, qqStreamInputDone)
	}
	if closing.ContentRaw != "半截回复" {
		t.Fatalf("closing shard must replay the last accepted content, got %q", closing.ContentRaw)
	}
	if closing.StreamMsgID != "sm-1" {
		t.Fatalf("closing shard must reuse the server stream id: %+v", closing)
	}
}

func TestQQOutboundStreamCloseSkipsRetireAfterNormalFinal(t *testing.T) {
	t.Parallel()

	var shards []qqStreamShardRequest
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: 0,
		now:            time.Now,
		send:           func(context.Context, channel.PreparedOutboundMessage) error { return nil },
		streamSend: func(_ context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
			shards = append(shards, req)
			return qqStreamShardResponse{ID: "sm-1"}, nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "完整回复"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}
	before := len(shards)
	if err := stream.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(shards) != before {
		t.Fatalf("a completed stream must not be retired again: %+v", shards[before:])
	}
}

func TestQQOutboundStreamFinalShardFailureRetiresStreamBeforeFallback(t *testing.T) {
	t.Parallel()

	var shards []qqStreamShardRequest
	var sent []channel.OutboundMessage
	finalFailed := false
	stream := &qqOutboundStream{
		target:         "c2c:user-openid",
		streamInterval: 0,
		now:            time.Now,
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
		streamSend: func(_ context.Context, req qqStreamShardRequest) (qqStreamShardResponse, error) {
			// The first attempt to close the stream fails, mirroring a
			// timeout on the last call of an otherwise healthy stream; the
			// retire shard that follows must still land.
			if req.InputState == qqStreamInputDone && !finalFailed {
				finalFailed = true
				return qqStreamShardResponse{}, errors.New("boom")
			}
			shards = append(shards, req)
			return qqStreamShardResponse{ID: "sm-1"}, nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "半截回复"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "，完整结尾"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	last := shards[len(shards)-1]
	if last.InputState != qqStreamInputDone {
		t.Fatalf("stream must be retired after a failed final shard, got %+v", shards)
	}
	if last.ContentRaw != "半截回复，完整结尾" {
		t.Fatalf("retire shard must replay the last accepted content, got %q", last.ContentRaw)
	}
	if len(sent) != 1 || sent[0].Message.PlainText() != "半截回复，完整结尾" {
		t.Fatalf("fallback must still carry the complete text, got %+v", sent)
	}
}
