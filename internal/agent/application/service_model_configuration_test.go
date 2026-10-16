package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/settings"
)

type missingModelQueries struct{ modelSelectionFakeQueries }

func (*missingModelQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{}, nil
}

func (*missingModelQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{}, nil
}

func TestBuildBaseRunConfigMissingModelPreservesPublicCode(t *testing.T) {
	queries := &missingModelQueries{}
	logger := slog.New(slog.DiscardHandler)
	svc := &Service{logger: logger, queries: queries, settingsService: settings.NewService(logger, queries, nil, nil), modelsService: models.NewService(logger, queries)}
	_, _, _, err := svc.buildBaseRunConfig(t.Context(), baseRunConfigParams{BotID: "00000000-0000-0000-0000-000000000001"})
	const want apperror.Code = "agent.chat_model_not_configured"
	if got := publicFailureCode(err); got != want {
		t.Fatalf("startup code = %q, want %q (error: %v)", got, want, err)
	}
	wire := rpc.AnswerStatus(t.Context(), fmt.Errorf("SECRET startup context: %w", err))
	if got := apperror.CodeOf(rpc.DecodeAppError(wire)); got != want {
		t.Fatalf("RPC code = %q, want %q", got, want)
	}
	if strings.Contains(wire.Error(), "SECRET") {
		t.Fatalf("private diagnostic crossed the RPC boundary: %v", wire)
	}
}
