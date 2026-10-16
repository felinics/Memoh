package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

func TestRequestFieldErrorsAreAnsweredWithTheFieldName(t *testing.T) {
	providersHandler := &ProvidersHandler{service: &providers.Service{}, modelsService: &models.Service{}}
	modelsHandler := &ModelsHandler{service: &models.Service{}}
	ttsHandler := &AudioHandler{}
	cases := []struct {
		name   string
		code   apperror.Code
		field  string
		params map[string]string
		target string
		body   string
		call   func(echo.Context) error
	}{
		{"provider id", apperror.CodeRequestFieldRequired, "id", map[string]string{"id": " "}, "/", "", providersHandler.Get},
		{"provider name body", apperror.CodeRequestFieldRequired, "name", nil, "/", `{}`, providersHandler.Create},
		{"model modelId", apperror.CodeRequestFieldRequired, "modelId", map[string]string{"modelId": ""}, "/", "", modelsHandler.GetByModelID},
		{"models client_type", apperror.CodeRequestFieldInvalid, "client_type", nil, "/?client_type=bogus", "", modelsHandler.List},
		{"speech provider id", apperror.CodeRequestFieldRequired, "id", map[string]string{"id": ""}, "/", "", ttsHandler.GetProvider},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			c := e.NewContext(req, httptest.NewRecorder())
			for k, v := range tc.params {
				c.SetParamNames(k)
				c.SetParamValues(v)
			}
			err := tc.call(c)
			if apperror.CodeOf(err) != tc.code || apperror.ArgsOf(err)["field"] != tc.field {
				t.Fatalf("error = %v, want %s for field %q", err, tc.code, tc.field)
			}
		})
	}
}
