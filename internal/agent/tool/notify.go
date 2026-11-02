package tools

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/chat/attention"
)

// ScheduleNotifier is the attention service as notify_user sees it: which
// schedule fire runs in a session, and the mark that tells its owner.
type ScheduleNotifier interface {
	ActiveScheduleFire(ctx context.Context, sessionID string) (attention.ScheduleFire, bool, error)
	Mark(ctx context.Context, sessionID string, kind attention.Kind) (bool, error)
}

// NotifyProvider registers notify_user in the runs of a schedule that tells
// its owner only when a run asks to. Every other run, scheduled or not, tells
// the owner on its own terms and never sees the tool.
type NotifyProvider struct {
	notifier ScheduleNotifier
	logger   *slog.Logger
}

func NewNotifyProvider(log *slog.Logger, notifier ScheduleNotifier) *NotifyProvider {
	if log == nil {
		log = slog.Default()
	}
	return &NotifyProvider{notifier: notifier, logger: log.With(slog.String("tool", "notify"))}
}

type notifyUserArgs struct{}

func (p *NotifyProvider) Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.notifier == nil || session.IsSubagent || session.SessionType != sessionmode.Schedule {
		return nil, nil
	}
	sessionID := strings.TrimSpace(session.SessionID)
	if sessionID == "" {
		return nil, nil
	}
	fire, ok, err := p.notifier.ActiveScheduleFire(ctx, sessionID)
	if err != nil {
		p.logger.WarnContext(ctx, "read schedule fire failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil, nil
	}
	if !ok || fire.NotifyPolicy != attention.NotifyImportant {
		return nil, nil
	}
	return []toolexec.Tool{{
		Name:        ToolNotifyUser().String(),
		Description: "Tell the user that this scheduled run has something they should see. Without this call the user is not told about the run in Memoh. Call it at most once, after the work is done, and only when, judging by the command, the result is something the user would want to know now; do not call it for routine or empty results. Failures and approvals reach the user without it. The user sees only that the run has an update, so put what matters in your reply.",
		Parameters:  toolexec.SchemaFor[notifyUserArgs](),
		Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, _ notifyUserArgs) (sdk.ToolOutput, error) {
			fire, ok, err := p.notifier.ActiveScheduleFire(ctx.Context, sessionID)
			if err != nil {
				return sdk.ToolOutput{}, err
			}
			if !ok {
				return sdk.ToolOutput{}, errors.New("this run is no longer a scheduled run")
			}
			if fire.Notified {
				return toolexec.OutputFromValue(map[string]any{"status": "already_notified"}), nil
			}
			marked, err := p.notifier.Mark(ctx.Context, sessionID, attention.KindUpdate)
			if err != nil {
				return sdk.ToolOutput{}, err
			}
			if !marked {
				// The session cannot carry unread state (an IM conversation,
				// for one): the user reads this run where it happened.
				return toolexec.OutputFromValue(map[string]any{"status": "not_applicable"}), nil
			}
			return toolexec.OutputFromValue(map[string]any{"status": "notified"}), nil
		}),
	}}, nil
}
