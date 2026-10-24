package main

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// A chat turn carries its own input to the model whether or not the session's
// timeline pipeline holds it: an IM message is projected into the pipeline
// before its turn starts, a Web send into the same session is not, and each
// request must carry the text it was sent exactly once.
func TestPostgresChatInputReachesTheModelWithAndWithoutThePipeline(t *testing.T) {
	f := newBacklogFixture(t)
	if _, err := f.pool.Exec(f.ctx, `UPDATE bot_sessions SET type='chat', channel_type='telegram' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	f.service.SetPipeline(pipeline)
	for _, entry := range []struct {
		name, channel, text string
		projected           bool
	}{
		{name: "im", channel: "telegram", text: "IM_INPUT_KEEP_EXACT", projected: true},
		{name: "web", channel: "local", text: "WEB_INPUT_KEEP_EXACT"},
	} {
		cmd := turn.StartTurnCommand{TeamID: f.cfg.TeamID, Mode: turn.ModeChat, BotID: f.botID, ChatID: f.botID, ThreadID: f.sessionID, Query: entry.text, CurrentChannel: entry.channel}
		if entry.projected {
			cmd.ExternalMessageID = uuid.NewString()
			at := time.Now().UnixMilli()
			pipeline.PushEvent(f.sessionID, timeline.MessageEvent{
				SessionID: f.sessionID, MessageID: cmd.ExternalMessageID, ReceivedAtMs: at, TimestampSec: at / 1000,
				Content: []timeline.ContentNode{{Type: "text", Text: entry.text}}, Conversation: timeline.ConversationMeta{Channel: entry.channel, ConversationType: "private"},
			})
		}
		calls := f.modelCalls.Load()
		handle, err := f.service.StartTurn(f.ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %v", entry.name, err)
		}
		for range handle.Events() {
		}
		if err := <-handle.Errs(); err != nil {
			t.Fatalf("%s: %v", entry.name, err)
		}
		request, _ := f.lastRequest.Load().(string)
		if f.modelCalls.Load() != calls+1 || strings.Count(request, entry.text) != 1 {
			t.Fatalf("%s: provider_calls=%d input_in_request=%d, want one call carrying the input once", entry.name, f.modelCalls.Load()-calls, strings.Count(request, entry.text))
		}
	}
}
