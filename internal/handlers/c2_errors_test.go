package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botbackup"
	"github.com/felinics/memoh/internal/botbackup/secure"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/settings"
)

const c2BotID = "00000000-0000-0000-0000-0000000000b1"

type emptySettingsQueries struct{ dbstore.Queries }

func (emptySettingsQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{}, nil
}

func serveJSONProblem(t *testing.T, register func(*echo.Echo), target, body string) (int, server.Problem) {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))
	register(e)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var problem server.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem %q: %v", rec.Body.String(), err)
	}
	return rec.Code, problem
}

func TestBotSynthesizeAnswersTextLimitAndMissingModel(t *testing.T) {
	t.Parallel()
	h := NewBotAudioHandler(slog.New(slog.DiscardHandler), nil,
		settings.NewService(slog.New(slog.DiscardHandler), emptySettingsQueries{}, nil, nil), nil)

	status, p := serveJSONProblem(t, h.Register, "/bots/"+c2BotID+"/tts/synthesize", `{"text":"`+strings.Repeat("a", 501)+`"}`)
	if status != http.StatusBadRequest || p.Code != string(apperror.CodeTTSTextTooLong) || p.Args["max"] != "500" || p.Fault != apperror.FaultClient {
		t.Fatalf("too long: status=%d problem=%+v", status, p)
	}

	status, p = serveJSONProblem(t, h.Register, "/bots/"+c2BotID+"/tts/synthesize", `{"text":"hello"}`)
	if status != http.StatusConflict || p.Code != string(apperror.CodeTTSModelNotConfigured) || p.Fault != apperror.FaultClient {
		t.Fatalf("no model: status=%d problem=%+v", status, p)
	}
}

func TestSpeechModelTestAnswersTextLimit(t *testing.T) {
	t.Parallel()
	h := &AudioHandler{}
	ctx := fieldErrorContext(http.MethodPost, `{"text":"`+strings.Repeat("a", 501)+`"}`, "u", map[string]string{"id": "m"})
	err := h.TestModel(ctx)
	if apperror.CodeOf(err) != apperror.CodeTTSTextTooLong || apperror.ArgsOf(err)["max"] != "500" {
		t.Fatalf("TestModel() = %v, want %s with max 500", err, apperror.CodeTTSTextTooLong)
	}
}

func TestBotBackupErrorSplitsUserAndInternalFailures(t *testing.T) {
	t.Parallel()
	bundle := botbackup.ErrInvalidBundle
	cases := []struct {
		name  string
		err   error
		code  apperror.Code
		field string
	}{
		{"passphrase required", secure.ErrPassphraseRequired, apperror.CodeRequestFieldRequired, "passphrase"},
		{"wrong passphrase", secure.ErrAuth, apperror.CodeRequestFieldInvalid, "passphrase"},
		{"target required", botbackup.ErrTargetBotRequired, apperror.CodeRequestFieldRequired, "target_bot_id"},
		{"bad bundle", errors.Join(bundle, errors.New("manifest.json not found")), apperror.CodeBotBackupBundleInvalid, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := botBackupError(tc.err, "import bot backup")
			if got := apperror.CodeOf(err); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
			if got := apperror.ArgsOf(err)["field"]; got != tc.field {
				t.Fatalf("field = %q, want %q", got, tc.field)
			}
		})
	}

	cause := errors.New("insert bot failed")
	err := botBackupError(cause, "import bot backup")
	if apperror.CodeOf(err) != "" || !errors.Is(err, cause) {
		t.Fatalf("internal failure = %v, want the cause wrapped without a public code", err)
	}
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("internal failure carries HTTP status %d", httpErr.Code)
	}
	if errs.FaultOf(err) != apperror.FaultServer {
		t.Fatalf("fault = %s, want server", errs.FaultOf(err))
	}
}

func TestImportModelsRejectsInvalidDefaultCompatibilities(t *testing.T) {
	t.Parallel()
	h := NewProvidersHandler(slog.New(slog.DiscardHandler), providers.NewService(nil, testHandlerQueries{}, ""), nil)
	ctx := fieldErrorContext(http.MethodPost, `{"default_compatibilities":["bogus"]}`, "u", map[string]string{"id": "00000000-0000-0000-0000-000000000001"})
	requireFieldError(t, h.ImportModels(ctx), apperror.CodeRequestFieldInvalid, "default_compatibilities")
}

func TestFSArchiveAndExtractFieldAndArchiveErrors(t *testing.T) {
	env := newSkillsTestEnv(t)

	_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/archive", FSArchiveRequest{Paths: []string{" "}}, env.handler.FSArchive)
	requireFieldError(t, err, apperror.CodeRequestFieldRequired, "paths")

	env.writeBinaryFile(t, "/data/broken.zip", []byte("not a zip"))
	env.writeBinaryFile(t, "/data/notes.txt", []byte("hello"))
	if err := os.MkdirAll(env.localPath("/data/folder.zip"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, p := range []string{"/data/broken.zip", "/data/notes.txt", "/data/folder.zip"} {
		_, err := env.callFileManager(t, http.MethodPost, "/bots/:bot_id/container/fs/extract", FSExtractRequest{Path: p}, env.handler.FSExtract)
		if got := apperror.CodeOf(err); got != apperror.CodeWorkspaceArchiveInvalid {
			t.Fatalf("extract %s: error = %v, want code %s", p, err, apperror.CodeWorkspaceArchiveInvalid)
		}
	}
}

func TestUpsertSkillsRejectsSourcePathWithSeveralSkills(t *testing.T) {
	env := newSkillsTestEnv(t)
	_, err := env.callJSON(t, http.MethodPost, "/bots/:bot_id/container/skills", SkillsUpsertRequest{
		Skills:     []string{managedSkillRaw("a", "A"), managedSkillRaw("b", "B")},
		SourcePath: "/data/skills/user/personal/a/SKILL.md",
	}, env.handler.UpsertSkills)
	requireFieldError(t, err, apperror.CodeRequestFieldInvalid, "source_path")
}

func TestModelWriteErrorNamesFieldAndKeepsInternalFailuresInternal(t *testing.T) {
	t.Parallel()
	cause := errors.New("probe failed")
	requireFieldError(t, modelWriteError(fmt.Errorf("wrapped: %w", &models.FieldError{Field: "config.dimensions", Required: true, Err: cause}), "create model"),
		apperror.CodeRequestFieldRequired, "config.dimensions")
	requireFieldError(t, modelWriteError(&models.FieldError{Field: "type", Err: models.ErrInvalidModelType}, "create model"),
		apperror.CodeRequestFieldInvalid, "type")

	internal := errors.New("insert model failed")
	err := modelWriteError(internal, "create model")
	if apperror.CodeOf(err) != "" || !errors.Is(err, internal) {
		t.Fatalf("internal failure = %v, want the cause wrapped without a public code", err)
	}
}
