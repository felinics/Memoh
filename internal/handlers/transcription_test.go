package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/memohai/memoh/internal/apperror"
	audiopkg "github.com/memohai/memoh/internal/audio"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/models"
)

type transcriptionTestStore struct {
	dbstore.Queries
	model    sqlc.GetTranscriptionModelWithProviderRow
	provider sqlc.Provider
}

func (s transcriptionTestStore) GetTranscriptionModelWithProvider(context.Context, pgtype.UUID) (sqlc.GetTranscriptionModelWithProviderRow, error) {
	return s.model, nil
}

func (s transcriptionTestStore) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return s.provider, nil
}

// Exercise the PC upload contract through the service and actual provider adapter.
func TestAlibabaTranscriptionUpload(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		upstreamStatus   int
		config           string
		disabledProvider bool
		speechOnly       bool
		wantStatus       int
		wantCode         apperror.Code
	}{
		{name: "success", upstreamStatus: 200, config: `{"language":"zh","enable_itn":false}`, wantStatus: 200},
		{name: "invalid key", upstreamStatus: 401, wantStatus: 502, wantCode: apperror.CodeTranscriptionRequestRejected},
		{name: "rate limit", upstreamStatus: 429, wantStatus: 429, wantCode: apperror.CodeTranscriptionRateLimited},
		{name: "unavailable", upstreamStatus: 503, wantStatus: 503, wantCode: apperror.CodeTranscriptionUnavailable},
		{name: "invalid option", config: `{"enable_itn":"true"}`, wantStatus: 400, wantCode: apperror.CodeTranscriptionRequestInvalid},
		{name: "invalid JSON", config: `{"context":`, wantStatus: 400, wantCode: apperror.CodeTranscriptionRequestInvalid},
		{name: "disabled provider", disabledProvider: true, wantStatus: 400, wantCode: apperror.CodeTranscriptionRequestInvalid},
		{name: "speech only provider", speechOnly: true, wantStatus: 400, wantCode: apperror.CodeTranscriptionRequestInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.upstreamStatus == 0 {
					t.Error("invalid requests must not send audio to the provider")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer private-key" {
					t.Error("incorrect upstream route or authentication")
				}
				w.WriteHeader(tc.upstreamStatus)
				if tc.upstreamStatus == http.StatusOK {
					var request struct {
						Options map[string]any `json:"asr_options"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Options["language"] != "zh" || request.Options["enable_itn"] != false {
						t.Error("upload overrides must take precedence over saved configuration")
					}
					_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"明天上午九点开会。","annotations":[{"type":"audio_info","language":"zh"}]}}],"usage":{"seconds":3}}`)
				} else {
					_, _ = fmt.Fprint(w, `{"error":"private-key private-audio"}`)
				}
			}))
			defer upstream.Close()
			id, err := db.ParseUUID("00000000-0000-0000-0000-000000000002")
			require.NoError(t, err)
			store := transcriptionTestStore{
				model: sqlc.GetTranscriptionModelWithProviderRow{
					ID: id, ProviderID: id, ModelID: "qwen3-asr-flash", Enable: true,
					Config: []byte(`{"language":"en","enable_itn":true}`),
				},
				provider: sqlc.Provider{
					ID: id, ClientType: string(models.ClientTypeAlibabaTranscription), Enable: !tc.disabledProvider,
					Config: []byte(fmt.Sprintf(`{"api_key":"private-key","base_url":%q}`, upstream.URL)),
				},
			}
			if tc.speechOnly {
				store.provider.ClientType = string(models.ClientTypeEdgeSpeech)
			}
			log := slog.New(slog.DiscardHandler)
			handler := NewAudioHandler(log, audiopkg.NewService(log, store, audiopkg.NewRegistry()), nil)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			file, err := writer.CreateFormFile("file", "dictation.m4a")
			require.NoError(t, err)
			_, err = file.Write([]byte("private-audio"))
			require.NoError(t, err)
			require.NoError(t, writer.WriteField("config", tc.config))
			require.NoError(t, writer.Close())
			request := httptest.NewRequest(http.MethodPost, "/transcription-models/"+id.String()+"/test", &body)
			request.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
			recorder := httptest.NewRecorder()
			c := echo.New().NewContext(request, recorder)
			c.SetParamNames("id")
			c.SetParamValues(id.String())
			err = handler.TestTranscriptionModel(c)
			if tc.wantCode == "" {
				require.NoError(t, err)
				require.Equal(t, tc.wantStatus, recorder.Code)
				var result audiopkg.TestTranscriptionResponse
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
				require.Equal(t, "明天上午九点开会。", result.Text)
				require.Equal(t, "zh", result.Language)
				require.InDelta(t, 3, result.DurationSeconds, 0.001)
				return
			}
			require.Error(t, err)
			problem, ok := apperror.ProblemFrom(err, "asr-test")
			require.True(t, ok)
			require.Equal(t, tc.wantStatus, problem.Status)
			require.Equal(t, string(tc.wantCode), problem.Code)
			encoded, err := json.Marshal(problem)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "private-key")
			require.NotContains(t, string(encoded), "private-audio")
		})
	}
}
