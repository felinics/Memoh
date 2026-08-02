package tools

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/email"
	"github.com/memohai/memoh/internal/searchproviders"
	"github.com/memohai/memoh/internal/settings"
)

const configuredAvailabilityBotID = "00000000-0000-0000-0000-000000000001"

var configuredAvailabilityProviderID = uuid.MustParse("00000000-0000-0000-0000-000000000002")

type configuredAvailabilityQueries struct {
	dbstore.Queries
	settingsRow  sqlc.GetSettingsByBotIDRow
	searchRow    sqlc.SearchProvider
	emailBinding []sqlc.BotEmailBinding
}

func (q *configuredAvailabilityQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return q.settingsRow, nil
}

func (q *configuredAvailabilityQueries) GetSearchProviderByID(context.Context, pgtype.UUID) (sqlc.SearchProvider, error) {
	return q.searchRow, nil
}

func (q *configuredAvailabilityQueries) ListBotEmailBindings(context.Context, pgtype.UUID) ([]sqlc.BotEmailBinding, error) {
	return append([]sqlc.BotEmailBinding(nil), q.emailBinding...), nil
}

type configuredAvailabilityEmailRuntime struct{}

func (configuredAvailabilityEmailRuntime) RefreshProvider(context.Context, string) error {
	return nil
}

func (configuredAvailabilityEmailRuntime) SendEmail(context.Context, string, string, email.OutboundEmail) (string, error) {
	return "", nil
}

func TestWebToolsRequireEnabledConfiguredProvider(t *testing.T) {
	t.Parallel()

	queries := &configuredAvailabilityQueries{}
	settingsService := settings.NewService(slog.Default(), queries, nil, nil)
	searchService := searchproviders.NewService(slog.Default(), queries)
	provider := NewWebProvider(slog.Default(), settingsService, searchService)
	session := SessionContext{BotID: configuredAvailabilityBotID}

	got, err := provider.Tools(t.Context(), session)
	if err != nil || len(got) != 0 {
		t.Fatalf("unconfigured search tools = %#v, err = %v", toolNames(got), err)
	}

	queries.settingsRow.SearchProviderID = pgtype.UUID{Bytes: configuredAvailabilityProviderID, Valid: true}
	queries.searchRow.Enable = false
	got, err = provider.Tools(t.Context(), session)
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled search tools = %#v, err = %v", toolNames(got), err)
	}

	queries.searchRow.Enable = true
	got, err = provider.Tools(t.Context(), session)
	if err != nil {
		t.Fatalf("configured search tools: %v", err)
	}
	if names := toolNames(got); !slices.Equal(names, []string{ToolWebSearch().String()}) {
		t.Fatalf("configured search tools = %#v", names)
	}
}

func TestEmailToolsFollowConfiguredBindingPermissions(t *testing.T) {
	t.Parallel()

	queries := &configuredAvailabilityQueries{}
	service := email.NewService(slog.Default(), queries, nil)
	provider := NewEmailProvider(slog.Default(), service, configuredAvailabilityEmailRuntime{})
	session := SessionContext{BotID: configuredAvailabilityBotID}

	tests := []struct {
		name     string
		bindings []sqlc.BotEmailBinding
		want     []string
	}{
		{name: "unconfigured"},
		{
			name:     "read only",
			bindings: []sqlc.BotEmailBinding{{CanRead: true}},
			want:     []string{ToolListEmailAccounts().String(), ToolListEmail().String(), ToolReadEmail().String()},
		},
		{
			name:     "write only",
			bindings: []sqlc.BotEmailBinding{{CanWrite: true}},
			want:     []string{ToolListEmailAccounts().String(), ToolSendEmail().String()},
		},
		{
			name:     "read and write",
			bindings: []sqlc.BotEmailBinding{{CanRead: true, CanWrite: true}},
			want:     []string{ToolListEmailAccounts().String(), ToolSendEmail().String(), ToolListEmail().String(), ToolReadEmail().String()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries.emailBinding = test.bindings
			got, err := provider.Tools(t.Context(), session)
			if err != nil {
				t.Fatalf("list email tools: %v", err)
			}
			if names := toolNames(got); !slices.Equal(names, test.want) {
				t.Fatalf("email tools = %#v, want %#v", names, test.want)
			}
		})
	}
}
