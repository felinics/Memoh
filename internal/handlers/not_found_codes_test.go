package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/route"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/storage/providers/localfs"
	"github.com/felinics/memoh/internal/video"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// requireNotFoundCode asserts that err answers code with a 404 and a client
// fault, and that the handler kept a cause for the result record.
func requireNotFoundCode(t *testing.T, err error, code apperror.Code, wantCause bool) {
	t.Helper()
	got, _, fault := answered(t, err)
	if got != code || fault != apperror.FaultClient {
		t.Fatalf("answered %s %s, want %s client (error: %v)", got, fault, code, err)
	}
	if def, ok := apperror.Lookup(code); !ok || def.HTTPStatus != http.StatusNotFound {
		t.Fatalf("%s status = %d, want 404", code, def.HTTPStatus)
	}
	if wantCause && apperror.CauseOf(err) == nil {
		t.Fatalf("%s lost its cause", code)
	}
}

// lookupQueries answers every resource lookup with the same error, so a
// handler's classification can be asserted without a database.
type lookupQueries struct {
	dbstore.Queries
	err error
}

func (q lookupQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return sqlc.Provider{}, q.err
}

func (q lookupQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return sqlc.Model{}, q.err
}

func (q lookupQueries) GetSpeechModelWithProvider(context.Context, pgtype.UUID) (sqlc.GetSpeechModelWithProviderRow, error) {
	return sqlc.GetSpeechModelWithProviderRow{}, q.err
}

func (q lookupQueries) GetTranscriptionModelWithProvider(context.Context, pgtype.UUID) (sqlc.GetTranscriptionModelWithProviderRow, error) {
	return sqlc.GetTranscriptionModelWithProviderRow{}, q.err
}

func (q lookupQueries) GetVideoModelWithProvider(context.Context, pgtype.UUID) (sqlc.GetVideoModelWithProviderRow, error) {
	return sqlc.GetVideoModelWithProviderRow{}, q.err
}

func (q lookupQueries) GetFetchProviderByID(context.Context, pgtype.UUID) (sqlc.FetchProvider, error) {
	return sqlc.FetchProvider{}, q.err
}

func (q lookupQueries) GetSearchProviderByID(context.Context, pgtype.UUID) (sqlc.SearchProvider, error) {
	return sqlc.SearchProvider{}, q.err
}

// lookupCases is every handler that answers a lookup by resource id, with the
// code it reports for a row that is not there.
func lookupCases(err error) []struct {
	name string
	call func(echo.Context) error
	code apperror.Code
} {
	queries := lookupQueries{err: err}
	providersHandler := &ProvidersHandler{service: providers.NewService(slog.Default(), queries, ""), modelsService: models.NewService(slog.Default(), queries)}
	modelsHandler := &ModelsHandler{service: models.NewService(slog.Default(), queries)}
	audioHandler := &AudioHandler{service: audio.NewService(slog.Default(), queries, nil)}
	videoHandler := &VideoHandler{service: video.NewService(slog.Default(), queries, nil)}
	fetchHandler := &FetchProvidersHandler{service: fetchproviders.NewService(slog.Default(), queries)}
	searchHandler := &SearchProvidersHandler{service: searchproviders.NewService(slog.Default(), queries)}
	return []struct {
		name string
		call func(echo.Context) error
		code apperror.Code
	}{
		{"provider", providersHandler.Get, apperror.CodeProviderNotFound},
		{"model", modelsHandler.GetByID, apperror.CodeModelNotFound},
		{"speech provider", audioHandler.GetProvider, apperror.CodeTTSProviderNotFound},
		{"speech model", audioHandler.GetModel, apperror.CodeTTSModelNotFound},
		{"speech model capabilities", audioHandler.GetModelCapabilities, apperror.CodeTTSModelNotFound},
		{"transcription model", audioHandler.GetTranscriptionModel, apperror.CodeTranscriptionModelNotFound},
		{"transcription model capabilities", audioHandler.GetTranscriptionModelCapabilities, apperror.CodeTranscriptionModelNotFound},
		{"video provider", videoHandler.GetProvider, apperror.CodeVideoProviderNotFound},
		{"video model", videoHandler.GetModel, apperror.CodeVideoModelNotFound},
		{"fetch provider", fetchHandler.Get, apperror.CodeFetchProviderNotFound},
		{"search provider", searchHandler.Get, apperror.CodeSearchProviderNotFound},
	}
}

func resourceIDContext(id string) echo.Context {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues(id)
	return c
}

// A row that is not there is the caller's 404.
func TestResourceLookupsAnswerTheirNotFoundCodeForAMissingRow(t *testing.T) {
	for _, tc := range lookupCases(pgx.ErrNoRows) {
		t.Run(tc.name, func(t *testing.T) {
			requireNotFoundCode(t, tc.call(resourceIDContext(validUUID)), tc.code, true)
		})
	}
}

// A malformed id is a field problem, not a missing resource.
func TestResourceLookupsAnswerAFieldErrorForAMalformedID(t *testing.T) {
	for _, tc := range lookupCases(nil) {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(resourceIDContext("not-a-uuid"))
			code, args, fault := answered(t, err)
			if code != apperror.CodeRequestFieldInvalid || fault != apperror.FaultClient {
				t.Fatalf("answered %s %s, want %s client (error: %v)", code, fault, apperror.CodeRequestFieldInvalid, err)
			}
			if args["field"] != "id" {
				t.Fatalf("field = %q, want id", args["field"])
			}
		})
	}
}

// A lookup that fails for any other reason is a server fault, and the cause
// survives so the result record can name it.
func TestResourceLookupsKeepOtherFailuresAsServerFaults(t *testing.T) {
	cause := errors.New("connection reset by peer")
	for _, tc := range lookupCases(cause) {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(resourceIDContext(validUUID))
			code, _, fault := answered(t, err)
			if code != apperror.CodeInternal || fault != apperror.FaultServer {
				t.Fatalf("answered %s %s, want %s server (error: %v)", code, fault, apperror.CodeInternal, err)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("%v lost the failure that caused it", err)
			}
		})
	}
}

func TestProviderByNameAndTestAnswerProviderNotFound(t *testing.T) {
	// A lookup that finds no row answers the same code as an unparsable id.
	handler := &ProvidersHandler{service: providers.NewService(nil, providerNoRows{}, ""), modelsService: &models.Service{}}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues("11111111-1111-1111-1111-111111111111")
	requireNotFoundCode(t, handler.Test(c), apperror.CodeProviderNotFound, true)
}

type providerNoRows struct{ dbstore.Queries }

func (providerNoRows) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return sqlc.Provider{}, pgx.ErrNoRows
}

func TestNotFoundTranslatorsAnswerTheirCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code apperror.Code
	}{
		{"workspace target", workspaceTargetHTTPError(workspace.ErrWorkspaceTargetNotFound), apperror.CodeWorkspaceTargetNotFound},
		{"workspace file", fsHTTPError(bridge.ErrNotFound), apperror.CodeWorkspaceFileNotFound},
		{"access grant", (*BotUserAccessHandler)(nil).mapGrantError(bots.ErrGrantNotFound), apperror.CodeBotAccessGrantNotFound},
		{"bot of a grant", (*BotUserAccessHandler)(nil).mapGrantError(bots.ErrBotNotFound), apperror.CodeBotNotFound},
		{"user runtime", runtimeHTTPError(db.ErrNotFound), apperror.CodeUserRuntimeNotFound},
		{"channel config", sendChannelMessageHTTPError(channel.ErrChannelConfigNotFound, true), apperror.CodeChannelConfigNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNotFoundCode(t, tc.err, tc.code, true)
		})
	}
}

type botNoRows struct{ dbstore.Queries }

func (botNoRows) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{}, pgx.ErrNoRows
}

func TestBotAuthorizationAnswersBotNotFound(t *testing.T) {
	_, err := AuthorizeBotAccess(context.Background(), bots.NewService(nil, botNoRows{}), newTestAdminAccountService("user"), "user-1", "00000000-0000-0000-0000-0000000000b1")
	requireNotFoundCode(t, err, apperror.CodeBotNotFound, true)
}

type userNoRows struct{ dbstore.AccountStore }

func (userNoRows) GetByUserID(context.Context, string) (dbstore.AccountRecord, error) {
	return dbstore.AccountRecord{}, pgx.ErrNoRows
}

func TestGetUserAnswersUserNotFound(t *testing.T) {
	h := &UsersHandler{service: accounts.NewService(nil, userNoRows{})}
	c := testAuthContext(echo.New(), httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(), "user-1")
	c.SetParamNames("id")
	c.SetParamValues("user-1")
	requireNotFoundCode(t, h.GetUser(c), apperror.CodeUserNotFound, false)
}

func TestBrowserProxyAnswersBrowserSessionNotFound(t *testing.T) {
	h := &ContainerdHandler{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	c := echo.New().NewContext(req, httptest.NewRecorder())
	requireNotFoundCode(t, h.HandleBrowserProxy(c), apperror.CodeBrowserSessionNotFound, false)
}

func TestChannelIdentityConfigAnswersItsNotFoundCode(t *testing.T) {
	registry := channel.NewRegistry()
	registry.MustRegister(sentinelTestAdapter{})
	h := NewChannelHandler(channel.NewStore(bindingQueries{err: pgx.ErrNoRows}, registry), registry)
	c := testAuthContext(echo.New(), httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(), uuid.NewString())
	c.SetParamNames("platform")
	c.SetParamValues(string(sentinelTestChannelType))
	requireNotFoundCode(t, h.GetChannelIdentityConfig(c), apperror.CodeChannelIdentityConfigNotFound, true)
}

func TestPublicMediaAnswersMediaAssetNotFound(t *testing.T) {
	service := media.NewService(slog.Default(), localfs.New(filepath.Join(t.TempDir(), "media")))
	h := &PublicMediaHandler{logger: slog.Default(), media: service}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	_, _, err := h.openImage(c, "bot-1", strings.Repeat("a", 64))
	requireNotFoundCode(t, err, apperror.CodeMediaAssetNotFound, true)
}

func notFoundContainerdHandler() *ContainerdHandler {
	return &ContainerdHandler{
		botService:     bots.NewService(nil, newInvocationTestQueries()),
		accountService: newTestAdminAccountService("admin"),
	}
}

func notFoundBotContext(params map[string]string) echo.Context {
	c := testAuthContext(echo.New(), httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(), invocationTestOwnerID)
	names := make([]string, 0, len(params))
	values := make([]string, 0, len(params))
	for name, value := range params {
		names = append(names, name)
		values = append(values, value)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	return c
}

func TestWorkspaceSessionHandlersAnswerTheirNotFoundCode(t *testing.T) {
	h := notFoundContainerdHandler()
	botParams := map[string]string{"bot_id": invocationTestBotID, "session_id": "missing", "connection_id": "missing"}
	requireNotFoundCode(t, h.CloseDisplaySession(notFoundBotContext(botParams)), apperror.CodeDisplaySessionNotFound, false)
	requireNotFoundCode(t, h.KeepAliveBrowserSession(notFoundBotContext(botParams)), apperror.CodeBrowserSessionNotFound, false)
	requireNotFoundCode(t, h.HandleMCPStdio(notFoundBotContext(botParams)), apperror.CodeMCPConnectionNotFound, false)
}

type locateNoRows struct{ messagepkg.Service }

func (locateNoRows) LocateByExternalIDBySession(context.Context, string, string, int32, int32) (messagepkg.LocateResult, error) {
	return messagepkg.LocateResult{}, pgx.ErrNoRows
}

func TestLocateMessageAnswersMessageNotFound(t *testing.T) {
	queries := newInvocationTestQueries(invocationTestSession(invocationTestSessionA, invocationTestOwnerID))
	h := NewMessageHandler(slog.Default(), locateNoRows{}, newThreadServiceForTest(queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
	c := testAuthContext(echo.New(), httptest.NewRequest(http.MethodGet, "/?session_id="+invocationTestSessionA+"&external_message_id=m1", nil), httptest.NewRecorder(), invocationTestOwnerID)
	c.SetParamNames("bot_id")
	c.SetParamValues(invocationTestBotID)
	requireNotFoundCode(t, h.LocateMessage(c), apperror.CodeMessageNotFound, true)
}

type routeNoRows struct{ route.Service }

func (routeNoRows) GetByID(context.Context, string) (route.Route, error) {
	return route.Route{}, pgx.ErrNoRows
}

type unusedChannelRuntime struct{ channel.Runtime }

func TestSendChatAnswersChannelRouteNotFound(t *testing.T) {
	h := &UsersHandler{routeService: routeNoRows{}, channelRuntime: unusedChannelRuntime{}}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	c.Set("user", &jwt.Token{Valid: true, Claims: jwt.MapClaims{
		"typ": "chat_route", "bot_id": invocationTestBotID, "route_id": "route-1", "user_id": "user-1",
	}})
	c.SetParamNames("id")
	c.SetParamValues(invocationTestBotID)
	requireNotFoundCode(t, h.SendBotMessageSession(c), apperror.CodeChannelRouteNotFound, true)
}
