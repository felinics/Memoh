package command

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/schedule"
)

func (h *Handler) buildScheduleGroup() *CommandGroup {
	g := newCommandGroup("schedule", "Manage scheduled tasks")
	g.DefaultAction = "list" // bare /schedule lands on the live schedule list
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list - List all schedules",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			items, err := h.scheduleService.List(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return WithButtons(
					&Result{Text: cc.T("cmd.schedule.empty", map[string]any{"command": CmdRef(`schedule create daily "0 9 * * *" "Send the report"`)})},
					ListItem{Label: cc.T("cmd.common.allCommands"), Action: &ItemAction{Resource: "help", Action: "schedule"}},
				), nil
			}
			records := make([]listRecord, 0, len(items))
			for _, item := range items {
				// The cron phrase is the identifying fact, so it leads as the chip;
				// a status chip appears only when the schedule is paused (an
				// enabled schedule is the expected state and needs no flag).
				fields := []kv{
					{cc.T("cmd.common.fieldName"), item.Name},
					{"", humanizeCronT(cc, item.Pattern)},
				}
				if !item.Enabled {
					fields = append(fields, kv{"", cc.T("cmd.schedule.paused")})
				}
				note := ""
				if d := strings.TrimSpace(item.Description); d != "" && !strings.EqualFold(d, strings.TrimSpace(item.Name)) {
					note = truncate(d, 60)
				}
				records = append(records, listRecord{
					fields: fields,
					note:   note,
					// Tap a schedule to open its details — no typing of /schedule get.
					action: &ItemAction{Resource: "schedule", Action: "get", Args: []string{item.Name}},
				})
			}
			result := buildListResult(cc.T("cmd.schedule.title"), "schedule", "list", nil, records, cc.Page, defaultListLimit, cc.L)
			if result.Interactive != nil && result.Interactive.List != nil {
				result.Interactive.List.HintVerb = HintVerbDetails
			}
			return WithExtraActions(result,
				ListItem{Label: cc.T("cmd.common.allCommands"), Action: &ItemAction{Resource: "help", Action: "schedule"}},
			), nil
		},
	})
	g.Register(SubCommand{
		Name:  "get",
		Usage: "get <name> - Get schedule details",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if len(cc.Args) < 1 {
				return &Result{Text: cc.T("cmd.schedule.getUsage", map[string]any{"command": CmdRef("schedule get <name>")})}, nil
			}
			item, err := h.findScheduleByName(cc, cc.Args[0])
			if err != nil {
				return nil, err
			}
			status := cc.T("cmd.common.active")
			if !item.Enabled {
				status = cc.T("cmd.schedule.paused")
			}
			runs := strconv.Itoa(item.CurrentCalls)
			if item.MaxCalls != nil {
				runs = cc.T("cmd.schedule.runsOf", map[string]any{"current": item.CurrentCalls, "max": *item.MaxCalls})
			}
			desc := item.Description
			if d := strings.TrimSpace(desc); d == "" ||
				strings.EqualFold(d, strings.TrimSpace(item.Name)) ||
				strings.EqualFold(d, strings.TrimSpace(item.Command)) {
				desc = ""
			}
			pairs := []kv{
				{cc.T("cmd.schedule.fieldDescription"), desc},
				{cc.T("cmd.schedule.fieldSchedule"), humanizeCronT(cc, item.Pattern)},
				{cc.T("cmd.schedule.fieldCommand"), item.Command},
				{cc.T("cmd.common.fieldStatus"), status},
				{cc.T("cmd.schedule.fieldRuns"), runs},
				{cc.T("cmd.common.fieldCreated"), humanizeTimeT(cc, item.CreatedAt)},
			}
			if !item.UpdatedAt.Truncate(time.Second).Equal(item.CreatedAt.Truncate(time.Second)) {
				pairs = append(pairs, kv{cc.T("cmd.common.fieldUpdated"), humanizeTimeT(cc, item.UpdatedAt)})
			}
			return WithButtons(
				&Result{Text: formatKVTitled(item.Name, pairs)},
				ListItem{Label: cc.T("cmd.schedule.back"), Action: &ItemAction{Resource: "schedule", Action: "list"}},
				ListItem{Label: cc.T("cmd.common.allCommands"), Action: &ItemAction{Resource: "help", Action: "schedule"}},
			), nil
		},
	})
	g.Register(SubCommand{
		Name:    "create",
		Usage:   "create <name> <pattern> <command> - Create a schedule",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 3 {
				return cc.T("cmd.schedule.createUsage"), nil
			}
			name := cc.Args[0]
			pattern := cc.Args[1]
			command := strings.Join(cc.Args[2:], " ")
			item, err := h.scheduleService.Create(cc.Ctx, cc.BotID, schedule.CreateRequest{
				Name:        name,
				Description: name,
				Pattern:     pattern,
				Command:     command,
			})
			if err != nil {
				return "", err
			}
			// Echo the humanized cron + command so the user can confirm the
			// pattern was parsed as intended ("did 0 9 * * * mean 9am?").
			return cc.T("cmd.schedule.created", map[string]any{
				"name":    MdCode(item.Name),
				"runs":    renderValue(humanizeCronT(cc, item.Pattern)),
				"command": renderValue(item.Command),
			}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "update",
		Usage:   "update <name> [--pattern P] [--command C] - Update a schedule",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.schedule.updateUsage"), nil
			}
			item, err := h.findScheduleByName(cc, cc.Args[0])
			if err != nil {
				return "", err
			}
			req := schedule.UpdateRequest{}
			args := cc.Args[1:]
			for i := 0; i < len(args); i++ {
				if i+1 >= len(args) {
					break
				}
				switch args[i] {
				case "--name":
					i++
					req.Name = &args[i]
				case "--pattern":
					i++
					req.Pattern = &args[i]
				case "--command":
					i++
					val := strings.Join(args[i:], " ")
					req.Command = &val
					i = len(args)
				case "--enabled":
					i++
					v := strings.ToLower(args[i]) == "true"
					req.Enabled = &v
				}
			}
			updated, err := h.scheduleService.Update(cc.Ctx, item.ID, req)
			if err != nil {
				return "", err
			}
			return cc.T("cmd.schedule.updated", map[string]any{"name": MdCode(updated.Name)}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "delete",
		Usage:   "delete <name> - Delete a schedule",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.schedule.deleteUsage"), nil
			}
			item, err := h.findScheduleByName(cc, cc.Args[0])
			if err != nil {
				return "", err
			}
			if err := h.scheduleService.Delete(cc.Ctx, item.ID); err != nil {
				return "", err
			}
			return cc.T("cmd.schedule.deleted", map[string]any{"name": MdCode(item.Name)}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "enable",
		Usage:   "enable <name> - Enable a schedule",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.schedule.enableUsage"), nil
			}
			item, err := h.findScheduleByName(cc, cc.Args[0])
			if err != nil {
				return "", err
			}
			enabled := true
			_, err = h.scheduleService.Update(cc.Ctx, item.ID, schedule.UpdateRequest{Enabled: &enabled})
			if err != nil {
				return "", err
			}
			return cc.T("cmd.schedule.enabled", map[string]any{"name": MdCode(item.Name)}), nil
		},
	})
	g.Register(SubCommand{
		Name:    "disable",
		Usage:   "disable <name> - Disable a schedule",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.schedule.disableUsage"), nil
			}
			item, err := h.findScheduleByName(cc, cc.Args[0])
			if err != nil {
				return "", err
			}
			enabled := false
			_, err = h.scheduleService.Update(cc.Ctx, item.ID, schedule.UpdateRequest{Enabled: &enabled})
			if err != nil {
				return "", err
			}
			return cc.T("cmd.schedule.pausedDone", map[string]any{"name": MdCode(item.Name)}), nil
		},
	})
	return g
}

func (h *Handler) findScheduleByName(cc CommandContext, name string) (schedule.Schedule, error) {
	items, err := h.scheduleService.List(cc.Ctx, cc.BotID)
	if err != nil {
		return schedule.Schedule{}, err
	}
	for _, item := range items {
		if strings.EqualFold(item.Name, name) {
			return item, nil
		}
	}
	return schedule.Schedule{}, fmt.Errorf("%s", cc.T("cmd.schedule.notFound", map[string]any{"name": fmt.Sprintf("%q", name), "command": CmdRef("schedule list")}))
}
