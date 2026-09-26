package tools

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/workdir"
)

// WorkdirLister is the slice of the workdir domain the tool needs.
type WorkdirLister interface {
	List(ctx context.Context, botID string, includeArchived bool) ([]workdir.Workdir, error)
}

// WorkdirProvider exposes the bot's named working directories so the agent
// can reference them by id — today that means binding a scheduled task to a
// workdir via create_schedule.
type WorkdirProvider struct {
	service WorkdirLister
	logger  *slog.Logger
}

func NewWorkdirProvider(log *slog.Logger, service WorkdirLister) *WorkdirProvider {
	if log == nil {
		log = slog.Default()
	}
	return &WorkdirProvider{
		service: service,
		logger:  log.With(slog.String("tool", "workdir")),
	}
}

func (p *WorkdirProvider) Tools(_ context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.service == nil {
		return nil, nil
	}
	sess := session
	return []toolexec.Tool{
		{
			Name:        ToolListWorkdirs().String(),
			Description: "List this bot's workdirs (named working directories): workdir_id, name, target kind (native workspace or remote runtime), and path. Use a workdir_id to bind a scheduled task's sessions to that directory.",
			Parameters:  toolexec.SchemaFor[listWorkdirsArgs](),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, _ listWorkdirsArgs) (sdk.ToolOutput, error) {
				botID := strings.TrimSpace(sess.BotID)
				if botID == "" {
					return sdk.ToolOutput{}, errors.New("bot_id is required")
				}
				workdirs, err := p.service.List(ctx.Context, botID, false)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				items := make([]map[string]any, 0, len(workdirs))
				for _, wd := range workdirs {
					items = append(items, map[string]any{
						"workdir_id":  wd.ID,
						"name":        wd.Name,
						"target_kind": wd.TargetKind,
						"path":        wd.Path,
					})
				}
				return toolexec.OutputFromValue(map[string]any{"workdirs": items, "count": len(items)}), nil
			}),
		},
	}, nil
}

type listWorkdirsArgs struct{}
