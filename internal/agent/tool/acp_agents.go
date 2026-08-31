package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ACPRuntimeSummary is the tool-local projection of a booted ACP runtime's
// state. Mirrored here instead of importing the acp packages: the acp client
// test binary imports this package, so a real dependency would be an import
// cycle.
type ACPRuntimeSummary struct {
	RuntimeID      string
	DefaultModelID string
	CurrentModelID string
	Models         []ACPOptionInfo
	CurrentEffort  string
	Efforts        []ACPOptionInfo
}

// ACPOptionInfo is one selectable model or reasoning effort of an ACP agent.
type ACPOptionInfo struct {
	ID   string
	Name string
}

// ACPRuntimePool is the slice of the ACP session pool the tool needs to
// discover an agent's models and reasoning efforts. Those live only inside a
// running agent process, so the deep listing boots a temporary runtime.
type ACPRuntimePool interface {
	CreateAgentRuntime(ctx context.Context, botID, agentID, runtimeOwnerAccountID string) (ACPRuntimeSummary, error)
	CloseAgentRuntime(botID, runtimeID string) error
}

// ACPAgentsProvider exposes the bot's generic ACP agents so the agent can pick
// one — plus its model and reasoning effort — when creating scheduled tasks.
type ACPAgentsProvider struct {
	pool    ACPRuntimePool
	queries dbstore.Queries
	logger  *slog.Logger
}

func NewACPAgentsProvider(log *slog.Logger, pool ACPRuntimePool, queries dbstore.Queries) *ACPAgentsProvider {
	if log == nil {
		log = slog.Default()
	}
	return &ACPAgentsProvider{
		pool:    pool,
		queries: queries,
		logger:  log.With(slog.String("tool", "acp_agents")),
	}
}

func (p *ACPAgentsProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.pool == nil || p.queries == nil {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name: ToolListACPAgents().String(),
			Description: "List generic ACP agents enabled for this bot. Without arguments this returns the agent catalog instantly. " +
				"Pass agent_id to also fetch that agent's available models and reasoning efforts — this boots a temporary agent runtime and can take many seconds, so only do it when you actually need model/effort ids (e.g. for create_schedule).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_id": map[string]any{
						"type":        "string",
						"description": "Optional ACP agent id from the catalog. When set, the response includes that agent's models and reasoning efforts.",
					},
				},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return nil, errors.New("bot_id is required")
				}
				agentID := acpprofile.NormalizeAgentID(StringArg(args, "agent_id"))
				if agentID == "" {
					return p.listAgents(ctx.Context, botID)
				}
				return p.describeAgent(ctx.Context, botID, agentID, sess)
			},
		},
	}, nil
}

func (p *ACPAgentsProvider) listAgents(ctx context.Context, botID string) (any, error) {
	metadata, err := p.botMetadata(ctx, botID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	for _, profile := range acpprofile.List() {
		enabled := acpprofile.MetadataAgentEnabledRaw(metadata, profile.ID)
		if !enabled {
			continue
		}
		items = append(items, map[string]any{
			"agent_id":     profile.ID,
			"display_name": profile.DisplayName,
			"description":  profile.Description,
		})
	}
	return map[string]any{
		"agents": items,
		"count":  len(items),
		"hint":   "Call again with agent_id to list an agent's models and reasoning efforts.",
	}, nil
}

// describeAgent boots a temporary runtime for the agent (the same path the
// web pre-session model picker uses), reads its model and reasoning state,
// and closes the runtime again.
func (p *ACPAgentsProvider) describeAgent(ctx context.Context, botID, agentID string, sess SessionContext) (any, error) {
	metadata, err := p.botMetadata(ctx, botID)
	if err != nil {
		return nil, err
	}
	if !acpprofile.MetadataAgentEnabledRaw(metadata, agentID) {
		if _, known := acpprofile.Lookup(agentID); !known {
			return nil, fmt.Errorf("unknown ACP agent %q", agentID)
		}
		return nil, fmt.Errorf("ACP agent %q is not enabled for this bot", agentID)
	}
	runtimeOwner := strings.TrimSpace(sess.ChannelIdentityID)
	if runtimeOwner == "" {
		runtimeOwner = strings.TrimSpace(sess.UserID)
	}
	if runtimeOwner == "" {
		return nil, errors.New("no runtime owner identity available for this session")
	}
	status, err := p.pool.CreateAgentRuntime(ctx, botID, agentID, runtimeOwner)
	if err != nil {
		return nil, fmt.Errorf("boot %s runtime: %w", agentID, err)
	}
	defer func() {
		if closeErr := p.pool.CloseAgentRuntime(botID, status.RuntimeID); closeErr != nil {
			p.logger.Warn("close temporary ACP runtime failed",
				slog.String("bot_id", botID),
				slog.String("runtime_id", status.RuntimeID),
				slog.Any("error", closeErr))
		}
	}()

	models := make([]map[string]any, 0, len(status.Models))
	for _, m := range status.Models {
		models = append(models, map[string]any{
			"acp_model_id": m.ID,
			"name":         m.Name,
			"current":      m.ID == status.CurrentModelID,
		})
	}
	efforts := make([]map[string]any, 0, len(status.Efforts))
	for _, e := range status.Efforts {
		efforts = append(efforts, map[string]any{
			"effort":  e.ID,
			"name":    e.Name,
			"current": e.ID == status.CurrentEffort,
		})
	}
	return map[string]any{
		"agent_id":          agentID,
		"models":            models,
		"default_model_id":  status.DefaultModelID,
		"reasoning_efforts": efforts,
	}, nil
}

func (p *ACPAgentsProvider) botMetadata(ctx context.Context, botID string) ([]byte, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	bot, err := p.queries.GetBotByID(ctx, pgBotID)
	if err != nil {
		return nil, fmt.Errorf("get bot: %w", err)
	}
	return bot.Metadata, nil
}
