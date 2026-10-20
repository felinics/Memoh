package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func burstChunkText(kind string, i int) string {
	return fmt.Sprintf("%s%d,", kind, i)
}

func burstExpectedText(kind string, chunks int) string {
	var sb strings.Builder
	for i := 0; i < chunks; i++ {
		sb.WriteString(burstChunkText(kind, i))
	}
	return sb.String()
}

// promptBurst writes chunks thought chunks and then chunks message chunks in a
// single write before answering, the way a backlog reaches Memoh when an
// Agent replays a session or flushes output it buffered.
func (*fakeACPAgent) promptBurst(p acp.PromptRequest, raw string) (acp.PromptResponse, error) {
	chunks, err := strconv.Atoi(raw)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	var burst []byte
	for _, update := range []struct {
		kind string
		of   func(string) acp.SessionUpdate
	}{{"r", acp.UpdateAgentThoughtText}, {"t", acp.UpdateAgentMessageText}} {
		for i := 0; i < chunks; i++ {
			line, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  acp.ClientMethodSessionUpdate,
				"params":  acp.SessionNotification{SessionId: p.SessionId, Update: update.of(burstChunkText(update.kind, i))},
			})
			if err != nil {
				return acp.PromptResponse{}, err
			}
			burst = append(append(burst, line...), '\n')
		}
	}
	if _, err := os.Stdout.Write(burst); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// slowConsumer stands for a live stream slower than the Agent, such as one
// whose every frame waits on a remote store.
type slowConsumer struct {
	delay time.Duration

	mu     sync.Mutex
	events []event.StreamEvent
}

func (c *slowConsumer) EmitStreamEvent(ev event.StreamEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	time.Sleep(c.delay)
}

// An Agent can write updates far faster than Memoh decodes them, and the live
// stream can be slower still. acp-go-sdk closes the connection once 1024
// notifications wait for its one handler goroutine, so the Agent must be held
// back rather than outrun it, and the slow stream must still receive the
// whole turn in order.
func TestPromptSurvivesUpdateBurst(t *testing.T) {
	const chunks = 5000 // per kind, far past acp-go-sdk's 1024-notification queue
	t.Setenv("MEMOH_ACP_FAKE_AGENT_BURST_CHUNKS", strconv.Itoa(chunks))
	runner, agentPath := newStartSessionTestRunner(t)
	sess, err := runner.StartSession(context.Background(), StartRequest{
		AgentID:     acpprofile.AgentACPID,
		BotID:       "bot-1",
		ProjectPath: "/data/project",
		Command:     agentPath,
		Timeout:     30 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	consumer := &slowConsumer{delay: time.Millisecond}
	sink := NewTurnSink(consumer, ToolOutputLimit{})
	result, err := sess.Prompt(context.Background(), "burst", sink)
	sink.Finish()
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var reasoning, text strings.Builder
	for _, ev := range consumer.events {
		switch ev.Type {
		case event.ReasoningDelta:
			if text.Len() > 0 {
				t.Fatal("reasoning delta after text started")
			}
			reasoning.WriteString(ev.Delta)
		case event.TextDelta:
			text.WriteString(ev.Delta)
		}
	}
	wantReasoning, wantText := burstExpectedText("r", chunks), burstExpectedText("t", chunks)
	if reasoning.String() != wantReasoning || text.String() != wantText || result.Text != wantText {
		t.Fatalf("stream holds %d+%d bytes and the turn %d bytes of text, want %d+%d", reasoning.Len(), text.Len(), len(result.Text), len(wantReasoning), len(wantText))
	}
}
