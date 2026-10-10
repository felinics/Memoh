package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	message "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messageconv"
	"github.com/felinics/memoh/internal/models"
)

// relayFailedStream has the shape of the Responses stream llm-relay emits when
// an Anthropic upstream fails after message_start reported 10 input, 200 cache
// read, 100 cache write and 1 output token. Relay settles that usage itself;
// the native loop must keep the call failed and must not turn it into a
// committed step. Each case picks the error code for its retry classification,
// not for being Relay's verbatim output: Relay forwards Anthropic error types,
// and OpenAI's server_error in the retried case is not one of them.
func relayFailedStream(code string) string {
	return "event: response.created\ndata: {\"response\":{\"created_at\":946684800,\"id\":\"resp_msg_failure\",\"model\":\"review-model\",\"object\":\"response\",\"output\":[],\"status\":\"in_progress\"},\"type\":\"response.created\"}\n\n" +
		"event: response.output_item.added\ndata: {\"item\":{\"type\":\"message\",\"id\":\"msg_msg_failure_0\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}]},\"output_index\":0,\"type\":\"response.output_item.added\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"content_index\":0,\"delta\":\"hello\",\"item_id\":\"msg_msg_failure_0\",\"output_index\":0,\"type\":\"response.output_text.delta\"}\n\n" +
		"event: response.failed\ndata: {\"response\":{\"id\":\"resp_msg_failure\",\"object\":\"response\",\"created_at\":946684800,\"status\":\"failed\",\"model\":\"review-model\",\"output\":[{\"type\":\"message\",\"id\":\"msg_msg_failure_0\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":310,\"output_tokens\":1,\"total_tokens\":311,\"input_tokens_details\":{\"cached_tokens\":200,\"cache_write_tokens\":100}},\"error\":{\"code\":\"" + code + "\",\"message\":\"fixture (request id: fixture)\"}},\"type\":\"response.failed\"}\n\n"
}

const relayCompletedStream = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retry\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":400,\"output_tokens\":5,\"total_tokens\":405,\"input_tokens_details\":{\"cached_tokens\":300}}}}\n\n"

// Native usage statistics count committed model calls only. A failed call's
// known usage reaches the SDK finish part and is settled by Relay, but it is
// neither committed as a step nor persisted with the turn.
func TestPostgresProviderUsageExcludesFailedAttempts(t *testing.T) {
	for _, tt := range []struct {
		name     string
		streams  []string
		terminal native.StreamEventType
		retries  int
		input    int
		read     int
	}{
		{name: "failure", streams: []string{relayFailedStream("invalid_request_error")}, terminal: native.EventAgentAbort},
		{name: "retried failure", streams: []string{relayFailedStream("server_error"), relayCompletedStream}, terminal: native.EventAgentEnd, retries: 1, input: 400, read: 300},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx := beginUsagePostgresTx(t, ctx)
			setupUsagePostgresFixtures(t, ctx, tx)
			queries := sqlc.New(tx)
			svc := message.NewService(nil, postgresstore.NewQueries(queries))
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				call := int(calls.Add(1))
				if call > len(tt.streams) {
					t.Errorf("unexpected provider call %d", call)
					http.Error(w, "unexpected call", http.StatusTeapot)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tt.streams[call-1]))
			}))
			defer server.Close()

			model := models.NewSDKChatModel(models.SDKModelConfig{ModelID: "usage-test", ClientType: string(models.ClientTypeOpenAIResponses), APIKey: "fixture", BaseURL: server.URL})
			assertFailedCallUsage(t, ctx, tt.streams[0])
			var terminal native.StreamEvent
			var stepEnds, retries int
			for ev := range native.New(native.Deps{}).Stream(ctx, native.RunConfig{Model: model, Messages: []sdk.Message{sdk.UserMessage("hi")}}) {
				switch {
				case ev.Type == native.EventStepEnd:
					stepEnds++
				case ev.Type == native.EventRetry:
					retries++
				case ev.IsTerminal():
					terminal = ev
				}
			}
			if terminal.Type != tt.terminal || int(calls.Load()) != len(tt.streams) || retries != tt.retries || stepEnds != len(tt.streams)-1 {
				t.Fatalf("terminal=%q calls=%d retries=%d stepEnds=%d", terminal.Type, calls.Load(), retries, stepEnds)
			}
			var usage sdk.Usage
			if err := json.Unmarshal(terminal.Usage, &usage); err != nil {
				t.Fatal(err)
			}
			if usage.InputTokens != tt.input || usage.InputTokenDetails.CacheReadTokens != tt.read || usage.CacheReadTokensReported != (tt.input > 0) {
				t.Fatalf("run usage = %s, want only committed calls", terminal.Usage)
			}
			var output []sdk.Message
			if err := json.Unmarshal(terminal.Messages, &output); err != nil {
				t.Fatal(err)
			}
			for _, msg := range messageconv.SDKMessagesToModelMessages(output) {
				if strings.Contains(string(msg.Content), "hello") {
					t.Fatalf("failed attempt output became history: %s", msg.Content)
				}
				if _, err := svc.Persist(ctx, message.PersistInput{BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: msg.Role, Content: msg.Content, Usage: msg.Usage, RuntimeType: "model"}); err != nil {
					t.Fatal(err)
				}
			}
			botID, _ := dbpkg.ParseUUID(postgresMessageTestBotID)
			days, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botID, FromTime: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, ToTime: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.input == 0 {
				if len(days) != 0 {
					t.Fatalf("failed call reached usage statistics: %+v", days)
				}
				return
			}
			if len(days) != 1 || days[0].InputTokens != int64(tt.input) || days[0].CacheReadTokens != int64(tt.read) || !days[0].CacheReadTokensReported {
				t.Fatalf("daily usage = %+v, want the committed call only", days)
			}
		})
	}
}

func assertFailedCallUsage(t *testing.T, ctx context.Context, stream string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()
	model := models.NewSDKChatModel(models.SDKModelConfig{ModelID: "usage-test", ClientType: string(models.ClientTypeOpenAIResponses), APIKey: "fixture", BaseURL: server.URL})
	parts, err := model.Provider.DoStream(ctx, sdk.Request{Model: model.ID, Messages: []sdk.Message{sdk.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var failed, stepEnds int
	var finish *sdk.FinishPart
	for part := range parts {
		switch part := part.(type) {
		case *sdk.ErrorPart:
			failed++
		case *sdk.FinishStepPart:
			stepEnds++
		case *sdk.FinishPart:
			finish = part
		}
	}
	if failed != 1 || stepEnds != 0 || finish == nil {
		t.Fatalf("errors=%d stepEnds=%d finish=%v", failed, stepEnds, finish)
	}
	got := finish.TotalUsage
	if got.InputTokens != 310 || got.OutputTokens != 1 || got.InputTokenDetails.CacheReadTokens != 200 || got.InputTokenDetails.CacheWriteTokens != 100 || !got.CacheReadTokensReported {
		t.Fatalf("failed call usage = %+v", got)
	}
}
