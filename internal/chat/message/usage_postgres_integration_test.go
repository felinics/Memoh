package message

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messageconv"
	"github.com/felinics/memoh/internal/models"
)

func TestPostgresProviderUsageSurvivesPersistence(t *testing.T) {
	for _, clientType := range []models.ClientType{models.ClientTypeAnthropicMessages, models.ClientTypeOpenAIResponses} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", clientType, stream), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				tx := beginPostgresMessageTestTx(t, ctx)
				setupPostgresMessageTestFixtures(t, ctx, tx)
				queries := sqlc.New(tx)
				svc := NewService(nil, postgresstore.NewQueries(queries))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					response := `{"id":"resp_usage","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":310,"output_tokens":5,"total_tokens":315,"input_tokens_details":{"cached_tokens":200}}}`
					if clientType == models.ClientTypeAnthropicMessages {
						response = `{"id":"msg_usage","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":200,"cache_creation_input_tokens":100}}`
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						if clientType == models.ClientTypeAnthropicMessages {
							response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + strings.Replace(response, `"output_tokens":5`, `"output_tokens":0`, 1) + "}\n\n" +
								"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
								"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
								"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
								"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
						} else {
							response = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
								"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
						}
					}
					if _, err := fmt.Fprint(w, response); err != nil {
						t.Errorf("write fixture: %v", err)
					}
				}))
				defer server.Close()
				model := models.NewSDKChatModel(models.SDKModelConfig{ModelID: "usage-test", ClientType: string(clientType), APIKey: "fixture", BaseURL: server.URL})
				opts := []sdk.GenerateOption{sdk.WithModel(model), sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}), sdk.WithMaxSteps(1)}
				var result *sdk.GenerateResult
				var err error
				if stream {
					var streamed *sdk.StreamResult
					streamed, err = sdk.StreamText(ctx, opts...)
					if err == nil {
						result, err = streamed.ToResult()
					}
				} else {
					result, err = sdk.GenerateTextResult(ctx, opts...)
				}
				if err != nil {
					t.Fatal(err)
				}
				messages := messageconv.SDKMessagesToModelMessages(result.Messages)
				var saved []Message
				for _, msg := range messages {
					if msg.Role != "assistant" {
						continue
					}
					persisted, err := svc.Persist(ctx, PersistInput{BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: msg.Role, Content: msg.Content, Usage: msg.Usage, RuntimeType: "model"})
					if err != nil {
						t.Fatal(err)
					}
					saved = append(saved, persisted)
				}
				if len(saved) != 1 {
					t.Fatalf("saved %d assistant messages, want 1", len(saved))
				}
				var usage sdk.Usage
				if err := json.Unmarshal(saved[0].Usage, &usage); err != nil {
					t.Fatal(err)
				}
				wantWrite := int64(0)
				if clientType == models.ClientTypeAnthropicMessages {
					wantWrite = 100
				}
				if usage.InputTokens != 310 || usage.OutputTokens != 5 || usage.TotalTokens != 315 || usage.InputTokenDetails.CacheReadTokens != 200 || int64(usage.InputTokenDetails.CacheWriteTokens) != wantWrite {
					t.Fatalf("persisted usage = %+v", usage)
				}
				sessionID, _ := dbpkg.ParseUUID(postgresMessageTestSessionID)
				botID, _ := dbpkg.ParseUUID(postgresMessageTestBotID)
				from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
				to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
				for range 2 {
					stats, err := queries.GetSessionCacheStats(ctx, sessionID)
					if err != nil || stats.TotalInputTokens != 310 || stats.CacheReadTokens != 200 {
						t.Fatalf("session stats = %+v, err = %v", stats, err)
					}
					latest, err := queries.GetLatestAssistantUsage(ctx, sessionID)
					if err != nil || latest != 310 {
						t.Fatalf("latest = %d, err = %v", latest, err)
					}
					records, err := queries.ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{BotID: botID, FromTime: from, ToTime: to, PageLimit: 10})
					if err != nil || len(records) != 1 || records[0].InputTokens != 310 {
						t.Fatalf("records = %+v, err = %v", records, err)
					}
					days, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botID, FromTime: from, ToTime: to})
					if err != nil || len(days) != 1 || days[0].InputTokens != 310 {
						t.Fatalf("days = %+v, err = %v", days, err)
					}
					byModel, err := queries.GetTokenUsageByModel(ctx, sqlc.GetTokenUsageByModelParams{BotID: botID, FromTime: from, ToTime: to})
					if err != nil || len(byModel) != 1 || byModel[0].InputTokens != 310 {
						t.Fatalf("models = %+v, err = %v", byModel, err)
					}
				}
			})
		}
	}
}
