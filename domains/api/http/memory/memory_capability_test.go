package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/iam/account"
	memorydomain "github.com/memohai/memoh/domains/memory"
	memprovider "github.com/memohai/memoh/domains/memory/registry"
)

type memoryCapabilityQueries struct {
	bot      bot.Record
	settings setting.Record
}

type memorySettingsStore struct {
	setting.Store
	record setting.Record
	bot    setting.BotRecord
}

func (s *memorySettingsStore) Get(context.Context, string) (setting.Record, error) {
	return s.record, nil
}

func (s *memorySettingsStore) GetBot(context.Context, string) (setting.BotRecord, error) {
	return s.bot, nil
}

type unsupportedCompactProvider struct{}

func (*unsupportedCompactProvider) Type() string { return "external" }

func (*unsupportedCompactProvider) OnBeforeChat(context.Context, memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	return nil, nil
}

func (*unsupportedCompactProvider) OnAfterChat(context.Context, memprovider.AfterChatRequest) error {
	return nil
}

func (*unsupportedCompactProvider) Add(context.Context, memprovider.AddRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, nil
}

func (*unsupportedCompactProvider) Search(context.Context, memprovider.SearchRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, nil
}

func (*unsupportedCompactProvider) GetAll(context.Context, memprovider.GetAllRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, nil
}

func (*unsupportedCompactProvider) Update(context.Context, memprovider.UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, nil
}

func (*unsupportedCompactProvider) Delete(context.Context, string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, nil
}

func (*unsupportedCompactProvider) DeleteBatch(context.Context, []string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, nil
}

func (*unsupportedCompactProvider) DeleteAll(context.Context, memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, nil
}

func (*unsupportedCompactProvider) Compact(context.Context, map[string]any, float64, int) (memprovider.CompactResult, error) {
	return memprovider.CompactResult{}, errors.New("compact should not be called without semantic capability")
}

func (*unsupportedCompactProvider) Usage(context.Context, map[string]any) (memprovider.UsageResponse, error) {
	return memprovider.UsageResponse{}, nil
}

func (*unsupportedCompactProvider) Status(context.Context, string) (memprovider.MemoryStatusResponse, error) {
	return memprovider.MemoryStatusResponse{ProviderType: "external"}, nil
}

func (*unsupportedCompactProvider) Rebuild(context.Context, string) (memprovider.RebuildResult, error) {
	return memprovider.RebuildResult{}, nil
}

func TestChatCompactReturnsNotImplementedWhenProviderDoesNotSupportSemanticCompact(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	registry := memprovider.NewRegistry(slog.Default())
	registry.Register(defaultBuiltinProviderID, &unsupportedCompactProvider{})

	botRow := httpfixture.BotRow(botID, map[string]any{})
	botRow.OwnerUserID = userID
	botRow.Status = bot.BotStatusReady
	queries := &memoryCapabilityQueries{bot: botRow}
	handler := NewMemoryHandler(
		slog.Default(),
		httpfixture.NewBotService(slog.Default(), httpfixture.NewBotStore(queries.bot)),
		account.NewService(slog.Default(), httpfixture.AdminAccountStore{Role: "admin"}),
	)
	handler.SetMemoryRegistry(registry)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/"+botID+"/memory/compact", bytes.NewBufferString(`{"ratio":0.5}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := httpfixture.AuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/compact")
	echoCtx.SetParamNames("bot_id")
	echoCtx.SetParamValues(botID)

	err := handler.ChatCompact(echoCtx)
	if err == nil {
		t.Fatal("expected unsupported semantic compact error")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected echo HTTP error, got %T", err)
	}
	if httpErr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", httpErr.Code)
	}
	if !strings.Contains(httpErr.Message.(string), "semantic compact") {
		t.Fatalf("unexpected error message: %v", httpErr.Message)
	}
}

func TestChatStatusIncludesSemanticCompactCapability(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	registry := memprovider.NewRegistry(slog.Default())
	registry.Register(defaultBuiltinProviderID, &unsupportedCompactProvider{})

	botRow := httpfixture.BotRow(botID, map[string]any{})
	botRow.OwnerUserID = userID
	botRow.Status = bot.BotStatusReady
	queries := &memoryCapabilityQueries{bot: botRow}
	handler := NewMemoryHandler(
		slog.Default(),
		httpfixture.NewBotService(slog.Default(), httpfixture.NewBotStore(queries.bot)),
		account.NewService(slog.Default(), httpfixture.AdminAccountStore{Role: "admin"}),
	)
	handler.SetMemoryRegistry(registry)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/memory/status", nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := httpfixture.AuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/status")
	echoCtx.SetParamNames("bot_id")
	echoCtx.SetParamValues(botID)

	if err := handler.ChatStatus(echoCtx); err != nil {
		t.Fatalf("ChatStatus returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("ChatStatus status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var status memprovider.MemoryStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.Compact.Semantic {
		t.Fatalf("semantic compact should be unavailable: %+v", status.Compact)
	}
	if !strings.Contains(status.Compact.Reason, "semantic compact") {
		t.Fatalf("unexpected compact capability reason: %+v", status.Compact)
	}
}

func TestChatStatusDoesNotFallbackToBuiltinWhenConfiguredProviderIsUnavailable(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	missingProviderID := "22222222-2222-2222-2222-222222222222"
	registry := memprovider.NewRegistry(slog.Default())
	registry.Register(defaultBuiltinProviderID, &unsupportedCompactProvider{})

	botRow := httpfixture.BotRow(botID, map[string]any{})
	botRow.OwnerUserID = userID
	botRow.Status = bot.BotStatusReady
	queries := &memoryCapabilityQueries{
		bot: botRow,
		settings: setting.Record{
			MemoryProviderID: missingProviderID,
		},
	}
	handler := NewMemoryHandler(
		slog.Default(),
		httpfixture.NewBotService(slog.Default(), httpfixture.NewBotStore(queries.bot)),
		account.NewService(slog.Default(), httpfixture.AdminAccountStore{Role: "admin"}),
	)
	handler.SetMemoryRegistry(registry)
	handler.SetSettingsService(httpfixture.NewSettingsService(slog.Default(), &memorySettingsStore{
		record: setting.Record{MemoryProviderID: missingProviderID},
		bot:    setting.BotRecord{OwnerUserID: userID},
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/memory/status", nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := httpfixture.AuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/status")
	echoCtx.SetParamNames("bot_id")
	echoCtx.SetParamValues(botID)

	err := handler.ChatStatus(echoCtx)
	if err == nil {
		t.Fatal("expected configured provider lookup error")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected echo HTTP error, got %T", err)
	}
	if httpErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", httpErr.Code)
	}
}
