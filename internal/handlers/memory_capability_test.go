package handlers

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

type memoryCapabilityQueries struct {
	dbstore.Queries
	bot              sqlc.GetBotByIDRow
	settings         sqlc.GetSettingsByBotIDRow
	aclDefaultEffect string
}

func (q *memoryCapabilityQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (*memoryCapabilityQueries) GetContainerByBotID(context.Context, pgtype.UUID) (sqlc.Container, error) {
	return sqlc.Container{}, pgx.ErrNoRows
}

func (q *memoryCapabilityQueries) GetSettingsByBotID(_ context.Context, _ pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return q.settings, nil
}

func (q *memoryCapabilityQueries) GetBotACLDefaultEffect(_ context.Context, _ pgtype.UUID) (string, error) {
	if q.aclDefaultEffect != "" {
		return q.aclDefaultEffect, nil
	}
	return "allow", nil
}

type unsupportedCompactProvider struct{}

func (*unsupportedCompactProvider) Type() string { return "external" }

func (*unsupportedCompactProvider) OnBeforeChat(context.Context, memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	return nil, nil
}

func (*unsupportedCompactProvider) OnAfterChat(context.Context, memprovider.AfterChatRequest) error {
	return nil
}

func (*unsupportedCompactProvider) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return nil, nil
}

func (*unsupportedCompactProvider) CallTool(context.Context, mcp.ToolSessionContext, string, map[string]any) (map[string]any, error) {
	return nil, nil
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

func (*unsupportedCompactProvider) Update(context.Context, memprovider.UpdateRequest) (memprovider.MemoryItem, error) {
	return memprovider.MemoryItem{}, nil
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

	botRow := testBotRow(botID, map[string]any{})
	botRow.OwnerUserID = testUUID(userID)
	botRow.Status = bots.BotStatusReady
	queries := &memoryCapabilityQueries{bot: botRow}
	handler := NewMemoryHandler(
		slog.Default(),
		bots.NewService(slog.Default(), queries),
		accounts.NewService(slog.Default(), testAdminAccountStore{role: "admin"}),
	)
	handler.SetMemoryRegistry(registry)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/"+botID+"/memory/compact", bytes.NewBufferString(`{"ratio":0.5}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
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

	botRow := testBotRow(botID, map[string]any{})
	botRow.OwnerUserID = testUUID(userID)
	botRow.Status = bots.BotStatusReady
	queries := &memoryCapabilityQueries{bot: botRow}
	handler := NewMemoryHandler(
		slog.Default(),
		bots.NewService(slog.Default(), queries),
		accounts.NewService(slog.Default(), testAdminAccountStore{role: "admin"}),
	)
	handler.SetMemoryRegistry(registry)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/memory/status", nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
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

	botRow := testBotRow(botID, map[string]any{})
	botRow.OwnerUserID = testUUID(userID)
	botRow.Status = bots.BotStatusReady
	queries := &memoryCapabilityQueries{
		bot: botRow,
		settings: sqlc.GetSettingsByBotIDRow{
			BotID:            testUUID(botID),
			MemoryProviderID: testUUID(missingProviderID),
		},
	}
	handler := NewMemoryHandler(
		slog.Default(),
		bots.NewService(slog.Default(), queries),
		accounts.NewService(slog.Default(), testAdminAccountStore{role: "admin"}),
	)
	handler.SetMemoryRegistry(registry)
	handler.SetSettingsService(settings.NewService(slog.Default(), queries, nil, nil))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/memory/status", nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
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
