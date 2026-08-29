package command

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

var usageRangePresets = []string{"24h", "7d", "30d", "all"}

// resolveUsageRange maps a --range key to a query window. Unknown/empty keys
// default to the last 7 days. Returns the normalized key (for ●-marking the
// active preset), the window start, and a human label.
func resolveUsageRange(cc CommandContext, key string) (norm string, from time.Time, label string) {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "24h":
		return "24h", now.Add(-24 * time.Hour), cc.T("cmd.usage.range24h")
	case "30d":
		return "30d", now.AddDate(0, 0, -30), cc.T("cmd.usage.range30d")
	case "all":
		return "all", time.Unix(0, 0).UTC(), cc.T("cmd.usage.rangeAll")
	default:
		return "7d", now.AddDate(0, 0, -7), cc.T("cmd.usage.range7d")
	}
}

func usageRangeView(action, current string) *Interactive {
	return &Interactive{
		Kind:  InteractiveRange,
		Range: &RangeView{Resource: "usage", Action: action, Current: current, Presets: usageRangePresets},
	}
}

func (h *Handler) buildUsageGroup() *CommandGroup {
	g := newCommandGroup("usage", "View token usage")
	g.DefaultAction = "summary"
	g.Register(SubCommand{
		Name:  "summary",
		Usage: "summary [--range 24h|7d|30d|all] - Token usage summary",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.queries == nil {
				return &Result{Text: cc.T("cmd.usage.unavailable")}, nil
			}
			botUUID, err := parseBotUUID(cc.BotID)
			if err != nil {
				return nil, err
			}
			norm, from, label := resolveUsageRange(cc, cc.Range)
			now := time.Now().UTC()
			fromTS := pgtype.Timestamptz{Time: from, Valid: true}
			toTS := pgtype.Timestamptz{Time: now, Valid: true}
			nullModel := pgtype.UUID{Valid: false}

			rows, err := h.queries.GetTokenUsageByDayAndType(cc.Ctx, dbsqlc.GetTokenUsageByDayAndTypeParams{
				BotID: botUUID, FromTime: fromTS, ToTime: toTS, ModelID: nullModel,
			})
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				return &Result{
					Text:        cc.T("cmd.usage.empty"),
					Interactive: usageRangeView("summary", norm),
				}, nil
			}

			type bucket struct {
				label string
				rows  []dbsqlc.GetTokenUsageByDayAndTypeRow
			}
			buckets := []bucket{{label: cc.T("cmd.usage.bucketChat")}, {label: cc.T("cmd.usage.bucketDiscuss")}, {label: cc.T("cmd.usage.bucketSchedule")}}
			for _, r := range rows {
				switch r.SessionType {
				case "discuss":
					buckets[1].rows = append(buckets[1].rows, r)
				case "schedule":
					buckets[2].rows = append(buckets[2].rows, r)
				default:
					buckets[0].rows = append(buckets[0].rows, r)
				}
			}

			var b strings.Builder
			b.WriteString(MdBold(cc.T("cmd.usage.summaryTitle", map[string]any{"range": label})) + "\n\n")
			first := true
			for _, bk := range buckets {
				if len(bk.rows) == 0 {
					continue
				}
				if !first {
					b.WriteByte('\n')
				}
				first = false
				b.WriteString(MdBold(bk.label) + "\n")
				var totalIn, totalOut int64
				for _, r := range bk.rows {
					day := r.Day.Time.Format("Jan 02")
					// Bulleted lines so WeChat / Weixin / Local-Web preserve the per-day
					// breaks. Indented (2-space) lines without a bullet get collapsed by
					// plain-text IM renderers and the breakdown reads as one paragraph.
					fmt.Fprintf(&b, "- %s · %s\n", day, cc.T("cmd.usage.inOut", map[string]any{"in": formatTokens(r.InputTokens), "out": formatTokens(r.OutputTokens)}))
					totalIn += r.InputTokens
					totalOut += r.OutputTokens
				}
				fmt.Fprintf(&b, "- %s · %s\n", cc.T("cmd.usage.total"), cc.T("cmd.usage.inOut", map[string]any{"in": formatTokens(totalIn), "out": formatTokens(totalOut)}))
			}

			return &Result{
				Text:        strings.TrimRight(b.String(), "\n"),
				Interactive: usageRangeView("summary", norm),
			}, nil
		},
	})
	g.Register(SubCommand{
		Name:  "by-model",
		Usage: "by-model [--range 24h|7d|30d|all] - Token usage grouped by model",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.queries == nil {
				return &Result{Text: cc.T("cmd.usage.unavailable")}, nil
			}
			botUUID, err := parseBotUUID(cc.BotID)
			if err != nil {
				return nil, err
			}
			norm, from, label := resolveUsageRange(cc, cc.Range)
			now := time.Now().UTC()
			fromTS := pgtype.Timestamptz{Time: from, Valid: true}
			toTS := pgtype.Timestamptz{Time: now, Valid: true}

			rows, err := h.queries.GetTokenUsageByModel(cc.Ctx, dbsqlc.GetTokenUsageByModelParams{
				BotID: botUUID, FromTime: fromTS, ToTime: toTS, SessionType: pgtype.Text{},
			})
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				return &Result{
					Text:        cc.T("cmd.usage.empty"),
					Interactive: usageRangeView("by-model", norm),
				}, nil
			}

			var b strings.Builder
			b.WriteString(MdBold(cc.T("cmd.usage.byModelTitle", map[string]any{"range": label})) + "\n\n")
			for _, r := range rows {
				name := r.ModelName
				switch {
				case strings.EqualFold(strings.TrimSpace(name), "unknown"):
					// The SQL COALESCEs missing model/provider joins to "Unknown".
					name = cc.T("cmd.usage.otherModels")
				case strings.TrimSpace(r.ProviderName) != "" &&
					!strings.EqualFold(strings.TrimSpace(r.ProviderName), "unknown") &&
					!strings.Contains(strings.ToLower(name), strings.ToLower(r.ProviderName)):
					name = fmt.Sprintf("%s (%s)", name, r.ProviderName)
				}
				// Bulleted lines so per-model breakdown survives plain-text IMs that
				// fold indented continuation lines.
				fmt.Fprintf(&b, "- %s — %s\n", name, cc.T("cmd.usage.inOut", map[string]any{"in": formatTokens(r.InputTokens), "out": formatTokens(r.OutputTokens)}))
			}

			return &Result{
				Text:        strings.TrimRight(b.String(), "\n"),
				Interactive: usageRangeView("by-model", norm),
			}, nil
		},
	})
	return g
}

func parseBotUUID(botID string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(botID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid bot ID: %w", err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}
