package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/httpx"
)

func TestRequiredParamsNameTheMissingField(t *testing.T) {
	e := echo.New()
	for name, tc := range map[string]struct {
		target string
		param  string
		read   func(echo.Context, string) (string, error)
		want   string
	}{
		"path present":  {"/", " b_1 ", httpx.RequiredParam, "b_1"},
		"path blank":    {"/", "  ", httpx.RequiredParam, ""},
		"query present": {"/?bot_id=b_2", "", httpx.RequiredQuery, "b_2"},
		"query absent":  {"/", "", httpx.RequiredQuery, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := e.NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.target, nil), httptest.NewRecorder())
			c.SetParamNames("bot_id")
			c.SetParamValues(tc.param)
			got, err := tc.read(c, "bot_id")
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if apperror.CodeOf(err) != apperror.CodeRequestFieldRequired || apperror.ArgsOf(err)["field"] != "bot_id" {
				t.Fatalf("error = %v (args %v), want request.field_required for bot_id", err, apperror.ArgsOf(err))
			}
		})
	}
}

func TestBinderNamesTheFieldOfAWrongTypedValue(t *testing.T) {
	type item struct {
		Count int `json:"count"`
	}
	type body struct {
		Limit int `json:"limit"`
		Inner struct {
			Flag bool `json:"flag"`
		} `json:"inner"`
		Items []item          `json:"items"`
		Meta  map[string]item `json:"meta"`
	}
	e := echo.New()
	for name, tc := range map[string]struct {
		body  string
		field string
	}{
		"top level":     {`{"limit":"x"}`, "limit"},
		"nested object": {`{"inner":{"flag":"x"}}`, "inner.flag"},
		"whole array":   {`{"items":"x"}`, "items"},
		// An element is named with its position and a map value with its key:
		// "items.count" would not say which of the items is wrong.
		"array element": {`{"items":[{"count":1},{"count":"x"}]}`, "items.1.count"},
		"map value":     {`{"meta":{"k":{"count":"x"}}}`, "meta.k.count"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			var got body
			err := (&httpx.Binder{}).Bind(&got, e.NewContext(req, httptest.NewRecorder()))
			if apperror.CodeOf(err) != apperror.CodeRequestFieldInvalid || apperror.ArgsOf(err)["field"] != tc.field {
				t.Fatalf("error = %v (args %v), want request.field_invalid for %q", err, apperror.ArgsOf(err), tc.field)
			}
		})
	}

	// Malformed JSON names no field and stays echo's 400.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(`{"limit":`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	var got body
	err := (&httpx.Binder{}).Bind(&got, e.NewContext(req, httptest.NewRecorder()))
	if err == nil || apperror.CodeOf(err) == apperror.CodeRequestFieldInvalid {
		t.Fatalf("malformed JSON error = %v, want a binding error without a field", err)
	}
}
