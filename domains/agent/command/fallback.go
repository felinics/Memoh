package command

import (
	"strings"

	"github.com/memohai/memoh/internal/i18n"
)

// HintVerb names the shape of a typeable affordance when rendering the
// no-button fallback trailer. Setting ListView.HintVerb to one of these
// overrides the automatic inference in FallbackTrailer.
//
// The values double as the suffix of the i18n key (cmd.fallback.<verb>) so
// adding a verb is a two-step change: register the constant here and add the
// matching localized template in locales/*.json.
type HintVerb string

const (
	HintVerbSwitch  HintVerb = "switch"
	HintVerbPick    HintVerb = "pick"
	HintVerbToggle  HintVerb = "toggle"
	HintVerbOpen    HintVerb = "open"
	HintVerbDetails HintVerb = "details"
	HintVerbRange   HintVerb = "range"
	HintVerbMenu    HintVerb = "menu"
)

// Typeable renders an ItemAction as the slash command a user would type to
// invoke it (e.g. "/memory set Alice"). Nil-safe.
//
// Unlike ParsedCallback.SyntheticCommand, this is designed for display in hint
// text — it does not append --page artifacts and does not round-trip through
// the callback encoder.
//
// Args containing whitespace are double-quoted so a copy-pasted hint
// round-trips through Parse()/tokenize() back to the same intent. Without
// quoting, `/memory set my provider` would tokenize as ["my", "provider"]
// and the handler would read only "my" — silently picking the wrong target.
func (a *ItemAction) Typeable() string {
	if a == nil {
		return ""
	}
	return formatSlashCommand(a.Resource, a.Action, a.Args, true)
}

// formatSlashCommand builds the canonical "/resource action [args...]" string
// used by both user-facing hint trailers (Typeable) and internal callback
// re-dispatch (ParsedCallback.SyntheticCommand for list-page kind). Returns ""
// for invalid (empty resource or action) input.
//
// When quote is true, whitespace-bearing args are wrapped via quoteArgIfNeeded
// so the output round-trips through Parse()/tokenize(). Both the user-facing
// hint trailer (Typeable, copy-pasted by hand) and internal callback
// re-dispatch (ParsedCallback.SyntheticCommand) pass quote=true: a row tap on a
// space-bearing name (e.g. an MCP connection "My Server") must re-Parse back to
// a single arg, not split into two.
func formatSlashCommand(resource, action string, args []string, quote bool) string {
	resource = strings.TrimSpace(resource)
	action = strings.TrimSpace(action)
	if resource == "" || action == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("/")
	b.WriteString(resource)
	b.WriteString(" ")
	b.WriteString(action)
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		b.WriteString(" ")
		if quote {
			b.WriteString(quoteArgIfNeeded(trimmed))
		} else {
			b.WriteString(trimmed)
		}
	}
	return b.String()
}

// quoteArgIfNeeded wraps an arg in double quotes when it contains whitespace
// that would otherwise split it into separate tokens. Embedded double quotes
// are replaced with single quotes (cheap fallback — tokenize() doesn't handle
// escape sequences, and resource names with literal double quotes are absurd
// in practice). Newlines are stripped because they'd never survive the chat
// transport intact anyway.
func quoteArgIfNeeded(arg string) string {
	if !strings.ContainsAny(arg, " \t\n\r") {
		return arg
	}
	cleaned := strings.NewReplacer("\n", " ", "\r", " ", `"`, "'").Replace(arg)
	return `"` + cleaned + `"`
}

// FallbackTrailer derives a human-readable list of typeable commands from an
// Interactive payload, intended for appending to Result.Text on channels that
// cannot render buttons. Returns "" when the payload offers no typeable
// affordance worth surfacing (display-only list with no extras, suppressed
// choices, nil view).
//
// The returned string carries Markdown markup (backticks via MdCode); the
// renderer's applyMessageFormat strips it for plain-text channels.
func FallbackTrailer(iv *Interactive, t *i18n.Localizer) string {
	if iv == nil {
		return ""
	}
	switch iv.Kind {
	case InteractiveList:
		return trailerForList(iv.List, t)
	case InteractiveChoices:
		return trailerForChoices(iv.Choices, t)
	case InteractiveModelPicker:
		return trailerForPicker(iv.Picker, t)
	case InteractiveRange:
		return trailerForRange(iv.Range, t)
	}
	return ""
}

func trailerForList(lv *ListView, t *i18n.Localizer) string {
	if lv == nil {
		return ""
	}

	var lines []string
	var primaryEmitted bool

	// Try list-level HintVerb override first. If it returns "" — either
	// because the verb doesn't apply at list level (pick/toggle/open/range)
	// or because a future caller used an unrecognized HintVerb value — fall
	// through to row-level inference instead of silently leaving the trailer
	// empty. An empty trailer on a Result with no other text would otherwise
	// produce an outbound "message is required" rejection.
	if v := lv.HintVerb; v != "" {
		if line := listOverrideTrailer(lv, v, t); line != "" {
			lines = append(lines, line)
			primaryEmitted = true
		}
	}

	if !primaryEmitted {
		// Walk actionable rows. Detect homogeneity by (Resource, Action) so a
		// memory/search/model-style list (every row's tap means "switch to this
		// one") collapses into a single switch line rather than enumerating
		// every row's typeable form.
		var actionable []*ItemAction
		resource, action := "", ""
		homogeneous := true
		for _, item := range lv.Items {
			if item.Action == nil {
				continue
			}
			if len(actionable) == 0 {
				resource = item.Action.Resource
				action = item.Action.Action
			} else if item.Action.Resource != resource || item.Action.Action != action {
				homogeneous = false
			}
			actionable = append(actionable, item.Action)
		}

		if len(actionable) > 0 {
			if homogeneous {
				lines = append(lines, t.T("cmd.fallback.switch", map[string]any{
					"command": MdCode("/" + resource + " " + action + " <name>"),
				}))
			} else {
				lines = append(lines, t.T("cmd.fallback.open", map[string]any{
					"commands": joinActionCmds(actionable),
				}))
			}
		}
	}

	// Cross-nav extras: surfaced REGARDLESS of the primary verb path, since the
	// "All commands ▸" affordance disappears on text channels and the user
	// needs the typeable equivalent independently of any row drill-down.
	// Previously the HintVerb override short-circuited the function and
	// silently dropped these — /mcp list and /schedule list users on WeChat
	// saw "See details with /mcp get <name>" but never learned /help mcp.
	var extras []*ItemAction
	for _, ea := range lv.ExtraActions {
		if ea.Action != nil {
			extras = append(extras, ea.Action)
		}
	}
	if len(extras) > 0 {
		lines = append(lines, t.T("cmd.fallback.open", map[string]any{
			"commands": joinActionCmds(extras),
		}))
	}

	return strings.Join(lines, "\n")
}

// listOverrideTrailer synthesizes a pseudo ItemAction for each supported
// list-level HintVerb and routes it through verbLine, so list-level overrides
// share the verb dictionary with row-level overrides (no two-switch drift).
// Verbs that don't naturally apply at the list level return "":
//   - pick/toggle/open need explicit row actions
//   - range needs a RangeView (presets)
//   - menu at list level would self-reference (a /resource list trailer
//     pointing back to /resource list is orientation noise), so the trailer
//     stays empty and the caller falls through to row-level inference
func listOverrideTrailer(lv *ListView, verb HintVerb, t *i18n.Localizer) string {
	resource := strings.TrimSpace(lv.Resource)
	if resource == "" {
		return ""
	}
	var fake *ItemAction
	switch verb {
	case HintVerbDetails:
		fake = &ItemAction{Resource: resource, Action: "get", Args: []string{"<name>"}}
	case HintVerbSwitch:
		fake = &ItemAction{Resource: resource, Action: "set", Args: []string{"<name>"}}
	default:
		return ""
	}
	return verbLine(verb, []*ItemAction{fake}, t)
}

func trailerForChoices(cv *ChoicesView, t *i18n.Localizer) string {
	if cv == nil || cv.BodyEnumeratesChoices {
		return ""
	}
	var actionable []*ItemAction
	for _, ch := range cv.Choices {
		if ch.Action != nil {
			actionable = append(actionable, ch.Action)
		}
	}
	if len(actionable) == 0 {
		return ""
	}

	// Homogeneity by (Resource, Action). A homogeneous choice set is either a
	// pick (args are plain enum values like "low") or a flag-bearing toggle
	// (args have leading dashes like "--heartbeat_enabled true").
	resource := actionable[0].Resource
	action := actionable[0].Action
	homogeneous := true
	for _, a := range actionable[1:] {
		if a.Resource != resource || a.Action != action {
			homogeneous = false
			break
		}
	}

	if homogeneous {
		if isPickShape(actionable) {
			if hasAnyArgs(actionable) {
				return t.T("cmd.fallback.pick", map[string]any{
					"command": MdCode("/" + resource + " " + action + " " + pickValueClause(actionable)),
				})
			}
			// Single no-arg target (e.g. /mcp empty's "All commands ▸" → /help mcp):
			// surface it as a direct menu nudge rather than a templated pick.
			return t.T("cmd.fallback.menu", map[string]any{
				"command": MdCode(actionable[0].Typeable()),
			})
		}
		return t.T("cmd.fallback.toggle", map[string]any{
			"commands": joinActionCmds(actionable),
		})
	}

	// Heterogeneous: list every typeable as a cross-nav opener.
	return t.T("cmd.fallback.open", map[string]any{
		"commands": joinActionCmds(actionable),
	})
}

// isPickShape reports whether the actions look like a value-pick (args are
// plain enum tokens) rather than a toggle (args carry leading-dash flags).
func isPickShape(actions []*ItemAction) bool {
	for _, a := range actions {
		for _, arg := range a.Args {
			if strings.HasPrefix(strings.TrimSpace(arg), "-") {
				return false
			}
		}
	}
	return true
}

// hasAnyArgs reports whether any of the actions carry at least one non-blank
// arg. A no-args homogeneous group renders as a direct menu nudge rather than
// a "<value>" template.
func hasAnyArgs(actions []*ItemAction) bool {
	for _, a := range actions {
		for _, arg := range a.Args {
			if strings.TrimSpace(arg) != "" {
				return true
			}
		}
	}
	return false
}

// pickValueClause builds the "<v1|v2|v3>" enumeration clause from a pick-shape
// choice set's first args, or "<value>" when no usable values are available.
// Duplicates are removed in encounter order so the catalog reads cleanly.
func pickValueClause(actions []*ItemAction) string {
	seen := make(map[string]bool, len(actions))
	var values []string
	for _, a := range actions {
		if len(a.Args) == 0 {
			continue
		}
		v := strings.TrimSpace(a.Args[0])
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		values = append(values, v)
	}
	if len(values) == 0 {
		return "<value>"
	}
	return "<" + strings.Join(values, "|") + ">"
}

func trailerForPicker(p *ModelPickerView, t *i18n.Localizer) string {
	if p == nil {
		return ""
	}
	switch p.Level {
	case LevelProviders:
		if len(p.Providers) == 0 {
			return ""
		}
		return t.T("cmd.fallback.menu", map[string]any{
			"command": MdCode("/model list <provider_name>"),
		})
	case LevelModels:
		if len(p.Models) == 0 {
			return ""
		}
		// Provider-scoped list: a single-arg "/model set <name>" is resolved
		// globally and errors as ambiguous when the same model name exists under
		// more than one provider. The selection the user actually drilled into is
		// the two-arg, provider-qualified form, so emit that with the concrete
		// provider (quoted when it contains spaces so it survives re-parse).
		if provider := strings.TrimSpace(p.ProviderName); provider != "" {
			return t.T("cmd.fallback.menu", map[string]any{
				"command": MdCode("/model set " + quoteArgIfNeeded(provider) + " <model_name>"),
			})
		}
		return t.T("cmd.fallback.menu", map[string]any{
			"command": MdCode("/model set <name>"),
		})
	}
	return ""
}

func trailerForRange(rv *RangeView, t *i18n.Localizer) string {
	if rv == nil {
		return ""
	}
	resource := strings.TrimSpace(rv.Resource)
	action := strings.TrimSpace(rv.Action)
	if resource == "" || action == "" || len(rv.Presets) == 0 {
		return ""
	}
	return t.T("cmd.fallback.range", map[string]any{
		"command": MdCode("/" + resource + " " + action + " --range <preset>"),
		"presets": strings.Join(rv.Presets, " · "),
	})
}

// verbLine renders a single trailer line for the given verb. Used by
// listOverrideTrailer to honor list-level HintVerb overrides.
// Verbs that need data not available at this level (HintVerbRange needs
// presets that only RangeView carries) return "".
func verbLine(verb HintVerb, actions []*ItemAction, t *i18n.Localizer) string {
	if len(actions) == 0 {
		return ""
	}
	switch verb {
	case HintVerbSwitch, HintVerbPick, HintVerbDetails, HintVerbMenu:
		cmd := actions[0].Typeable()
		if cmd == "" {
			return ""
		}
		return t.T("cmd.fallback."+string(verb), map[string]any{"command": MdCode(cmd)})
	case HintVerbToggle:
		return t.T("cmd.fallback.toggle", map[string]any{"commands": joinActionCmds(actions)})
	case HintVerbOpen:
		return t.T("cmd.fallback.open", map[string]any{"commands": joinActionCmds(actions)})
	case HintVerbRange:
		// Range needs the preset list, which lives on RangeView, not on rows.
		// Trying to render a range trailer from row actions would leak a
		// literal "{presets}" placeholder. Use a RangeView Interactive instead.
		return ""
	}
	return ""
}

// joinActionCmds formats the commands of multiple actions for the {commands}
// placeholder. Single-action result: " /cmd" (leading space so templates like
// "Open:{commands}" render as "Open: /cmd"). Multi-action: newline-bullet
// list ("\n- /a\n- /b"). Empty: "" (the template ends up with a dangling
// label and an empty commands clause — callers should guard).
func joinActionCmds(actions []*ItemAction) string {
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		if cmd := a.Typeable(); cmd != "" {
			parts = append(parts, MdCode(cmd))
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return " " + parts[0]
	default:
		return "\n- " + strings.Join(parts, "\n- ")
	}
}
