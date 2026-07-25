package command

import "github.com/memohai/memoh/internal/i18n"

// MenuCommand is one entry for a channel's native slash-command menu (e.g.
// Telegram's setMyCommands), so users discover and tap commands without typing.
type MenuCommand struct {
	Command     string // command name without the leading slash, lowercase
	Description string // short label shown beside the command in the menu
}

// MenuCommands returns the curated slash-command list to advertise in a
// channel's native command menu, with descriptions localized via t. It is the
// single source for that menu; order roughly follows everyday usefulness. Only
// single-token commands belong here — the native menu cannot express sub-actions
// like "schedule list" (those are discovered via /help or in-message buttons).
//
// A nil Localizer renders English (the safe default), which is what transport
// adapters that register the menu without per-bot locale context currently pass.
func MenuCommands(t *i18n.Localizer) []MenuCommand {
	return []MenuCommand{
		{"start", t.T("menu.start")},
		{"help", t.T("menu.help")},
		{"new", t.T("menu.new")},
		{"stop", t.T("menu.stop")},
		{"status", t.T("menu.status")},
		{"context", t.T("menu.context")},
		{"model", t.T("menu.model")},
		{"reasoning", t.T("menu.reasoning")},
		{"settings", t.T("menu.settings")},
		{"language", t.T("menu.language")},
		{"memory", t.T("menu.memory")},
		{"search", t.T("menu.search")},
		{"schedule", t.T("menu.schedule")},
		{"mcp", t.T("menu.mcp")},
		{"usage", t.T("menu.usage")},
		{"email", t.T("menu.email")},
		{"heartbeat", t.T("menu.heartbeat")},
		{"skill", t.T("menu.skill")},
		{"fs", t.T("menu.fs")},
		{"access", t.T("menu.access")},
		{"compact", t.T("menu.compact")},
	}
}
