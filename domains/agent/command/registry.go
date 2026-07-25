package command

import (
	"context"
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/i18n"
)

// CommandContext carries execution context for a sub-command.
type CommandContext struct {
	Ctx               context.Context
	BotID             string
	Role              string // "owner", "admin", "member", or "" (guest)
	WriteAccess       bool
	Args              []string
	ChannelIdentityID string
	UserID            string
	ChannelType       string
	ConversationType  string
	ConversationID    string
	ThreadID          string
	RouteID           string
	SessionID         string
	Page              int    // zero-based page offset for paginated list commands
	Prov              int    // provider index for the model picker (-1 if absent)
	SelectID          string // stable model id for picker selection ("" if absent)
	Range             string // time-window key for time-series commands ("" = default)
	Locale            string // resolved command-UI locale ("en", "zh", …)
	L                 *i18n.Localizer
}

// T localizes key for this context's command-UI locale, substituting named
// "{placeholder}" params. Safe on a nil Localizer (returns the key), so handlers
// and tests that omit L degrade gracefully.
func (cc CommandContext) T(key string, params ...map[string]any) string {
	return cc.L.T(key, params...)
}

// SubCommand describes a single sub-command within a resource group.
//
// A sub-command provides either Handler (plain text) or ResultHandler
// (structured Result for rich rendering). When both are set, ResultHandler
// takes precedence.
type SubCommand struct {
	Name          string
	Usage         string
	IsWrite       bool
	Handler       func(cc CommandContext) (string, error)
	ResultHandler func(cc CommandContext) (*Result, error)
}

// CommandGroup groups sub-commands under a resource name.
type CommandGroup struct {
	Name          string
	Description   string
	DefaultAction string
	commands      map[string]SubCommand
	order         []string // preserves registration order for help output
}

func newCommandGroup(name, description string) *CommandGroup {
	return &CommandGroup{
		Name:        name,
		Description: description,
		commands:    make(map[string]SubCommand),
	}
}

func (g *CommandGroup) Register(sub SubCommand) {
	g.commands[sub.Name] = sub
	g.order = append(g.order, sub.Name)
}

// Usage returns the usage text for this resource group.
func (g *CommandGroup) Usage(localizers ...*i18n.Localizer) string {
	t := helpLocalizer(localizers...)
	var b strings.Builder
	b.WriteString(MdBold("/"+g.Name) + " — " + commandDescription(t, g) + "\n\n")
	for _, name := range g.order {
		sub := g.commands[name]
		_, summary := localizedActionUsage(t, g.Name, sub)
		line := CmdRef(g.Name + " " + sub.Name)
		if summary != "" {
			line += " — " + summary
		}
		fmt.Fprintf(&b, "- %s\n", line)
	}
	fmt.Fprintf(&b, "\n%s", t.T("cmd.help.runForDetails", map[string]any{"command": CmdRef("help " + g.Name + " <action>")}))
	return strings.TrimRight(b.String(), "\n")
}

func (g *CommandGroup) ActionHelp(action string, localizers ...*i18n.Localizer) string {
	t := helpLocalizer(localizers...)
	sub, ok := g.commands[action]
	if !ok {
		return t.T("cmd.error.unknownAction", map[string]any{"action": MdCode(action), "command": CmdRef(g.Name), "help": CmdRef("help " + g.Name)})
	}
	usage, summary := localizedActionUsage(t, g.Name, sub)
	var b strings.Builder
	b.WriteString(MdBold("/"+g.Name+" "+sub.Name) + "\n")
	if summary != "" {
		fmt.Fprintf(&b, "- %s %s\n", t.T("cmd.help.summaryLabel"), summary)
	}
	if usage == "" {
		usage = sub.Name
	}
	fmt.Fprintf(&b, "- %s %s\n", t.T("cmd.help.usageLabel"), CmdRef(g.Name+" "+usage))
	fmt.Fprintf(&b, "- %s: %s", t.T("cmd.help.siblingActionsLabel"), CmdRef("help "+g.Name))
	return strings.TrimRight(b.String(), "\n")
}

// Registry holds all registered command groups.
type Registry struct {
	groups map[string]*CommandGroup
	order  []string
}

func newRegistry() *Registry {
	return &Registry{
		groups: make(map[string]*CommandGroup),
	}
}

func (r *Registry) RegisterGroup(group *CommandGroup) {
	r.groups[group.Name] = group
	r.order = append(r.order, group.Name)
}

// GlobalHelp returns the top-level help text listing all commands. Commands stay
// short, plain, and tap-to-send; Telegram's manual /command@bot rule appears
// once below the list instead of repeating the username on every row.
func (r *Registry) GlobalHelp(t *i18n.Localizer, commandTarget string) string {
	t = helpLocalizer(t)
	var b strings.Builder
	b.WriteString(MdBold(t.T("cmd.help.availableCommands")) + "\n\n")
	b.WriteString("- " + CommandText("help", "") + " — " + t.T("cmd.help.top.help") + "\n")
	b.WriteString("- " + CommandText("start", "") + " — " + t.T("cmd.help.top.start") + "\n")
	b.WriteString("- " + CommandText("new", "") + " — " + t.T("cmd.help.top.new") + "\n")
	b.WriteString("- " + CommandText("stop", "") + " — " + t.T("cmd.help.top.stop") + "\n")
	for _, name := range r.order {
		group := r.groups[name]
		fmt.Fprintf(&b, "- %s — %s\n", CommandText(group.Name, ""), commandDescription(t, group))
	}
	b.WriteString("\n" + t.T("cmd.help.globalHint"))
	if tip := TelegramGroupCommandTip(t, commandTarget); tip != "" {
		b.WriteString("\n" + tip)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *Registry) GroupHelp(name string, localizers ...*i18n.Localizer) string {
	t := helpLocalizer(localizers...)
	group, ok := r.groups[name]
	if !ok {
		return t.T("cmd.error.unknownCommandShort", map[string]any{"command": CmdRef(name), "help": CmdRef("help")})
	}
	return group.Usage(t)
}

// GroupHelpResult returns an interactive version of GroupHelp: each sub-action
// is a tappable button that dispatches "/{group} {action}" in place, plus a
// "◀ Back" button that returns to the group's default content view. Text-only
// channels fall back to the textual Usage listing.
func (r *Registry) GroupHelpResult(name string, localizers ...*i18n.Localizer) *Result {
	t := helpLocalizer(localizers...)
	group, ok := r.groups[name]
	if !ok {
		return &Result{Text: t.T("cmd.error.unknownCommandShort", map[string]any{"command": CmdRef(name), "help": CmdRef("help")})}
	}
	var b strings.Builder
	b.WriteString(MdBold("/" + group.Name))
	b.WriteString(" — ")
	b.WriteString(commandDescription(t, group))
	b.WriteString("\n\n")
	for _, subName := range group.order {
		sub := group.commands[subName]
		_, summary := localizedActionUsage(t, group.Name, sub)
		line := "- " + CmdRef(group.Name+" "+subName)
		if summary != "" {
			line += " — " + summary
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n" + t.T("cmd.help.chooseAction"))
	title := strings.TrimRight(b.String(), "\n")
	choices := make([]ListItem, 0, len(group.order)+1)
	for _, subName := range group.order {
		choices = append(choices, ListItem{
			Label:  subName,
			Action: &ItemAction{Resource: group.Name, Action: subName},
		})
	}
	// "◀ Back" returns to the group's content (bare /{group}).
	back := group.DefaultAction
	if back == "" {
		back = "list"
	}
	choices = append(choices, ListItem{
		Label:  t.T("cmd.help.backToGroup", map[string]any{"group": commandDescription(t, group)}),
		Action: &ItemAction{Resource: group.Name, Action: back},
	})
	return &Result{
		Text: group.Usage(t),
		Interactive: &Interactive{
			Kind:    InteractiveChoices,
			Choices: &ChoicesView{Title: title, Choices: choices, Columns: 1, BodyEnumeratesChoices: true},
		},
	}
}

func (r *Registry) ActionHelp(groupName, action string, localizers ...*i18n.Localizer) string {
	t := helpLocalizer(localizers...)
	group, ok := r.groups[groupName]
	if !ok {
		return t.T("cmd.error.unknownCommandShort", map[string]any{"command": CmdRef(groupName), "help": CmdRef("help")})
	}
	return group.ActionHelp(action, t)
}

func splitUsage(usage string) (commandUsage string, summary string) {
	usage = strings.TrimSpace(usage)
	if usage == "" {
		return "", ""
	}
	parts := strings.SplitN(usage, " - ", 2)
	commandUsage = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		summary = strings.TrimSpace(parts[1])
	}
	return commandUsage, summary
}

func helpLocalizer(localizers ...*i18n.Localizer) *i18n.Localizer {
	if len(localizers) > 0 && localizers[0] != nil {
		return localizers[0]
	}
	return i18n.New("")
}

func commandDescription(t *i18n.Localizer, group *CommandGroup) string {
	if group == nil {
		return ""
	}
	key := "menu." + group.Name
	if v := t.T(key); v != key {
		return v
	}
	return group.Description
}

func localizedActionUsage(t *i18n.Localizer, groupName string, sub SubCommand) (usage string, summary string) {
	usage, summary = splitUsage(sub.Usage)
	key := "cmd.help.actions." + groupName + "." + sub.Name
	if v := t.T(key); v != key {
		summary = v
	}
	return usage, summary
}
