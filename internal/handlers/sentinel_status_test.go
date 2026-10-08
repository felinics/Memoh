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
			var httpErr *echo.HTTPError
			if err := h.GetChannelIdentityConfig(c); !errors.As(err, &httpErr) || httpErr.Code != tc.want {
				t.Fatalf("status = %v, want %d", err, tc.want)
			}
		})
	}
}

func TestFetchProviderHTTPErrorStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: bogus", fetchproviders.ErrInvalidProvider), http.StatusBadRequest},
		{fetchproviders.ErrManagedNativeProvider, http.StatusBadRequest},
		{errors.New("invalid provider row"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		var httpErr *echo.HTTPError
		if err := fetchProviderHTTPError(tc.err); !errors.As(err, &httpErr) || httpErr.Code != tc.want {
			t.Errorf("fetchProviderHTTPError(%v) = %v, want %d", tc.err, err, tc.want)
		}
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
		{"provider models id", "not-a-uuid", "/", providersHandler.ListModelsByProvider, ""},
		{"provider models type", uuid.NewString(), "/?type=bogus", providersHandler.ListModelsByProvider, ""},
		{"model test id", "not-a-uuid", "/", modelsHandler.Test, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, tc.target, nil), httptest.NewRecorder())
			c.SetParamNames("id")
			c.SetParamValues(tc.id)
			err := tc.call(c)
			if tc.field != "" {
				if apperror.CodeOf(err) != apperror.CodeRequestFieldInvalid || apperror.ArgsOf(err)["field"] != tc.field {
					t.Fatalf("error = %v, want %s for field %q", err, apperror.CodeRequestFieldInvalid, tc.field)
				}
				return
			}
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
				t.Fatalf("status = %v, want 400", err)
			}
		})
	}
}
