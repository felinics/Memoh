package httpx_test

import (
	"net/http"
	"net/http/httptest"
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
			c := e.NewContext(httptest.NewRequest(http.MethodGet, tc.target, nil), httptest.NewRecorder())
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
