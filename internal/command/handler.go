package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/bots"
	dbstore "github.com/felinics/memoh/internal/db/store"
	emailpkg "github.com/felinics/memoh/internal/email"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/schedule"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/settings"
)

// MemberRoleResolver resolves a user's role within a bot.
type MemberRoleResolver interface {
	GetMemberRole(ctx context.Context, botID, channelIdentityID string) (string, error)
}

// ChannelManageResolver reports whether a channel identity has been granted the
// manage capability on a bot (Channel Access "Manage"). It lets an IM identity
// run owner-only slash commands without being the web owner.
type ChannelManageResolver interface {
	HasManageGrant(ctx context.Context, botID, channelIdentityID string) (bool, error)
}

// BotMemberRoleAdapter adapts bots.Service to MemberRoleResolver.
type BotMemberRoleAdapter struct {
	BotService     *bots.Service
	ManageResolver ChannelManageResolver
}

func (a *BotMemberRoleAdapter) GetMemberRole(ctx context.Context, botID, channelIdentityID string) (string, error) {
	bot, err := a.BotService.Get(ctx, botID)
	if err != nil {
		return "", err
	}
	if bot.OwnerUserID == channelIdentityID {
		return "owner", nil
	}
	// A channel identity explicitly granted manage in Channel Access counts as a
	// manager, which carries the same write-command access as the owner.
	if a.ManageResolver != nil && strings.TrimSpace(channelIdentityID) != "" {
		granted, err := a.ManageResolver.HasManageGrant(ctx, botID, channelIdentityID)
		if err != nil {
			return "", err
		}
		if granted {
			return "manager", nil
		}
	}
	return "", nil
}

// Handler processes slash commands intercepted before they reach the LLM.
type Handler struct {
	registry        *Registry
	roleResolver    MemberRoleResolver
	scheduleService *schedule.Service
	settingsService *settings.Service
	mcpConnService  *mcp.ConnectionService

	modelsService      *models.Service
	providersService   *providers.Service
	memProvService     *memprovider.Service
	searchProvService  *searchproviders.Service
	emailService       *emailpkg.Service
	emailOutboxService *emailpkg.OutboxService
	compactionService  *compaction.Service
	queries            CommandQueries
	sqlcQueries        dbstore.Queries
	aclEvaluator       AccessEvaluator
	skillLoader        SkillLoader
	containerFS        ContainerFS
	linkConsumer       LinkConsumer

	logger *slog.Logger
}

// ExecuteInput carries the caller identity and channel context for command execution.
type ExecuteInput struct {
	BotID             string
	ChannelIdentityID string
	UserID            string
	Text              string
	// Invocation is the canonical channel parse. When present, command handling
	// must use it instead of interpreting Text again.
	Invocation       *Invocation
	ChannelType      string
	ConversationType string
	ConversationID   string
	ThreadID         string
	RouteID          string
	SessionID        string
	// CommandTarget optionally identifies the bot username for transport-specific
	// command guidance. Channel callers set it only for Telegram group messages.
	CommandTarget string
	// Locale optionally pins the command-UI locale. When empty, ExecuteResult
	// resolves it from the bot's command_ui_language setting (auto → en).
	Locale string
}

// NewHandler creates a Handler with all required services.
func NewHandler(
	log *slog.Logger,
	roleResolver MemberRoleResolver,
	scheduleService *schedule.Service,
	settingsService *settings.Service,
	mcpConnService *mcp.ConnectionService,
	modelsService *models.Service,
	providersService *providers.Service,
	memProvService *memprovider.Service,
	searchProvService *searchproviders.Service,
	emailService *emailpkg.Service,
	emailOutboxService *emailpkg.OutboxService,
	queries CommandQueries,
	aclEvaluator AccessEvaluator,
	skillLoader SkillLoader,
	containerFS ContainerFS,
) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{
		roleResolver:       roleResolver,
		scheduleService:    scheduleService,
		settingsService:    settingsService,
		mcpConnService:     mcpConnService,
		modelsService:      modelsService,
		providersService:   providersService,
		memProvService:     memProvService,
		searchProvService:  searchProvService,
		emailService:       emailService,
		emailOutboxService: emailOutboxService,
		queries:            queries,
		aclEvaluator:       aclEvaluator,
		skillLoader:        skillLoader,
		containerFS:        containerFS,
		logger:             log.With(slog.String("component", "command")),
	}
	h.registry = h.buildRegistry()
	return h
}

// SetCompactionService configures the compaction service for the /compact command.
func (h *Handler) SetCompactionService(s *compaction.Service, q dbstore.Queries) {
	h.compactionService = s
	h.sqlcQueries = q
}

// CurrentContext resolves the bot's current model/reasoning state for
// enriching command output (e.g. the /new confirmation). It is a read-only view
// over existing bot settings and makes no changes.
func (h *Handler) CurrentContext(ctx context.Context, botID string) (CurrentContext, error) {
	loc := i18n.New(h.ResolveLocale(ctx, botID))
	cc := CommandContext{Ctx: ctx, BotID: strings.TrimSpace(botID), Locale: loc.Locale(), L: loc}
	s, err := h.getBotSettings(cc)
	if err != nil {
		return CurrentContext{}, err
	}
	return CurrentContext{
		ChatModel:       h.resolveModelName(cc, s.ChatModelID),
		ReasoningEffort: s.ReasoningEffort,
		ContextWindow:   h.resolveContextWindow(cc),
	}, nil
}

// topLevelCommands are standalone commands (no sub-actions) that IsCommand
// recognises and that are handled outside the regular resource-group dispatch
// (the channel inbound processor has the routing context they need). Only
// /help, /start, /new, /stop are advertised in /help output. /approve, /reject,
// and /respond are internal continuation protocol verbs that users discover via
// the active prompt, not via the help listing.
//
// The map carries no per-key data — membership is the only fact callers need.
// Localized descriptions for the advertised entries live under `cmd.help.top.*`
// in the i18n catalog.
var topLevelCommands = map[string]struct{}{
	"start":   {},
	"new":     {},
	"stop":    {},
	"approve": {},
	"reject":  {},
	"respond": {},
}

// resourceAliases maps alternate spellings to the canonical command resource so
// that common variants all resolve (e.g. /setting, /reason, /effort, /think,
// /commands). Keys and values are lowercase (Parse lowercases the resource).
var resourceAliases = map[string]string{
	"setting":   "settings",
	"commands":  "help",
	"cmds":      "help",
	"model":     "model",
	"models":    "model",
	"reason":    "reasoning",
	"reasoning": "reasoning",
	"effort":    "reasoning",
	"think":     "reasoning",
}

// canonicalResource resolves a parsed resource through resourceAliases.
func canonicalResource(resource string) string {
	if c, ok := resourceAliases[resource]; ok {
		return c
	}
	return resource
}

// IsCommand reports whether the text contains a slash command.
// Handles both direct commands ("/help") and mention-prefixed commands ("@bot /help").
func (h *Handler) IsCommand(text string) bool {
	cmdText := ExtractCommandText(text)
	if cmdText == "" || len(cmdText) < 2 {
		return false
	}
	// Validate that it refers to a known command, not arbitrary "/path/to/file".
	parsed, err := Parse(cmdText)
	if err != nil {
		return false
	}
	return h.HasCommandResource(parsed.Resource)
}

// HasCommandResource checks registry membership for an already parsed command.
// Channel classification uses this form so it never reparses a synthetic string.
func (h *Handler) HasCommandResource(resource string) bool {
	resource = canonicalResource(strings.ToLower(strings.TrimSpace(resource)))
	if resource == "help" {
		return true
	}
	if _, ok := topLevelCommands[resource]; ok {
		return true
	}
	_, ok := h.registry.groups[resource]
	return ok
}

// IsCommandShaped reports whether text looks like a slash command (a leading
// slash followed by a command-name token), whether or not it is registered. It
// lets the channel layer reply with a helpful "unknown command" hint instead of
// forwarding a mistyped command to the model. Paths/URLs (e.g. "/a/b") are
// rejected so they are not mistaken for commands.
func (*Handler) IsCommandShaped(text string) bool {
	cmdText := ExtractCommandText(text)
	if cmdText == "" || len(cmdText) < 2 {
		return false
	}
	parsed, err := Parse(cmdText)
	if err != nil {
		return false
	}
	return isCommandName(parsed.Resource)
}

// isCommandName reports whether r is a plausible command name: a letter followed
// by letters/digits/_/-, at most 32 chars. This excludes paths ("path/to/file"),
// which contain a slash, and other non-command slashes.
func isCommandName(r string) bool {
	if r == "" || len(r) > 32 {
		return false
	}
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case (c >= '0' && c <= '9' || c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// UnknownCommandMessage is the reply for slash-command-shaped input that is not a
// known command. It points the user at /commands and offers the no-slash escape.
func UnknownCommandMessage(t *i18n.Localizer, text string) string {
	parsed, _ := Parse(ExtractCommandText(text))
	return t.T("cmd.error.unknownCommand", map[string]any{
		"command": CmdRef(parsed.Resource),
		"help":    CmdRef("commands"),
	})
}

// AccessDeniedMessage is the reply sent when the chat ACL denies a sender. It
// doubles as the binding hint so a forgetful owner learns how to link in
// (/link) while a genuine outsider learns they aren't permitted. A manager gets
// a narrower message because Manage access does not imply Chat access.
func AccessDeniedMessage(t *i18n.Localizer, role string) string {
	if strings.TrimSpace(role) == "manager" {
		return t.T("cmd.error.accessDeniedManager")
	}
	return t.T("cmd.error.accessDeniedHint", map[string]any{
		"link": CmdRef("link"),
	})
}

// MemberRole resolves the sender's bot role for presentation decisions outside
// command execution. Callers must not use it as an authorization shortcut.
func (h *Handler) MemberRole(ctx context.Context, botID, channelIdentityID string) (string, error) {
	if h == nil || h.roleResolver == nil || strings.TrimSpace(channelIdentityID) == "" {
		return "", nil
	}
	return h.roleResolver.GetMemberRole(ctx, strings.TrimSpace(botID), strings.TrimSpace(channelIdentityID))
}

// ResolveLocale resolves the command-UI locale for a bot from its
// command_ui_language setting (auto/unknown → server default). Any settings I/O
// error falls back to the default locale, so command rendering never blocks on
// it. Exported so the channel layer can localize its own renderer chrome and
// operational-failure messages with the same locale.
//
// Uses the scoped GetCommandUILanguage (single DB query) rather than GetBot
// (which also fetches the ACL default-effect) — the locale is resolved per
// command and per interactive callback tap, so single-query matters for
// paginated lists where users tap Prev/Next repeatedly.
func (h *Handler) ResolveLocale(ctx context.Context, botID string) string {
	if h == nil || h.settingsService == nil || strings.TrimSpace(botID) == "" {
		return i18n.DefaultLocale
	}
	lang, err := h.settingsService.GetCommandUILanguage(ctx, strings.TrimSpace(botID))
	if err != nil {
		return i18n.DefaultLocale
	}
	return i18n.Resolve(lang)
}

// Execute parses and runs a slash command, returning the text reply.
func (h *Handler) Execute(ctx context.Context, botID, channelIdentityID, text string) (string, error) {
	return h.ExecuteWithInput(ctx, ExecuteInput{
		BotID:             botID,
		ChannelIdentityID: channelIdentityID,
		Text:              text,
	})
}

// ExecuteWithInput parses and runs a slash command with channel/session
// context, returning the plain-text reply. It delegates to ExecuteResult and
// flattens the structured result to its text form.
func (h *Handler) ExecuteWithInput(ctx context.Context, input ExecuteInput) (string, error) {
	res, err := h.ExecuteResult(ctx, input)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.Text, nil
}

// ExecuteResult parses and runs a slash command, returning a neutral Result.
// The Result always carries complete Text; Interactive is set only by commands
// that opt into rich rendering via SubCommand.ResultHandler.
func (h *Handler) ExecuteResult(ctx context.Context, input ExecuteInput) (res *Result, err error) {
	// Resolve the command-UI locale once and stamp it onto whatever Result we
	// return, so the channel renderer can localize its own chrome to match.
	localeStr := strings.TrimSpace(input.Locale)
	if localeStr == "" {
		localeStr = h.ResolveLocale(ctx, input.BotID)
	}
	loc := i18n.New(localeStr)
	localeStr = loc.Locale()
	defer func() {
		if res != nil && res.Locale == "" {
			res.Locale = localeStr
		}
	}()

	parsed, ok := parsedCommandFromInput(input)
	if !ok {
		return &Result{Text: h.registry.GlobalHelp(loc, input.CommandTarget)}, nil
	}

	// Resolve the user's role in this bot.
	role := ""
	roleIdentityID := strings.TrimSpace(input.ChannelIdentityID)
	if h.roleResolver != nil && roleIdentityID != "" {
		r, err := h.roleResolver.GetMemberRole(ctx, input.BotID, roleIdentityID)
		if err != nil {
			h.logger.Warn("failed to resolve member role",
				slog.String("bot_id", input.BotID),
				slog.String("role_identity_id", roleIdentityID),
				slog.Any("error", err),
			)
		} else {
			role = r
		}
	}
	writeAccess := role == "owner" || role == "manager"

	resource := canonicalResource(parsed.Resource)
	// /language <lang> shorthand → /language set <lang>. Must run BEFORE cc is
	// built: cc.Args is frozen from parsed.Args below, so rewriting parsed.Args
	// after construction would leave the `language set` handler reading an empty
	// arg slice and emitting usage text instead of switching the language.
	normalizeLanguageShorthand(resource, &parsed)
	normalizeLinkShorthand(resource, &parsed)

	cc := CommandContext{
		Ctx:               ctx,
		BotID:             input.BotID,
		Role:              role,
		WriteAccess:       writeAccess,
		Args:              parsed.Args,
		ChannelIdentityID: strings.TrimSpace(input.ChannelIdentityID),
		UserID:            strings.TrimSpace(input.UserID),
		ChannelType:       strings.TrimSpace(input.ChannelType),
		ConversationType:  strings.TrimSpace(input.ConversationType),
		ConversationID:    strings.TrimSpace(input.ConversationID),
		ThreadID:          strings.TrimSpace(input.ThreadID),
		RouteID:           strings.TrimSpace(input.RouteID),
		SessionID:         strings.TrimSpace(input.SessionID),
		Page:              parsed.Page,
		Prov:              parsed.Prov,
		SelectID:          parsed.SelectID,
		Range:             parsed.Range,
		Locale:            localeStr,
		L:                 loc,
	}

	// ACL gate for commands: an outsider who cannot chat with the bot must not be
	// able to operate it via slash commands either. The binding entry point
	// (/link) is always allowed so a holder of a valid code can bind; owners and
	// managers are always allowed and can recover even in whitelist mode; everyone
	// else must pass the chat ACL.
	// (Write commands are additionally gated by writeAccess below.)
	if resource != "link" && !writeAccess {
		allowed, aclErr := h.chatACLAllows(cc)
		if aclErr != nil {
			h.logger.Warn("command acl evaluation failed",
				slog.String("bot_id", input.BotID),
				slog.String("resource", resource),
				slog.Any("error", aclErr),
			)
			return &Result{Text: cc.T("cmd.error.noAccess")}, nil
		}
		if !allowed {
			return &Result{Text: cc.T("cmd.error.noAccess")}, nil
		}
	}

	// /help (and its alias /commands)
	if resource == "help" {
		switch {
		case parsed.Action == "":
			return &Result{Text: h.registry.GlobalHelp(cc.L, input.CommandTarget)}, nil
		case len(parsed.Args) == 0:
			return h.registry.GroupHelpResult(parsed.Action, cc.L), nil
		default:
			return &Result{Text: h.registry.ActionHelp(parsed.Action, parsed.Args[0], cc.L)}, nil
		}
	}

	// Top-level commands (e.g. /new) are handled by the channel inbound
	// processor which has the required routing context. If Execute is
	// called for one of these, return a short usage hint.
	if _, ok := topLevelCommands[resource]; ok {
		return &Result{Text: fmt.Sprintf("/%s — %s", resource, cc.T("cmd.help.top."+resource))}, nil
	}

	group, ok := h.registry.groups[resource]
	if !ok {
		return &Result{Text: cc.T("cmd.error.unknownCommandShort", map[string]any{"command": CmdRef(parsed.Resource), "help": CmdRef("help")})}, nil
	}

	if parsed.Action == "" {
		if group.DefaultAction != "" {
			parsed.Action = group.DefaultAction
		} else {
			return &Result{Text: group.Usage(cc.L)}, nil
		}
	}

	sub, ok := group.commands[parsed.Action]
	if !ok {
		return &Result{Text: cc.T("cmd.error.unknownAction", map[string]any{"action": MdCode(parsed.Action), "command": CmdRef(parsed.Resource), "help": CmdRef("help " + parsed.Resource)})}, nil
	}

	if sub.IsWrite && !writeAccess {
		return &Result{Text: cc.T("cmd.error.ownerOnly", map[string]any{"command": CmdRef(parsed.Resource)})}, nil
	}

	if sub.ResultHandler != nil {
		res, handlerErr := safeExecuteResult(sub.ResultHandler, cc)
		if handlerErr != nil {
			return &Result{Text: h.friendlyCommandError(cc.L, parsed.Resource, handlerErr)}, nil
		}
		if res == nil {
			res = &Result{}
		}
		return res, nil
	}

	text, handlerErr := safeExecute(sub.Handler, cc)
	if handlerErr != nil {
		return &Result{Text: h.friendlyCommandError(cc.L, parsed.Resource, handlerErr)}, nil
	}
	return &Result{Text: text}, nil
}

// friendlyCommandError converts a service/handler error into user-facing text.
// Clean domain errors (e.g. `schedule "x" not found`, `model "x" is ambiguous`)
// are surfaced sentence-cased, with a discovery pointer appended for not-found
// cases. Errors that look like infra/transport leaks (raw Go wrap chains,
// "dial tcp", IPs, deadlines, SQL/driver text) are replaced with a generic
// retry line so internals never reach chat.
func (h *Handler) friendlyCommandError(t *i18n.Localizer, resource string, err error) string {
	if err == nil {
		return ""
	}
	var invalidReasoning *settings.InvalidReasoningEffortError
	if errors.As(err, &invalidReasoning) {
		return t.T("cmd.reasoning.unknownLevel", map[string]any{
			"level":  fmt.Sprintf("%q", invalidReasoning.Effort),
			"levels": strings.Join(reasoningChoicesFor(invalidReasoning.Options), ", "),
		})
	}
	if errors.Is(err, settings.ErrReasoningOptionsUnavailable) {
		return t.T("cmd.reasoning.unavailable")
	}
	msg := strings.TrimSpace(err.Error())
	res := strings.TrimSpace(resource)
	if msg != "" && !looksLikeInternalError(msg) {
		out := capitalizeFirst(msg)
		if !endsWithTerminalPunct(out) {
			out += "."
		}
		if res != "" && strings.Contains(strings.ToLower(msg), "not found") {
			out += t.T("cmd.error.runToSeeList", map[string]any{"command": CmdRef(res + " list")})
		}
		return out
	}
	// Sanitized path: keep the raw error in logs, show the user a clean retry line.
	if h.logger != nil {
		h.logger.Warn("command failed", slog.String("resource", res), slog.Any("error", err))
	}
	if res == "" {
		return t.T("cmd.error.genericNoResource")
	}
	return t.T("cmd.error.generic", map[string]any{"command": CmdRef(res)})
}

// normalizeLanguageShorthand rewrites the "/language <lang>" shorthand into the
// explicit "/language set <lang>" form so a bare value (zh/en/ja/auto) reaches the
// set handler's argument slice. The caller must invoke this BEFORE freezing
// CommandContext.Args from parsed.Args — otherwise the rewritten arg never makes
// it into the context the handler reads.
func normalizeLanguageShorthand(resource string, parsed *ParsedCommand) {
	if resource == "language" && parsed.Action != "" && parsed.Action != "show" && parsed.Action != "set" {
		parsed.Args = append([]string{parsed.Action}, parsed.Args...)
		parsed.Action = "set"
	}
}

// looksLikeInternalError reports whether an error message carries infra/transport
// internals that must not reach chat (Go wrap chains, network/SQL/TLS details).
// It keys on content markers only — a length cap was removed because legitimate
// domain messages (e.g. an ambiguous-model list of provider-qualified IDs) can
// be long, and capping by length wrongly replaced them with a dead retry line.
//
// Markers are conservative. "sql:" (with colon) catches database/sql / pq
// wrap chains without flagging model names that happen to contain "sql"
// (e.g. "sqlcoder"). "failed to " is the canonical Go wrap idiom; legitimate
// domain messages can also begin with it ("failed to find …"), so the
// false-positive test in handler_test pins the trade-off and the visible
// fallback ("please try again") is still recoverable.
func looksLikeInternalError(msg string) bool {
	lower := strings.ToLower(msg)
	markers := []string{
		"failed to ", "dial tcp", "connection refused", "context deadline",
		"i/o timeout", "no such host", "pq:", "sql:", "x509",
		"panic:", "goroutine", "invalid memory", "nil pointer",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// capitalizeFirst upper-cases the first rune of s, leaving the rest untouched.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// endsWithTerminalPunct reports whether s already ends in sentence-final
// punctuation (ASCII or CJK). friendlyCommandError uses it so it never tacks an
// ASCII "." onto an already-terminated string — e.g. a zh message ending in the
// ideographic full stop "。" would otherwise become "…。.".
func endsWithTerminalPunct(s string) bool {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return false
	}
	switch r[len(r)-1] {
	case '.', '!', '?', '。', '！', '？', '…':
		return true
	}
	return false
}

// chatACLAllows reports whether the bot's chat ACL permits this caller. With no
// ACL evaluator wired it allows, mirroring the inbound chat gate (a no-op when
// ACL is unconfigured).
func (h *Handler) chatACLAllows(cc CommandContext) (bool, error) {
	if h.aclEvaluator == nil {
		return true, nil
	}
	return h.aclEvaluator.Evaluate(cc.Ctx, acl.EvaluateRequest{
		BotID:             cc.BotID,
		ChannelIdentityID: cc.ChannelIdentityID,
		ChannelType:       cc.ChannelType,
		SourceScope: acl.SourceScope{
			ConversationType: cc.ConversationType,
			ConversationID:   cc.ConversationID,
			ThreadID:         cc.ThreadID,
		},
	})
}

// CommandAccess reports whether the caller may operate a command on the bot,
// applying the same policy as Execute's gate: /link is always allowed; owners and
// managers are always allowed; everyone else must pass the chat ACL. The channel
// processor uses it to gate the route-aware mode commands (/new, /stop, /status)
// that do not flow through Execute.
func (h *Handler) CommandAccess(ctx context.Context, input ExecuteInput) (bool, error) {
	if parsed, ok := parsedCommandFromInput(input); ok {
		if canonicalResource(parsed.Resource) == "link" {
			return true, nil
		}
	}
	role := ""
	roleIdentityID := strings.TrimSpace(input.ChannelIdentityID)
	if h.roleResolver != nil && roleIdentityID != "" {
		r, err := h.roleResolver.GetMemberRole(ctx, input.BotID, roleIdentityID)
		if err != nil {
			if h.logger != nil {
				h.logger.Warn("failed to resolve member role in CommandAccess",
					slog.String("bot_id", input.BotID),
					slog.String("channel_identity_id", roleIdentityID),
					slog.Any("error", err),
				)
			}
		} else {
			role = r
		}
	}
	if role == "owner" || role == "manager" {
		return true, nil
	}
	if h.aclEvaluator == nil {
		return true, nil
	}
	return h.aclEvaluator.Evaluate(ctx, acl.EvaluateRequest{
		BotID:             input.BotID,
		ChannelIdentityID: strings.TrimSpace(input.ChannelIdentityID),
		ChannelType:       strings.TrimSpace(input.ChannelType),
		SourceScope: acl.SourceScope{
			ConversationType: strings.TrimSpace(input.ConversationType),
			ConversationID:   strings.TrimSpace(input.ConversationID),
			ThreadID:         strings.TrimSpace(input.ThreadID),
		},
	})
}

func parsedCommandFromInput(input ExecuteInput) (ParsedCommand, bool) {
	if input.Invocation != nil {
		return input.Invocation.Parsed, true
	}
	cmdText := ExtractCommandText(input.Text)
	if cmdText == "" {
		return ParsedCommand{}, false
	}
	parsed, err := Parse(cmdText)
	return parsed, err == nil
}

// safeExecute runs a sub-command handler and recovers from panics.
func safeExecute(fn func(CommandContext) (string, error), cc CommandContext) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return fn(cc)
}

// safeExecuteResult runs a structured sub-command handler and recovers from panics.
func safeExecuteResult(fn func(CommandContext) (*Result, error), cc CommandContext) (result *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return fn(cc)
}
