package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/providertemplates"
	"github.com/felinics/memoh/internal/server"
)

// serveProblem sends one request to the routes register adds, through the
// server's error handler, and returns the status and the Problem it answered
// with.
func serveProblem(t *testing.T, register func(*echo.Echo), method, target, body string) (int, server.Problem) {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))
	register(e)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var problem server.Problem
	if rec.Code >= http.StatusBadRequest {
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatalf("decode problem %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, problem
}

type duplicateProviderQueries struct {
	dbstore.Queries
	existing sqlc.Provider
}

func (duplicateProviderQueries) CreateProvider(context.Context, sqlc.CreateProviderParams) (sqlc.Provider, error) {
	return sqlc.Provider{}, &pgconn.PgError{Code: "23505", ConstraintName: "providers_name_unique"}
}

func (q duplicateProviderQueries) GetProviderByName(context.Context, string) (sqlc.Provider, error) {
	return q.existing, nil
}

func TestCreateProviderAnswersNameTakenForADuplicateName(t *testing.T) {
	queries := duplicateProviderQueries{existing: sqlc.Provider{
		ID:       pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Name:     "openai",
		Enable:   true,
		Config:   []byte(`{"api_key":"sk-configured"}`),
		Metadata: []byte(`{}`),
	}}
	h := NewProvidersHandler(slog.New(slog.DiscardHandler), providers.NewService(nil, queries, ""), nil)
	status, problem := serveProblem(t, h.Register, http.MethodPost, "/providers", `{"name":"openai","client_type":"openai-completions"}`)
	if status != http.StatusConflict || problem.Code != string(apperror.CodeProviderNameTaken) || problem.Fault != "client" {
		t.Fatalf("answer = %d %+v, want 409 %s client", status, problem, apperror.CodeProviderNameTaken)
	}
}

type duplicateNameUpdateQueries struct {
	dbstore.Queries
	existing sqlc.Provider
}

func (q duplicateNameUpdateQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.existing, nil
}

func (duplicateNameUpdateQueries) UpdateProvider(context.Context, sqlc.UpdateProviderParams) (sqlc.Provider, error) {
	return sqlc.Provider{}, &pgconn.PgError{Code: "23505", ConstraintName: "providers_name_unique"}
}

func TestUpdateProviderAnswersNameTakenForADuplicateName(t *testing.T) {
	queries := duplicateNameUpdateQueries{existing: sqlc.Provider{
		ID:         pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Name:       "deepseek",
		ClientType: "openai-completions",
		Enable:     true,
		Config:     []byte(`{"api_key":"sk-configured"}`),
		Metadata:   []byte(`{}`),
	}}
	h := NewProvidersHandler(slog.New(slog.DiscardHandler), providers.NewService(nil, queries, ""), nil)
	status, problem := serveProblem(t, h.Register, http.MethodPut, "/providers/00000000-0000-0000-0000-000000000001", `{"name":"openai"}`)
	if status != http.StatusConflict || problem.Code != string(apperror.CodeProviderNameTaken) || problem.Fault != "client" {
		t.Fatalf("answer = %d %+v, want 409 %s client", status, problem, apperror.CodeProviderNameTaken)
	}
}

type providerTemplateQueries struct {
	dbstore.Queries
	template    sqlc.TemplateProviderTemplate
	templateErr error
	listErr     error
	modelsErr   error
	createErr   error
}

func (q providerTemplateQueries) GetProviderTemplateByID(context.Context, pgtype.UUID) (sqlc.TemplateProviderTemplate, error) {
	return q.template, q.templateErr
}

func (q providerTemplateQueries) ListProviderTemplates(context.Context, string) ([]sqlc.ListProviderTemplatesRow, error) {
	return nil, q.listErr
}

func (q providerTemplateQueries) ListProviderTemplateModels(context.Context, pgtype.UUID) ([]sqlc.TemplateProviderTemplateModel, error) {
	return nil, q.modelsErr
}

func (q providerTemplateQueries) CreateProviderFromTemplate(context.Context, sqlc.CreateProviderFromTemplateParams) (sqlc.Provider, error) {
	return sqlc.Provider{}, q.createErr
}

const testTemplateID = "00000000-0000-0000-0000-000000000001"

func TestProviderTemplateFailuresAnswerTheirCodes(t *testing.T) {
	llm := sqlc.TemplateProviderTemplate{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Domain: "llm", Name: "OpenAI"}
	search := llm
	search.Domain = "search"
	dbDown := errors.New("connection reset by peer")
	cases := []struct {
		name    string
		queries providerTemplateQueries
		method  string
		target  string
		body    string
		status  int
		code    apperror.Code
		fault   apperror.Fault
	}{
		{"list with an unknown domain", providerTemplateQueries{}, http.MethodGet, "/provider-templates?domain=bogus", "", http.StatusBadRequest, apperror.CodeProviderTemplateDomainInvalid, apperror.FaultClient},
		{"list fails in the database", providerTemplateQueries{listErr: dbDown}, http.MethodGet, "/provider-templates", "", http.StatusInternalServerError, apperror.CodeProviderTemplateOperationFailed, apperror.FaultServer},
		{"get with an unparsable id", providerTemplateQueries{}, http.MethodGet, "/provider-templates/not-a-uuid", "", http.StatusNotFound, apperror.CodeProviderTemplateNotFound, apperror.FaultClient},
		{"get an unknown template", providerTemplateQueries{templateErr: pgx.ErrNoRows}, http.MethodGet, "/provider-templates/" + testTemplateID, "", http.StatusNotFound, apperror.CodeProviderTemplateNotFound, apperror.FaultClient},
		{"get fails in the database", providerTemplateQueries{templateErr: dbDown}, http.MethodGet, "/provider-templates/" + testTemplateID, "", http.StatusInternalServerError, apperror.CodeProviderTemplateOperationFailed, apperror.FaultServer},
		{"get fails reading models", providerTemplateQueries{template: llm, modelsErr: dbDown}, http.MethodGet, "/provider-templates/" + testTemplateID, "", http.StatusInternalServerError, apperror.CodeProviderTemplateOperationFailed, apperror.FaultServer},
		{"create with an unknown domain", providerTemplateQueries{}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `","domain":"bogus"}`, http.StatusBadRequest, apperror.CodeProviderTemplateDomainInvalid, apperror.FaultClient},
		{"create with an unparsable id", providerTemplateQueries{}, http.MethodPost, "/providers/from-template", `{"template_id":"not-a-uuid"}`, http.StatusNotFound, apperror.CodeProviderTemplateNotFound, apperror.FaultClient},
		{"create from an unknown template", providerTemplateQueries{templateErr: pgx.ErrNoRows}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `"}`, http.StatusNotFound, apperror.CodeProviderTemplateNotFound, apperror.FaultClient},
		{"create reading the template fails", providerTemplateQueries{templateErr: dbDown}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `"}`, http.StatusInternalServerError, apperror.CodeProviderTemplateOperationFailed, apperror.FaultServer},
		{"create from a template of another domain", providerTemplateQueries{template: llm}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `","domain":"speech"}`, http.StatusBadRequest, apperror.CodeProviderTemplateDomainMismatch, apperror.FaultClient},
		{"create from a template no provider can use", providerTemplateQueries{template: search}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `"}`, http.StatusBadRequest, apperror.CodeProviderTemplateDomainMismatch, apperror.FaultClient},
		{"create with a taken name", providerTemplateQueries{template: llm, createErr: &pgconn.PgError{Code: "23505", ConstraintName: "providers_name_unique"}}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `"}`, http.StatusConflict, apperror.CodeProviderNameTaken, apperror.FaultClient},
		{"create fails in the database", providerTemplateQueries{template: llm, createErr: dbDown}, http.MethodPost, "/providers/from-template", `{"template_id":"` + testTemplateID + `"}`, http.StatusInternalServerError, apperror.CodeProviderTemplateOperationFailed, apperror.FaultServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			register := func(e *echo.Echo) {
				NewProviderTemplatesHandler(providertemplates.NewService(log, tc.queries)).Register(e)
				NewProvidersHandler(log, providers.NewService(log, tc.queries, ""), nil).Register(e)
			}
			status, problem := serveProblem(t, register, tc.method, tc.target, tc.body)
			if status != tc.status || problem.Code != string(tc.code) || problem.Fault != tc.fault {
				t.Fatalf("answer = %d %s %s, want %d %s %s", status, problem.Code, problem.Fault, tc.status, tc.code, tc.fault)
			}
		})
	}
}

func TestProviderTemplateErrorTranslatesEachSentinel(t *testing.T) {
	coded := apperror.New(apperror.CodeProviderTemplateRequestInvalid, nil)
	cases := []struct {
		err  error
		want apperror.Code
	}{
		{fmt.Errorf("resolve: %w", providertemplates.ErrNotFound), apperror.CodeProviderTemplateNotFound},
		{fmt.Errorf("%w: bogus", providertemplates.ErrDomainInvalid), apperror.CodeProviderTemplateDomainInvalid},
		{fmt.Errorf("%w: search", providertemplates.ErrDomainMismatch), apperror.CodeProviderTemplateDomainMismatch},
		{fmt.Errorf("create provider from template: %w", providers.ErrNameTaken), apperror.CodeProviderNameTaken},
		{errors.New("marshal provider template value"), apperror.CodeProviderTemplateOperationFailed},
		{coded, apperror.CodeProviderTemplateRequestInvalid},
	}
	for _, tc := range cases {
		got := providerTemplateError(tc.err)
		if apperror.CodeOf(got) != tc.want {
			t.Fatalf("providerTemplateError(%v) = %q, want %q", tc.err, apperror.CodeOf(got), tc.want)
		}
		if !errors.Is(apperror.CauseOf(got), tc.err) && got != coded { //nolint:errorlint // Identity: an already coded error is returned as it is.
			t.Fatalf("providerTemplateError(%v) dropped its cause", tc.err)
		}
	}
	if providerTemplateError(nil) != nil {
		t.Fatal("providerTemplateError(nil) is not nil")
	}
}

func TestProviderErrorTranslatesOnlyTheTakenName(t *testing.T) {
	taken := fmt.Errorf("create provider: %w: %w", providers.ErrNameTaken, &pgconn.PgError{Code: "23505"})
	if got := apperror.CodeOf(providerError(taken)); got != apperror.CodeProviderNameTaken {
		t.Fatalf("code = %q, want %q", got, apperror.CodeProviderNameTaken)
	}
	other := errors.New("create provider: connection reset")
	if got := providerError(other); got != other { //nolint:errorlint // Identity: an untranslated error is returned as it is.
		t.Fatalf("providerError(%v) = %v, want it unchanged", other, got)
	}
}
