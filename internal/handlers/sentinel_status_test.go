package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

const sentinelTestChannelType = channel.ChannelType("sentinel-test")

type sentinelTestAdapter struct{}

func (sentinelTestAdapter) Type() channel.ChannelType { return sentinelTestChannelType }
func (sentinelTestAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{Type: sentinelTestChannelType, DisplayName: "Sentinel"}
}

type bindingQueries struct {
	dbstore.Queries
	err error
}

func (q bindingQueries) GetUserChannelBinding(context.Context, sqlc.GetUserChannelBindingParams) (sqlc.UserChannelBinding, error) {
	return sqlc.UserChannelBinding{}, q.err
}

func TestGetChannelIdentityConfigStatusFollowsSentinel(t *testing.T) {
	registry := channel.NewRegistry()
	registry.MustRegister(sentinelTestAdapter{})
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"missing binding", pgx.ErrNoRows, http.StatusNotFound},
		// Text that merely mentions "not found" is an internal failure.
		{"other failure", errors.New("relation not found"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewChannelHandler(channel.NewStore(bindingQueries{err: tc.err}, registry), registry)
			e := echo.New()
			rec := httptest.NewRecorder()
			c := testAuthContext(e, httptest.NewRequest(http.MethodGet, "/", nil), rec, uuid.NewString())
			c.SetParamNames("platform")
			c.SetParamValues(string(sentinelTestChannelType))
			assertSentinelStatus(t, h.GetChannelIdentityConfig(c), tc.err, tc.want)
		})
	}
}

func TestFetchProviderHTTPErrorStatus(t *testing.T) {
	invalid := fetchProviderHTTPError(fmt.Errorf("%w: bogus", fetchproviders.ErrInvalidProvider))
	requireFieldError(t, invalid, apperror.CodeRequestFieldInvalid, "provider")
	if got := apperror.CodeOf(fetchProviderHTTPError(fetchproviders.ErrManagedNativeProvider)); got != apperror.CodeFetchProviderNativeManaged {
		t.Fatalf("native provider code = %s, want %s", got, apperror.CodeFetchProviderNativeManaged)
	}
	cause := errors.New("invalid provider row")
	assertSentinelStatus(t, fetchProviderHTTPError(cause), cause, http.StatusInternalServerError)
}

// assertSentinelStatus checks a 4xx answer as an echo.HTTPError. A 500 is the
// cause wrapped without a status of its own, which the HTTP boundary answers
// as an internal error.
func assertSentinelStatus(t *testing.T, got, cause error, want int) {
	t.Helper()
	var httpErr *echo.HTTPError
	isHTTP := errors.As(got, &httpErr)
	if want == http.StatusNotFound && apperror.CodeOf(got) != "" {
		if def, _ := apperror.Lookup(apperror.CodeOf(got)); def.HTTPStatus != want || apperror.CauseOf(got) == nil {
			t.Fatalf("answer = %v, want a 404 public error with a cause", got)
		}
		return
	}
	if want == http.StatusInternalServerError {
		if isHTTP || !errors.Is(got, cause) {
			t.Fatalf("error = %v, want %v wrapped without a status", got, cause)
		}
		return
	}
	if !isHTTP || httpErr.Code != want {
		t.Fatalf("status = %v, want %d", got, want)
	}
}

func TestProviderAndModelHandlersRejectInvalidInputWith400(t *testing.T) {
	providersHandler := &ProvidersHandler{service: &providers.Service{}, modelsService: &models.Service{}}
	modelsHandler := &ModelsHandler{service: &models.Service{}}
	cases := []struct {
		name   string
		id     string
		target string
		call   func(echo.Context) error
		field  string
	}{
		{"provider test id", "not-a-uuid", "/", providersHandler.Test, "id"},
		{"provider models id", "not-a-uuid", "/", providersHandler.ListModelsByProvider, "id"},
		{"provider models type", uuid.NewString(), "/?type=bogus", providersHandler.ListModelsByProvider, "type"},
		{"model test id", "not-a-uuid", "/", modelsHandler.Test, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, tc.target, nil), httptest.NewRecorder())
			c.SetParamNames("id")
			c.SetParamValues(tc.id)
			err := tc.call(c)
			if apperror.CodeOf(err) != apperror.CodeRequestFieldInvalid || apperror.ArgsOf(err)["field"] != tc.field {
				t.Fatalf("error = %v, want %s for field %q", err, apperror.CodeRequestFieldInvalid, tc.field)
			}
		})
	}
}
