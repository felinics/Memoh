package command

import (
	"strings"

	emailpkg "github.com/felinics/memoh/internal/email"
)

func (h *Handler) buildEmailGroup() *CommandGroup {
	g := newCommandGroup("email", "View email configuration")
	g.DefaultAction = "outbox" // bare /email lands on recent sends
	g.Register(SubCommand{
		Name:  "providers",
		Usage: "providers - List email providers",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			var (
				items []emailpkg.ProviderResponse
				err   error
			)
			if strings.TrimSpace(cc.UserID) != "" {
				items, err = h.emailService.ListProviders(cc.Ctx, cc.UserID, "")
			} else {
				items, err = h.emailService.ListProvidersInternal(cc.Ctx, "")
			}
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return WithButtons(
					&Result{Text: cc.T("cmd.email.providersEmpty")},
					ListItem{Label: cc.T("cmd.email.section.bindings"), Action: &ItemAction{Resource: "email", Action: "bindings"}},
					ListItem{Label: cc.T("cmd.email.section.outbox"), Action: &ItemAction{Resource: "email", Action: "outbox"}},
				), nil
			}
			records := make([]listRecord, 0, len(items))
			for _, item := range items {
				fields := []kv{{cc.T("cmd.common.fieldName"), item.Name}}
				if eng := distinctProviderEngine(item.Name, item.Provider); eng != "" {
					fields = append(fields, kv{"", eng})
				}
				records = append(records, listRecord{fields: fields})
			}
			result := buildListResult(cc.T("cmd.email.providersTitle"), "email", "providers", nil, records, cc.Page, defaultListLimit, cc.L)
			return WithExtraActions(
				result,
				ListItem{Label: cc.T("cmd.email.section.bindings"), Action: &ItemAction{Resource: "email", Action: "bindings"}},
				ListItem{Label: cc.T("cmd.email.section.outbox"), Action: &ItemAction{Resource: "email", Action: "outbox"}},
			), nil
		},
	})
	g.Register(SubCommand{
		Name:  "bindings",
		Usage: "bindings - List bot email bindings",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			items, err := h.emailService.ListBindings(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return WithButtons(
					&Result{Text: cc.T("cmd.email.bindingsEmpty")},
					ListItem{Label: cc.T("cmd.email.section.providers"), Action: &ItemAction{Resource: "email", Action: "providers"}},
					ListItem{Label: cc.T("cmd.email.section.outbox"), Action: &ItemAction{Resource: "email", Action: "outbox"}},
				), nil
			}
			records := make([]listRecord, 0, len(items))
			for _, item := range items {
				perms := buildPermString(cc, item.CanRead, item.CanWrite, item.CanDelete)
				records = append(records, listRecord{fields: []kv{
					{cc.T("cmd.email.fieldAddress"), item.EmailAddress},
					{cc.T("cmd.email.fieldPermissions"), perms},
				}})
			}
			result := buildListResult(cc.T("cmd.email.bindingsTitle"), "email", "bindings", nil, records, cc.Page, defaultListLimit, cc.L)
			return WithExtraActions(
				result,
				ListItem{Label: cc.T("cmd.email.section.providers"), Action: &ItemAction{Resource: "email", Action: "providers"}},
				ListItem{Label: cc.T("cmd.email.section.outbox"), Action: &ItemAction{Resource: "email", Action: "outbox"}},
			), nil
		},
	})
	g.Register(SubCommand{
		Name:  "outbox",
		Usage: "outbox - List recently sent emails",
		// UPSTREAM REPORT (backend, deferred): to offer the same --range time
		// window as /usage, emailOutboxService.ListByBot + ListEmailOutboxByBot
		// need created_at From/To params. Pagination already covers "view all".
		ResultHandler: func(cc CommandContext) (*Result, error) {
			const pageSize = 10
			page := cc.Page
			if page < 0 {
				page = 0
			}
			items, total, err := h.emailOutboxService.ListByBot(cc.Ctx, cc.BotID, pageSize, int32(page*pageSize)) //nolint:gosec // offset is a small, bounded page index
			if err != nil {
				return nil, err
			}
			// A page past the end (stale Next button, or a hand-typed
			// "--page 999") fetches an empty slice while total>0, which would
			// render an empty body under "Showing 0 of N". Clamp to the last
			// page and refetch so the user lands on real data.
			if total > 0 && page > 0 && page*pageSize >= int(total) {
				page = (int(total) - 1) / pageSize
				items, total, err = h.emailOutboxService.ListByBot(cc.Ctx, cc.BotID, pageSize, int32(page*pageSize)) //nolint:gosec // offset is a small, bounded page index
				if err != nil {
					return nil, err
				}
			}
			if total == 0 {
				return WithButtons(
					&Result{Text: cc.T("cmd.email.outboxEmpty")},
					ListItem{Label: cc.T("cmd.email.section.providers"), Action: &ItemAction{Resource: "email", Action: "providers"}},
					ListItem{Label: cc.T("cmd.email.section.bindings"), Action: &ItemAction{Resource: "email", Action: "bindings"}},
				), nil
			}
			records := make([]listRecord, 0, len(items))
			for _, item := range items {
				to := strings.Join(item.To, ", ")
				// A failed send is the most actionable row — surface its reason.
				note := ""
				if item.Error != "" {
					note = truncate(item.Error, 80)
				}
				// "Sent" is the expected outcome; flag only failures.
				fields := []kv{{cc.T("cmd.email.fieldSubject"), truncate(item.Subject, 40)}}
				if st := strings.ToLower(strings.TrimSpace(item.Status)); st != "sent" && !isSuccessStatus(item.Status) {
					fields = append(fields, kv{cc.T("cmd.common.fieldStatus"), humanizeStatusT(cc, item.Status)})
				}
				fields = append(fields, kv{cc.T("cmd.email.fieldTo"), truncate(to, 40)}, kv{cc.T("cmd.email.fieldSent"), humanizeTimeT(cc, item.SentAt)})
				records = append(records, listRecord{fields: fields, note: note})
			}
			result := buildPagedListResult(cc.T("cmd.email.outboxTitle"), "email", "outbox", nil, records, page, pageSize, int(total), "", cc.L)
			return WithExtraActions(
				result,
				ListItem{Label: cc.T("cmd.email.section.providers"), Action: &ItemAction{Resource: "email", Action: "providers"}},
				ListItem{Label: cc.T("cmd.email.section.bindings"), Action: &ItemAction{Resource: "email", Action: "bindings"}},
			), nil
		},
	})
	return g
}

func buildPermString(cc CommandContext, read, write, del bool) string {
	var parts []string
	if read {
		parts = append(parts, cc.T("cmd.email.permRead"))
	}
	if write {
		parts = append(parts, cc.T("cmd.email.permWrite"))
	}
	if del {
		parts = append(parts, cc.T("cmd.email.permDelete"))
	}
	if len(parts) == 0 {
		return cc.T("cmd.common.none")
	}
	return strings.Join(parts, ", ")
}
