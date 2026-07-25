package command

import "strings"

func (h *Handler) buildSkillGroup() *CommandGroup {
	g := newCommandGroup("skill", "View bot skills")
	g.DefaultAction = "list"
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list - List all skills",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.skillLoader == nil {
				return &Result{Text: cc.T("cmd.skill.unavailable")}, nil
			}
			if lister, ok := h.skillLoader.(RuntimeSkillLister); ok {
				return runtimeSkillListResult(cc, lister)
			}
			items, err := h.skillLoader.LoadSkills(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return &Result{Text: cc.T("cmd.skill.empty")}, nil
			}
			return buildListResult(cc.T("cmd.skill.title"), "skill", "list", nil, skillListRecords(cc, items, false), cc.Page, defaultListLimit, cc.L), nil
		},
	})
	return g
}

// runtimeSkillListResult renders the runtime-usable (activatable) skill
// catalog — the same safe catalog the Web slash picker shows — with
// tap-to-activate buttons on button-capable channels. Tapping re-dispatches
// the canonical "/<skill-name>" slash, so resolution, permissions and context
// checks all happen at activation time through the normal pipeline.
func runtimeSkillListResult(cc CommandContext, lister RuntimeSkillLister) (*Result, error) {
	items, err := lister.ListRuntimeSkills(cc.Ctx, cc.BotID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return &Result{Text: cc.T("cmd.skill.empty")}, nil
	}
	res := buildListResult(cc.T("cmd.skill.title"), "skill", "list", nil, skillListRecords(cc, items, true), cc.Page, defaultListLimit, cc.L)
	// The activation affordance lives in the body so every channel sees it:
	// button channels read it as the tap alternative, text channels as the
	// only way (rows carry no ItemAction, so no fallback trailer is derived).
	res.Text = strings.TrimRight(res.Text, "\n") + "\n\n" + cc.T("cmd.skill.activateHint")
	return res, nil
}

func skillListRecords(cc CommandContext, items []Skill, activatable bool) []listRecord {
	records := make([]listRecord, 0, len(items))
	for _, item := range items {
		note := truncate(item.Description, 80)
		if strings.EqualFold(strings.TrimSpace(item.Description), strings.TrimSpace(item.Name)) {
			note = "" // description repeats the name; don't print it twice
		}
		rec := listRecord{
			fields: []kv{{cc.T("cmd.common.fieldName"), item.Name}},
			note:   note,
		}
		if activatable {
			rec.callback = EncodeSkillActivateCallback(item.Name)
		}
		records = append(records, rec)
	}
	return records
}
