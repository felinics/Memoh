package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
)

// requireFieldError asserts that err answers with code naming field.
func requireFieldError(t *testing.T, err error, code apperror.Code, field string) {
	t.Helper()
	if got := apperror.CodeOf(err); got != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
	if got := apperror.ArgsOf(err)["field"]; got != field {
		t.Fatalf("field = %q, want %q", got, field)
	}
}

func fieldErrorContext(method, body string, user string, params map[string]string) echo.Context {
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx := testAuthContext(echo.New(), req, httptest.NewRecorder(), user)
	names := make([]string, 0, len(params))
	values := make([]string, 0, len(params))
	for k, v := range params {
		names = append(names, k)
		values = append(values, v)
	}
	ctx.SetParamNames(names...)
	ctx.SetParamValues(values...)
	return ctx
}

func TestRequiredPathParamsAnswerFieldRequired(t *testing.T) {
	t.Parallel()
	const user = "11111111-1111-1111-1111-111111111111"

	t.Run("session invocation", func(t *testing.T) {
		t.Parallel()
		ctx := fieldErrorContext(http.MethodGet, "", user, map[string]string{"bot_id": "b", "session_id": "s", "invocation_id": " "})
		requireFieldError(t, (&SessionHandler{}).GetSessionInvocation(ctx), apperror.CodeRequestFieldRequired, "invocation_id")
	})
	t.Run("session info", func(t *testing.T) {
		t.Parallel()
		ctx := fieldErrorContext(http.MethodGet, "", user, map[string]string{"bot_id": "b"})
		requireFieldError(t, (&SessionInfoHandler{}).GetSessionInfo(ctx), apperror.CodeRequestFieldRequired, "session_id")
	})
	t.Run("token usage", func(t *testing.T) {
		t.Parallel()
		ctx := fieldErrorContext(http.MethodGet, "", user, nil)
		requireFieldError(t, (&TokenUsageHandler{}).GetTokenUsage(ctx), apperror.CodeRequestFieldRequired, "bot_id")
	})
	t.Run("mcp", func(t *testing.T) {
		t.Parallel()
		ctx := fieldErrorContext(http.MethodGet, "", user, nil)
		requireFieldError(t, (&MCPHandler{}).List(ctx), apperror.CodeRequestFieldRequired, "bot_id")
	})
}

func TestMCPOAuthExchangeNamesMissingField(t *testing.T) {
	t.Parallel()

	ctx := fieldErrorContext(http.MethodPost, `{"code":"c"}`, "u", nil)
	requireFieldError(t, (&MCPOAuthHandler{}).Exchange(ctx), apperror.CodeRequestFieldRequired, "state")
}
