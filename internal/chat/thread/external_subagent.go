package thread

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

var ErrInvalidExternalSubagent = errors.New("invalid external subagent binding")

var ErrSubagentBindingImmutable = errors.New("external subagent runtime binding is immutable")

type externalSubagentQueries interface {
	GetActiveBotAgentByID(context.Context, sqlc.GetActiveBotAgentByIDParams) (sqlc.BotAgent, error)
	GetBotWorkdir(context.Context, sqlc.GetBotWorkdirParams) (sqlc.BotWorkdir, error)
}

func (s *Service) prepareExternalSubagent(ctx context.Context, input CreateInput) (CreateInput, error) {
	queries, ok := s.queries.(externalSubagentQueries)
	if !ok {
		return input, fmt.Errorf("%w: binding lookup unavailable", ErrInvalidExternalSubagent)
	}
	parentID, err := dbpkg.ParseUUID(input.ParentThreadID)
	if err != nil {
		return input, fmt.Errorf("%w: parent session is required", ErrInvalidExternalSubagent)
	}
	parent, err := s.queries.GetSessionByID(ctx, parentID)
	if err != nil {
		return input, fmt.Errorf("%w: load parent: %w", ErrInvalidExternalSubagent, err)
	}
	botID, err := dbpkg.ParseUUID(input.BotID)
	if err != nil || parent.BotID != botID {
		return input, fmt.Errorf("%w: parent belongs to another bot", ErrInvalidExternalSubagent)
	}
	agentID, err := dbpkg.ParseUUID(input.BotAgentID)
	if err != nil {
		return input, fmt.Errorf("%w: bot agent is required", ErrInvalidExternalSubagent)
	}
	agent, err := queries.GetActiveBotAgentByID(ctx, sqlc.GetActiveBotAgentByIDParams{BotID: botID, ID: agentID})
	if err != nil {
		return input, fmt.Errorf("%w: load bot agent: %w", ErrInvalidExternalSubagent, err)
	}
	agentRuntime := agent.Runtime
	if agentRuntime == "acp" {
		agentRuntime = RuntimeACPAgent
	}
	if agentRuntime != strings.TrimSpace(input.RuntimeType) {
		return input, fmt.Errorf("%w: bot agent runtime does not match", ErrInvalidExternalSubagent)
	}
	if agentRuntime == RuntimeACPAgent && metadataString(parseJSONMap(agent.Metadata), "provider") != metadataString(input.RuntimeMetadata, "acp_agent_id") {
		return input, fmt.Errorf("%w: ACP provider does not match", ErrInvalidExternalSubagent)
	}
	if _, err := dbpkg.ParseUUID(metadataString(input.RuntimeMetadata, "runtime_owner_account_id")); err != nil {
		return input, fmt.Errorf("%w: runtime owner account is required", ErrInvalidExternalSubagent)
	}
	parentWorkdirID := uuidText(parent.WorkdirID)
	if input.WorkdirID != "" && input.WorkdirID != parentWorkdirID {
		return input, fmt.Errorf("%w: workdir must match parent", ErrInvalidExternalSubagent)
	}
	input.WorkdirID = parentWorkdirID
	if parent.WorkdirID.Valid {
		workdir, err := queries.GetBotWorkdir(ctx, sqlc.GetBotWorkdirParams{BotID: botID, WorkdirID: parent.WorkdirID})
		if err != nil {
			return input, fmt.Errorf("%w: load parent workdir: %w", ErrInvalidExternalSubagent, err)
		}
		input.WorkdirPath = workdir.Path
	}
	input.Visibility = VisibilityInternal
	return input, nil
}
