package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messageconv"
	"github.com/felinics/memoh/internal/models"
)

// usageTokens is the input-side shape every provider must report:
// input = no cache + cache read + cache write, total = input + output.
type usageTokens struct {
	input, output, total, noCache, cacheRead, cacheWrite int
	reported                                             bool
}

func usageTokensOf(u sdk.Usage) usageTokens {
	return usageTokens{
		input: u.InputTokens, output: u.OutputTokens, total: u.TotalTokens,
		noCache: u.InputTokenDetails.NoCacheTokens, cacheRead: u.InputTokenDetails.CacheReadTokens,
		cacheWrite: u.InputTokenDetails.CacheWriteTokens, reported: u.CacheReadTokensReported,
	}
}

func sumUsageTokens(all []usageTokens) usageTokens {
	sum := usageTokens{reported: len(all) > 0}
	for _, u := range all {
		sum.input += u.input
		sum.output += u.output
		sum.total += u.total
		sum.noCache += u.noCache
		sum.cacheRead += u.cacheRead
		sum.cacheWrite += u.cacheWrite
		sum.reported = sum.reported && u.reported
	}
	return sum
}

func anthropicUsageResponse(stream bool, usage string, outputTokens int, toolCall bool) string {
	content := `{"type":"text","text":"ok"}`
	stopReason := "end_turn"
	if toolCall {
		content = `{"type":"tool_use","id":"toolu_usage","name":"missing_tool","input":{}}`
		stopReason = "tool_use"
	}
	if !stream {
		return fmt.Sprintf(`{"id":"msg_usage","type":"message","role":"assistant","content":[%s],"stop_reason":%q,"usage":{%s,"output_tokens":%d}}`, content, stopReason, usage, outputTokens)
	}
	delta := `{"type":"text_delta","text":"ok"}`
	start := `{"type":"text","text":""}`
	if toolCall {
		delta = `{"type":"input_json_delta","partial_json":"{}"}`
		start = `{"type":"tool_use","id":"toolu_usage","name":"missing_tool","input":{}}`
	}
	return fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{%s,\"output_tokens\":0}}}\n\n", usage) +
		fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":%s}\n\n", start) +
		fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":%s}\n\n", delta) +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":%d}}\n\n", stopReason, outputTokens) +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func openAIResponsesUsageResponse(stream bool) string {
	response := `{"id":"resp_usage","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":310,"output_tokens":5,"total_tokens":315,"input_tokens_details":{"cached_tokens":200}}}`
	if !stream {
		return response
	}
	return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
}

type providerUsageTurn struct {
	clientType models.ClientType
	stream     bool
	respond    func(stream bool, call int) string
}

// run drives one native turn against the provider's real wire format and
// persists its messages the way history stores them, returning the usage of
// each stored assistant message.
func (p providerUsageTurn) run(t *testing.T, ctx context.Context, messages *messagepkg.DBService, botID, sessionID string) []usageTokens {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if p.stream {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		_, _ = fmt.Fprint(w, p.respond(p.stream, int(calls.Add(1))))
	}))
	defer server.Close()

	model := models.NewSDKChatModel(models.SDKModelConfig{ModelID: "usage-test", ClientType: string(p.clientType), APIKey: "fixture", BaseURL: server.URL})
	agent := native.New(native.Deps{})
	config := native.RunConfig{Model: model, Messages: []sdk.Message{sdk.UserMessage("hi")}}
	var output []sdk.Message
	var turnUsage sdk.Usage
	if p.stream {
		for ev := range agent.Stream(ctx, config) {
			switch ev.Type {
			case native.EventAgentAbort:
				t.Fatalf("native abort: %+v", ev)
			case native.EventAgentEnd:
				if err := json.Unmarshal(ev.Messages, &output); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(ev.Usage, &turnUsage); err != nil {
					t.Fatal(err)
				}
			}
		}
	} else {
		result, err := agent.Generate(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		output = result.Messages
		turnUsage = *result.Usage
	}

	var stored []usageTokens
	for _, msg := range messageconv.SDKMessagesToModelMessages(output) {
		saved, err := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, Role: msg.Role, Content: msg.Content, Usage: msg.Usage, RuntimeType: "model"})
		if err != nil {
			t.Fatal(err)
		}
		if saved.Role != "assistant" {
			continue
		}
		var usage sdk.Usage
		if err := json.Unmarshal(saved.Usage, &usage); err != nil {
			t.Fatal(err)
		}
		if usage.CachedInputTokens != usage.InputTokenDetails.CacheReadTokens {
			t.Fatalf("cached input = %d, cache read = %d", usage.CachedInputTokens, usage.InputTokenDetails.CacheReadTokens)
		}
		stored = append(stored, usageTokensOf(usage))
	}
	if int(calls.Load()) != len(stored) {
		t.Fatalf("model calls = %d, stored assistant messages = %d", calls.Load(), len(stored))
	}
	if got, want := usageTokensOf(turnUsage), sumUsageTokens(stored); got != want {
		t.Fatalf("turn usage = %+v, stored sum = %+v", got, want)
	}
	return stored
}

func newProviderUsageFixture(t *testing.T, ctx context.Context) (*dbsqlc.Queries, *messagepkg.DBService, string, string) {
	t.Helper()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	return queries, messagepkg.NewService(nil, postgresstore.NewQueriesWithPool(pool, queries)), botID, sessionID
}

// assertProviderUsageQueries reads stored usage back through the queries
// behind the session panel, /status, /context, and the usage page.
func assertProviderUsageQueries(t *testing.T, ctx context.Context, queries *dbsqlc.Queries, botIDText, sessionIDText string, stored []usageTokens) {
	t.Helper()
	want := sumUsageTokens(stored)
	sessionID, _ := dbpkg.ParseUUID(sessionIDText)
	botID, _ := dbpkg.ParseUUID(botIDText)
	from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}

	stats, err := queries.GetSessionCacheStats(ctx, sessionID)
	if err != nil || stats.TotalInputTokens != int64(want.input) || stats.CacheReadTokens != int64(want.cacheRead) {
		t.Fatalf("session cache stats = %+v, err = %v, want input %d read %d", stats, err, want.input, want.cacheRead)
	}
	latest, err := queries.GetLatestAssistantUsage(ctx, sessionID)
	if last := stored[len(stored)-1].input; err != nil || latest != int64(last) {
		t.Fatalf("latest assistant input = %d, err = %v, want %d", latest, err, last)
	}
	records, err := queries.ListTokenUsageRecords(ctx, dbsqlc.ListTokenUsageRecordsParams{BotID: botID, FromTime: from, ToTime: to, PageLimit: 10})
	if err != nil || len(records) != len(stored) {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
	var recordInput, recordRead int64
	var recordReported, storedReported int
	for _, r := range records {
		recordInput += r.InputTokens
		recordRead += r.CacheReadTokens
		if r.CacheReadTokensReported {
			recordReported++
		}
	}
	for _, u := range stored {
		if u.reported {
			storedReported++
		}
	}
	if recordInput != int64(want.input) || recordRead != int64(want.cacheRead) || recordReported != storedReported {
		t.Fatalf("records input %d read %d reported %d, want %d %d %d", recordInput, recordRead, recordReported, want.input, want.cacheRead, storedReported)
	}
	days, err := queries.GetTokenUsageByDayAndType(ctx, dbsqlc.GetTokenUsageByDayAndTypeParams{BotID: botID, FromTime: from, ToTime: to})
	if err != nil {
		t.Fatal(err)
	}
	var dayInput, dayRead int64
	dayReported := len(days) > 0
	for _, d := range days {
		dayInput += d.InputTokens
		dayRead += d.CacheReadTokens
		dayReported = dayReported && d.CacheReadTokensReported
	}
	if dayInput != int64(want.input) || dayRead != int64(want.cacheRead) || dayReported != want.reported {
		t.Fatalf("daily usage = %+v, want input %d read %d reported %t", days, want.input, want.cacheRead, want.reported)
	}
	byModel, err := queries.GetTokenUsageByModel(ctx, dbsqlc.GetTokenUsageByModelParams{BotID: botID, FromTime: from, ToTime: to})
	if err != nil {
		t.Fatal(err)
	}
	var modelInput int64
	for _, m := range byModel {
		modelInput += m.InputTokens
	}
	if modelInput != int64(want.input) {
		t.Fatalf("model usage = %+v, want input %d", byModel, want.input)
	}
}

func TestPostgresProviderUsageSurvivesPersistence(t *testing.T) {
	cases := []struct {
		name       string
		clientType models.ClientType
		respond    func(stream bool, call int) string
		messages   []usageTokens
	}{
		{
			name:       "anthropic",
			clientType: models.ClientTypeAnthropicMessages,
			respond: func(stream bool, _ int) string {
				return anthropicUsageResponse(stream, `"input_tokens":10,"cache_read_input_tokens":200,"cache_creation_input_tokens":100`, 5, false)
			},
			messages: []usageTokens{{input: 310, output: 5, total: 315, noCache: 10, cacheRead: 200, cacheWrite: 100, reported: true}},
		},
		{
			name:       "anthropic tool round",
			clientType: models.ClientTypeAnthropicMessages,
			respond: func(stream bool, call int) string {
				if call == 1 {
					return anthropicUsageResponse(stream, `"input_tokens":10,"cache_read_input_tokens":200,"cache_creation_input_tokens":100`, 5, true)
				}
				return anthropicUsageResponse(stream, `"input_tokens":20,"cache_read_input_tokens":310,"cache_creation_input_tokens":0`, 7, false)
			},
			messages: []usageTokens{
				{input: 310, output: 5, total: 315, noCache: 10, cacheRead: 200, cacheWrite: 100, reported: true},
				{input: 330, output: 7, total: 337, noCache: 20, cacheRead: 310, reported: true},
			},
		},
		{
			name:       "openai responses",
			clientType: models.ClientTypeOpenAIResponses,
			respond:    func(stream bool, _ int) string { return openAIResponsesUsageResponse(stream) },
			messages:   []usageTokens{{input: 310, output: 5, total: 315, noCache: 110, cacheRead: 200, reported: true}},
		},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				queries, messages, botID, sessionID := newProviderUsageFixture(t, ctx)
				stored := providerUsageTurn{clientType: tc.clientType, stream: stream, respond: tc.respond}.run(t, ctx, messages, botID, sessionID)
				if fmt.Sprint(stored) != fmt.Sprint(tc.messages) {
					t.Fatalf("stored assistant usage = %+v, want %+v", stored, tc.messages)
				}
				assertProviderUsageQueries(t, ctx, queries, botID, sessionID, stored)
			})
		}
	}
}

// One session mixing providers must aggregate to the weighted hit rate. The
// first turn carries the #1374 sample ratio, which reads as 1028.1% when the
// Anthropic input omits its cache.
func TestPostgresProviderUsageMixesProvidersInOneSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries, messages, botID, sessionID := newProviderUsageFixture(t, ctx)
	turns := []providerUsageTurn{
		{clientType: models.ClientTypeAnthropicMessages, stream: true, respond: func(stream bool, _ int) string {
			return anthropicUsageResponse(stream, `"input_tokens":21969,"cache_read_input_tokens":225856,"cache_creation_input_tokens":0`, 42, false)
		}},
		{clientType: models.ClientTypeOpenAIResponses, respond: func(stream bool, _ int) string { return openAIResponsesUsageResponse(stream) }},
		{clientType: models.ClientTypeAnthropicMessages, respond: func(stream bool, _ int) string {
			return anthropicUsageResponse(stream, `"input_tokens":10,"cache_read_input_tokens":200,"cache_creation_input_tokens":100`, 5, false)
		}},
	}
	var stored []usageTokens
	for _, turn := range turns {
		stored = append(stored, turn.run(t, ctx, messages, botID, sessionID)...)
	}
	want := []usageTokens{
		{input: 247825, output: 42, total: 247867, noCache: 21969, cacheRead: 225856, reported: true},
		{input: 310, output: 5, total: 315, noCache: 110, cacheRead: 200, reported: true},
		{input: 310, output: 5, total: 315, noCache: 10, cacheRead: 200, cacheWrite: 100, reported: true},
	}
	if fmt.Sprint(stored) != fmt.Sprint(want) {
		t.Fatalf("stored assistant usage = %+v, want %+v", stored, want)
	}
	assertProviderUsageQueries(t, ctx, queries, botID, sessionID, stored)

	sessionUUID, _ := dbpkg.ParseUUID(sessionID)
	stats, err := queries.GetSessionCacheStats(ctx, sessionUUID)
	if err != nil {
		t.Fatal(err)
	}
	if rate := fmt.Sprintf("%.1f%%", float64(stats.CacheReadTokens)/float64(stats.TotalInputTokens)*100); rate != "91.1%" {
		t.Fatalf("session cache hit rate = %s, want 91.1%%", rate)
	}
}
