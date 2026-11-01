package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/botagents"
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
// running agent process: the one the Agent keeps outside any conversation.
type ACPRuntimePool interface {
	AgentOptions(ctx context.Context, botID, agentID string) (ACPRuntimeSummary, error)
}

// ACPAgentsProvider exposes the bot's generic ACP agents so the agent can pick
// one — plus its model and reasoning effort — when creating scheduled tasks.
type ACPAgentsProvider struct {
	pool    ACPRuntimePool
	queries dbstore.Queries
	setups  *botagents.Service
	logger  *slog.Logger
}

func NewACPAgentsProvider(log *slog.Logger, pool ACPRuntimePool, queries dbstore.Queries) *ACPAgentsProvider {
	if log == nil {
		log = slog.Default()
	}
	return &ACPAgentsProvider{
		pool:    pool,
		queries: queries,
		setups:  botagents.NewService(log, queries),
		logger:  log.With(slog.String("tool", "acp_agents")),
	}
}

func (p *ACPAgentsProvider) Tools(_ context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.pool == nil || p.queries == nil {
		return nil, nil
	}
	sess := session
	return []toolexec.Tool{
		{
			Name: ToolListACPAgents().String(),
			Description: "List generic ACP agents enabled for this bot. Without arguments this returns the agent catalog instantly. " +
				"Pass agent_id to also fetch that agent's available models and reasoning efforts — this may start the agent and can take many seconds, so only do it when you actually need model/effort ids (e.g. for create_schedule).",
			Parameters: toolexec.SchemaFor[listACPAgentsArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args listACPAgentsArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				agentID := acpprofile.NormalizeAgentID(args.AgentID)
				if agentID == "" {
					return toolexec.OutputPair(p.listAgents(ctx.Context, botID))
				}
				return toolexec.OutputPair(p.describeAgent(ctx.Context, botID, agentID))
			}),
		},
	}, nil
}

func (p *ACPAgentsProvider) listAgents(ctx context.Context, botID string) (any, error) {
	items := make([]map[string]any, 0)
	for _, profile := range acpprofile.List() {
		enabled, err := p.agentEnabled(ctx, botID, profile.ID)
		if err != nil {
			return nil, err
		}
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

// describeAgent reads the agent's model and reasoning options, the same ones
// the web picker offers before a conversation exists.
func (p *ACPAgentsProvider) describeAgent(ctx context.Context, botID, agentID string) (any, error) {
	if _, known := acpprofile.Lookup(agentID); !known {
		return nil, fmt.Errorf("unknown ACP agent %q", agentID)
	}
	enabled, err := p.agentEnabled(ctx, botID, agentID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, fmt.Errorf("ACP agent %q is not enabled for this bot", agentID)
	}
	status, err := p.pool.AgentOptions(ctx, botID, agentID)
	if err != nil {
		return nil, fmt.Errorf("read %s options: %w", agentID, err)
	}

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

// agentEnabled reports whether the bot can run agentID. The tool addresses
// agents by provider, so this is the setup of the instance a provider-only
// session would run as.
func (p *ACPAgentsProvider) agentEnabled(ctx context.Context, botID, agentID string) (bool, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return false, err
	}
	bot, err := p.queries.GetBotByID(ctx, pgBotID)
	if err != nil {
		return false, fmt.Errorf("get bot: %w", err)
	}
	var metadata map[string]any
	if len(bot.Metadata) > 0 {
		// Undecodable metadata reads as no legacy setup, as it always has.
		_ = json.Unmarshal(bot.Metadata, &metadata)
	}
	setup, err := p.setups.ResolveACPSetup(ctx, botID, "", agentID, metadata)
	if err != nil {
		return false, fmt.Errorf("resolve ACP agent setup: %w", err)
	}
	return setup.Enabled, nil
}

type listACPAgentsArgs struct {
	AgentID string `json:"agent_id,omitempty" jsonschema:"Optional ACP agent id from the catalog. When set, the response includes that agent's models and reasoning efforts."`
}
