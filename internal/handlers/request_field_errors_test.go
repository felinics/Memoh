package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
)

func TestRequestFieldErrors(t *testing.T) {
	jsonBody := func(s string) *http.Request {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(s))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		return req
	}
	cases := []struct {
		name   string
		req    *http.Request
		params map[string]string
		call   func(echo.Context) error
		code   apperror.Code
		field  string
	}{
		{"video path id", httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), map[string]string{"id": " "}, (&VideoHandler{}).GetProvider, apperror.CodeRequestFieldRequired, "id"},
		{"search provider name", jsonBody(`{"provider":"brave"}`), nil, (&SearchProvidersHandler{}).Create, apperror.CodeRequestFieldRequired, "name"},
		{"fetch provider provider", jsonBody(`{"name":"a"}`), nil, (&FetchProvidersHandler{}).Create, apperror.CodeRequestFieldRequired, "provider"},
		{"tts bot id", jsonBody(`{}`), nil, (&BotAudioHandler{}).Synthesize, apperror.CodeRequestFieldRequired, "bot_id"},
		{"tts text", jsonBody(`{}`), map[string]string{"bot_id": "bot"}, (&BotAudioHandler{}).Synthesize, apperror.CodeRequestFieldRequired, "text"},
		{"oauth callback code", httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?state=s", nil), nil, (&ProviderOAuthHandler{}).Callback, apperror.CodeRequestFieldRequired, "code"},
		{"oauth callback state", httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?code=c", nil), nil, (&ProviderOAuthHandler{}).Callback, apperror.CodeRequestFieldRequired, "state"},
		{"supermarket icon digest", httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), map[string]string{"digest": "short"}, (&SupermarketHandler{}).GetRegistrySkillIcon, apperror.CodeRequestFieldInvalid, "digest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := echo.New().NewContext(tc.req, httptest.NewRecorder())
			var names, values []string
			for k, v := range tc.params {
				names = append(names, k)
				values = append(values, v)
			}
			c.SetParamNames(names...)
			c.SetParamValues(values...)
			err := tc.call(c)
			if got := apperror.CodeOf(err); got != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.code, err)
			}
			if got := apperror.ArgsOf(err)["field"]; got != tc.field {
				t.Fatalf("field = %q, want %q", got, tc.field)
			}
		})
	}
}
