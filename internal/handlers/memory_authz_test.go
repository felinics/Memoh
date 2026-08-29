package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
)

// recordingMemoryProvider records write calls so tests can assert the handler
// blocked (or allowed) the provider invocation.
type recordingMemoryProvider struct {
	unsupportedCompactProvider
	updateCalls      []string
	deleteCalls      []string
	deleteBatchCalls [][]string
}

func (p *recordingMemoryProvider) Update(_ context.Context, req memprovider.UpdateRequest) (memprovider.MemoryItem, error) {
	p.updateCalls = append(p.updateCalls, req.MemoryID)
	return memprovider.MemoryItem{ID: req.MemoryID}, nil
}

func (p *recordingMemoryProvider) Delete(_ context.Context, memoryID string) (memprovider.DeleteResponse, error) {
	p.deleteCalls = append(p.deleteCalls, memoryID)
	return memprovider.DeleteResponse{}, nil
}

func (p *recordingMemoryProvider) DeleteBatch(_ context.Context, memoryIDs []string) (memprovider.DeleteResponse, error) {
	p.deleteBatchCalls = append(p.deleteBatchCalls, memoryIDs)
	return memprovider.DeleteResponse{}, nil
}

func newMemoryAuthzHandler(t *testing.T, botID, userID string) (*MemoryHandler, *recordingMemoryProvider) {
	t.Helper()
	provider := &recordingMemoryProvider{}
	registry := memprovider.NewRegistry(slog.Default())
	registry.Register(defaultBuiltinProviderID, provider)

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
	return handler, provider
}

func requireForbidden(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected forbidden error, got nil")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected echo HTTP error, got %T", err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", httpErr.Code)
	}
}

func TestMemoryItemRoutesDecodeEscapedMemoryID(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	for _, tt := range []struct {
		name            string
		method          string
		escapedMemoryID string
		memoryID        string
		body            string
		decodedPath     bool
	}{
		{
			name:            "update",
			method:          http.MethodPut,
			escapedMemoryID: botID + "%3Amem_123",
			memoryID:        botID + ":mem_123",
			body:            `{"memory":"updated"}`,
		},
		{
			name:            "delete",
			method:          http.MethodDelete,
			escapedMemoryID: botID + "%3Amem_123",
			memoryID:        botID + ":mem_123",
		},
		{
			name:            "update literal percent triplet",
			method:          http.MethodPut,
			escapedMemoryID: botID + "%3Amem%253Afoo",
			memoryID:        botID + ":mem%3Afoo",
			body:            `{"memory":"updated"}`,
		},
		{
			name:            "delete literal percent triplet",
			method:          http.MethodDelete,
			escapedMemoryID: botID + "%3Amem%253Afoo",
			memoryID:        botID + ":mem%3Afoo",
		},
		{
			name:            "update decoded literal percent triplet",
			method:          http.MethodPut,
			escapedMemoryID: botID + "%3Amem%253Afoo",
			memoryID:        botID + ":mem%3Afoo",
			body:            `{"memory":"updated"}`,
			decodedPath:     true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, provider := newMemoryAuthzHandler(t, botID, userID)
			e := echo.New()
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("user", &jwt.Token{
						Valid: true,
						Claims: jwt.MapClaims{
							"sub":     userID,
							"user_id": userID,
						},
					})
					return next(c)
				}
			})
			handler.Register(e)

			req := httptest.NewRequest(tt.method, "/bots/"+botID+"/memory/"+tt.escapedMemoryID, bytes.NewBufferString(tt.body))
			if tt.decodedPath {
				req.URL.Path = "/bots/" + botID + "/memory/" + tt.memoryID
				req.URL.RawPath = ""
			}
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s encoded memory id %q status = %d, want 200; body = %s", tt.method, tt.memoryID, rec.Code, rec.Body.String())
			}
			if tt.method == http.MethodPut && (len(provider.updateCalls) != 1 || provider.updateCalls[0] != tt.memoryID) {
				t.Fatalf("provider.Update calls = %v, want [%s]", provider.updateCalls, tt.memoryID)
			}
			if tt.method == http.MethodDelete && (len(provider.deleteCalls) != 1 || provider.deleteCalls[0] != tt.memoryID) {
				t.Fatalf("provider.Delete calls = %v, want [%s]", provider.deleteCalls, tt.memoryID)
			}
		})
	}
}

func TestChatDeleteOneRejectsForeignBotMemoryID(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	otherBotID := "33333333-3333-3333-3333-333333333333"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, provider := newMemoryAuthzHandler(t, botID, userID)

	memID := otherBotID + ":mem_123"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/bots/"+botID+"/memory/"+memID, nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/:memory_id")
	echoCtx.SetParamNames("bot_id", "memory_id")
	echoCtx.SetParamValues(botID, memID)

	requireForbidden(t, handler.ChatDeleteOne(echoCtx))
	if len(provider.deleteCalls) != 0 {
		t.Fatalf("provider.Delete must not be called, got %v", provider.deleteCalls)
	}
}

func TestChatDeleteOneAllowsOwnBotMemoryID(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, provider := newMemoryAuthzHandler(t, botID, userID)

	memID := botID + ":mem_123"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/bots/"+botID+"/memory/"+memID, nil)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/:memory_id")
	echoCtx.SetParamNames("bot_id", "memory_id")
	echoCtx.SetParamValues(botID, memID)

	if err := handler.ChatDeleteOne(echoCtx); err != nil {
		t.Fatalf("ChatDeleteOne returned error: %v", err)
	}
	if len(provider.deleteCalls) != 1 || provider.deleteCalls[0] != memID {
		t.Fatalf("provider.Delete calls = %v, want [%s]", provider.deleteCalls, memID)
	}
}

func TestChatUpdateRejectsForeignBotMemoryID(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	otherBotID := "33333333-3333-3333-3333-333333333333"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, provider := newMemoryAuthzHandler(t, botID, userID)

	memID := otherBotID + ":mem_123"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/bots/"+botID+"/memory/"+memID, bytes.NewBufferString(`{"memory":"updated"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/:memory_id")
	echoCtx.SetParamNames("bot_id", "memory_id")
	echoCtx.SetParamValues(botID, memID)

	requireForbidden(t, handler.ChatUpdate(echoCtx))
	if len(provider.updateCalls) != 0 {
		t.Fatalf("provider.Update must not be called, got %v", provider.updateCalls)
	}
}

func TestChatUpdateAllowsOwnBotMemoryID(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, provider := newMemoryAuthzHandler(t, botID, userID)

	memID := botID + ":mem_123"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/bots/"+botID+"/memory/"+memID, bytes.NewBufferString(`{"memory":"updated"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory/:memory_id")
	echoCtx.SetParamNames("bot_id", "memory_id")
	echoCtx.SetParamValues(botID, memID)

	if err := handler.ChatUpdate(echoCtx); err != nil {
		t.Fatalf("ChatUpdate returned error: %v", err)
	}
	if len(provider.updateCalls) != 1 || provider.updateCalls[0] != memID {
		t.Fatalf("provider.Update calls = %v, want [%s]", provider.updateCalls, memID)
	}
}

func TestChatDeleteBatchRejectsWhenAnyIDBelongsToAnotherBot(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	otherBotID := "33333333-3333-3333-3333-333333333333"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, provider := newMemoryAuthzHandler(t, botID, userID)

	body := `{"memory_ids":["` + botID + `:mem_1","` + otherBotID + `:mem_2"]}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/bots/"+botID+"/memory", bytes.NewBufferString(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory")
	echoCtx.SetParamNames("bot_id")
	echoCtx.SetParamValues(botID)

	requireForbidden(t, handler.ChatDelete(echoCtx))
	if len(provider.deleteBatchCalls) != 0 {
		t.Fatalf("provider.DeleteBatch must not be called, got %v", provider.deleteBatchCalls)
	}
}

func TestChatDeleteBatchAllowsOwnBotMemoryIDs(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	handler, provider := newMemoryAuthzHandler(t, botID, userID)

	body := `{"memory_ids":["` + botID + `:mem_1","` + botID + `:mem_2"]}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/bots/"+botID+"/memory", bytes.NewBufferString(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	echoCtx := testAuthContext(e, req, rec, userID)
	echoCtx.SetPath("/bots/:bot_id/memory")
	echoCtx.SetParamNames("bot_id")
	echoCtx.SetParamValues(botID)

	if err := handler.ChatDelete(echoCtx); err != nil {
		t.Fatalf("ChatDelete returned error: %v", err)
	}
	if len(provider.deleteBatchCalls) != 1 || len(provider.deleteBatchCalls[0]) != 2 {
		t.Fatalf("provider.DeleteBatch calls = %v, want one call with 2 ids", provider.deleteBatchCalls)
	}
}
