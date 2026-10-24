package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/settings"
)

type fishAudioQueries struct {
	dbstore.Queries
	provider sqlc.Provider
}

func (q fishAudioQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func (q fishAudioQueries) GetSpeechModelWithProvider(context.Context, pgtype.UUID) (sqlc.GetSpeechModelWithProviderRow, error) {
	return sqlc.GetSpeechModelWithProviderRow{ModelID: "s2.1-pro", Enable: true, ProviderID: q.provider.ID}, nil
}

func (q fishAudioQueries) GetTranscriptionModelWithProvider(context.Context, pgtype.UUID) (sqlc.GetTranscriptionModelWithProviderRow, error) {
	return sqlc.GetTranscriptionModelWithProviderRow{ModelID: "transcribe-1-pro", Enable: true, ProviderID: q.provider.ID}, nil
}

func (fishAudioQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{TtsModelID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}}, nil
}

func TestFishAudioQuotaAnswersDependencyWithoutSecrets(t *testing.T) {
	for _, mode := range []string{"speech", "transcription", "bot speech"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			testFishAudioQuotaAnswer(t, mode)
		})
	}
}

func testFishAudioQuotaAnswer(t *testing.T, mode string) {
	t.Helper()
	const privateContent = "sk-FAKE-key-and-submitted-text"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/tts"
		if mode == "transcription" {
			wantPath = "/asr"
		}
		if r.URL.Path != wantPath {
			t.Errorf("upstream path = %q, want %s", r.URL.Path, wantPath)
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"message":"Insufficient API credit ` + privateContent + `"}`))
	}))
	t.Cleanup(upstream.Close)
	config, err := json.Marshal(map[string]string{"api_key": privateContent, "base_url": upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	clientType := "fishaudio-speech"
	if mode == "transcription" {
		clientType = "fishaudio-transcription"
	}
	queries := fishAudioQueries{provider: sqlc.Provider{ClientType: clientType, Config: config}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	service := audio.NewService(logger, queries, audio.NewRegistry())
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(logger)
	e.Use(server.AccessLog(logger))
	NewAudioHandler(logger, service, nil).Register(e)
	req := httptest.NewRequest(http.MethodPost, "/speech-models/00000000-0000-0000-0000-000000000001/test", strings.NewReader(`{"text":"test"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	switch mode {
	case "transcription":
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "test.wav")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("RIFF-test-audio")); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(http.MethodPost, "/transcription-models/00000000-0000-0000-0000-000000000001/test", &body)
		req.Header.Set(echo.HeaderContentType, form.FormDataContentType())
	case "bot speech":
		store, err := audio.NewTempStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		NewBotAudioHandler(logger, service, settings.NewService(logger, queries, nil, nil), store).Register(e)
		req.URL.Path = "/bots/00000000-0000-0000-0000-000000000001/tts/synthesize"
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var problem server.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadGateway || problem.Code != string(apperror.CodeAgentProviderQuotaExhausted) || problem.Fault != apperror.FaultDependency {
		t.Fatalf("answer = %d %s, want 502 agent.provider_quota_exhausted dependency", rec.Code, rec.Body.String())
	}
	if problem.Detail != "The model provider account has no remaining balance or quota." {
		t.Fatalf("detail = %q", problem.Detail)
	}
	if rec.Header().Get(echo.HeaderContentType) != "application/problem+json" {
		t.Fatalf("content type = %q", rec.Header().Get(echo.HeaderContentType))
	}
	if strings.Contains(rec.Body.String()+logs.String(), privateContent) {
		t.Fatal("upstream body or API key leaked to response or log")
	}
	if !strings.Contains(logs.String(), `"reason":"agent.provider_quota_exhausted"`) || !strings.Contains(logs.String(), `"fault":"dependency"`) {
		t.Fatalf("log does not attribute quota failure to dependency: %s", logs.String())
	}
}
