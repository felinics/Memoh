package models_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
)

// formerTemplate is a chat model of a provider template Memoh shipped before
// models.dev, as kept in testdata.
type formerTemplate struct {
	Provider   string          `json:"provider"`
	ClientType string          `json:"client_type"`
	ModelID    string          `json:"model_id"`
	Config     json.RawMessage `json:"config"`
}

// Exercise the former OpenCode Go catalog, capability projection, resolver and
// real SDK serialization together: a selectable control must reach the
// advertised wire.
func TestOpenCodeGoCatalogReasoningWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/v1_chat_templates.json")
	if err != nil {
		t.Fatal(err)
	}
	var all []formerTemplate
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	const clientType = "opencode-go"
	catalog := slices.DeleteFunc(all, func(entry formerTemplate) bool {
		return entry.ClientType != clientType
	})
	if len(catalog) != 29 {
		t.Fatalf("catalog models = %d", len(catalog))
	}
	for _, entry := range catalog {
		t.Run(entry.ModelID, func(t *testing.T) {
			raw := entry.Config
			// Former templates are version 1 configs; read them the way a stored
			// row is read.
			config, err := models.DecodeModelConfig(raw, models.ConfigContext{ModelID: entry.ModelID, Type: models.ModelTypeChat})
			if err != nil {
				t.Fatal(err)
			}
			model := models.Model{ModelID: entry.ModelID, Config: config}
			opts := model.ReasoningOptions(clientType)
			if !opts.Supported {
				t.Fatal("reasoning capability missing")
			}
			efforts := slices.Clone(opts.Efforts)
			if opts.CanDisable {
				efforts = append(efforts, reasoning.EffortDisable)
			}
			if len(efforts) == 0 {
				efforts = []string{""}
			}
			for _, effort := range efforts {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/stream=%t", effort, stream), func(t *testing.T) {
						called := false
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							called = true
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Error(err)
								return
							}
							thinking, _ := body["thinking"].(map[string]any)
							output, _ := body["output_config"].(map[string]any)
							responseReasoning, _ := body["reasoning"].(map[string]any)
							wireEffort := body["reasoning_effort"]
							switch models.ResolveModelClientType(clientType, entry.ModelID) {
							case "openai-responses":
								if r.URL.Path != "/responses" {
									t.Errorf("path = %s", r.URL.Path)
								}
								wireEffort = responseReasoning["effort"]
							case "anthropic-messages":
								if r.URL.Path != "/messages" {
									t.Errorf("path = %s", r.URL.Path)
								}
								wireEffort = output["effort"]
							default:
								if r.URL.Path != "/chat/completions" {
									t.Errorf("path = %s", r.URL.Path)
								}
							}
							var wantEffort any
							if effort != "" && model.Config.ReasoningDialect() == reasoning.DialectTier {
								wantEffort = effort
								if effort == reasoning.EffortDisable {
									if model.Config.ReasoningOffSupport() == reasoning.OffSupportAccepted {
										wantEffort = nil
									} else {
										wantEffort = "none"
									}
								}
							}
							if wireEffort != wantEffort {
								t.Errorf("wire effort = %v, want %v", wireEffort, wantEffort)
							}
							if model.Config.ReasoningOffSupport() == reasoning.OffSupportAccepted {
								wantType := "enabled"
								if effort == reasoning.EffortDisable {
									wantType = "disabled"
								}
								if thinking["type"] != wantType {
									t.Errorf("thinking = %v, want %s", thinking, wantType)
								}
								var wantBudget any
								if model.Config.ReasoningDialect() == reasoning.DialectBudget && effort != reasoning.EffortDisable {
									wantBudget = map[string]float64{"low": 5000, "medium": 16000, "high": 50000}[effort]
								}
								if thinking["budget_tokens"] != wantBudget {
									t.Errorf("budget = %v, want %v", thinking["budget_tokens"], wantBudget)
								}
							} else if thinking != nil {
								t.Errorf("unexpected thinking flags: %v", thinking)
							}
							http.Error(w, "fixture rejection", http.StatusBadRequest)
						}))
						defer server.Close()
						cfg := models.SDKModelConfig{
							ModelID: entry.ModelID, ClientType: clientType, BaseURL: server.URL, HTTPClient: server.Client(),
							ReasoningConfig:  reasoning.ResolveConfig(model.ResolveThinkingMode(), model.Config.ReasoningEfforts(), opts, "", effort),
							ReasoningDialect: model.Config.ReasoningDialect(), ReasoningOffSupport: model.Config.ReasoningOffSupport(),
							ThinkingBudgetMin: model.Config.ThinkingBudgetMin(), ThinkingBudgetMax: model.Config.ThinkingBudgetMax(),
						}
						req := sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}
						models.ApplyReasoningToRequest(&req, cfg)
						chat := models.NewSDKChatModel(cfg)
						if stream {
							var response sdk.ModelStream
							response, err = chat.Stream(context.Background(), req)
							if err == nil {
								for range response.Parts {
								}
								_, err = response.Result()
							}
						} else {
							_, err = chat.Generate(context.Background(), req)
						}
						if err == nil || !called {
							t.Fatalf("wire not reached: called=%t error=%v", called, err)
						}
					})
				}
			}
			switch {
			case strings.HasPrefix(entry.ModelID, "gpt-"):
				if !slices.Equal(opts.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) || !opts.CanDisable {
					t.Fatalf("Luna options = %+v", opts)
				}
			case entry.ModelID == "longcat-2.0" || entry.ModelID == "minimax-m3":
				if !slices.Equal(opts.Efforts, []string{"enabled"}) || opts.DefaultEffort != "enabled" || !opts.CanDisable {
					t.Fatalf("toggle options = %+v", opts)
				}
			case entry.ModelID == "qwen3.8-max" || entry.ModelID == "qwen3.8-flash":
				if !slices.Equal(opts.Efforts, []string{"low", "medium", "xhigh"}) || !opts.CanDisable {
					t.Fatalf("Qwen options = %+v", opts)
				}
			}
		})
	}
}
