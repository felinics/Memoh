package slash

import (
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/commandsyntax"
)

type Surface string

const (
	SurfaceChannel Surface = "channel"
	SurfaceWebWS   Surface = "web_ws"
)

type DecisionKind string

const (
	DecisionNormalChat         DecisionKind = "normal_chat"
	DecisionRejectNoop         DecisionKind = "reject_noop"
	DecisionCommandAction      DecisionKind = "command_action"
	DecisionUnsupportedCommand DecisionKind = "unsupported_command"
	DecisionSkillIntent        DecisionKind = "skill_intent"
	DecisionUnknownSlash       DecisionKind = "unknown_slash"
	DecisionReject             DecisionKind = "reject"
)

type Command struct {
	Resource string
	Action   string
	Raw      string
}

type SkillIntent struct {
	Names  []string
	Prompt string
}

type Decision struct {
	Kind        DecisionKind
	Code        string
	Directed    bool
	Command     Command
	SkillIntent SkillIntent
	Invocation  *commandsyntax.Invocation
	// AgentCommand is the exact ACP agent-command selector a live-runtime
	// admission check matched for a NormalChat decision. Carried so the
	// session pool can re-validate it against the final runtime at prompt
	// time; empty for every other decision source.
	AgentCommand string
}

type ClassifyInput struct {
	Text               string
	HasAttachments     bool
	Surface            Surface
	IsGroup            bool
	Directed           bool
	SupportsMode       bool
	BotAliases         []string
	KnownCommand       func(resource string) bool
	WebActionSupported func(resource, action string) bool
}

func Classify(input ClassifyInput) Decision {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return Decision{Kind: DecisionNormalChat, Directed: input.Directed}
	}
	invocation, err := commandsyntax.ParseInvocation(commandsyntax.InvocationInput{
		Text:       text,
		BotAliases: input.BotAliases,
		Directed:   input.Directed,
	})
	if err != nil {
		if input.Surface == SurfaceChannel && errors.Is(err, commandsyntax.ErrCommandForOtherBot) {
			return Decision{Kind: DecisionRejectNoop, Directed: false}
		}
		return Decision{Kind: DecisionNormalChat, Directed: input.Directed}
	}

	effectiveDirected := invocation.Directed
	if input.Surface == SurfaceChannel && input.IsGroup && !effectiveDirected {
		return Decision{Kind: DecisionRejectNoop, Directed: false, Invocation: &invocation}
	}
	parsed := invocation.Parsed

	if parsed.Resource == "skill" && parsed.Action == "use" {
		return Decision{Kind: DecisionReject, Code: CodeInvalidSkillSlashSyntax, Directed: effectiveDirected, Invocation: &invocation}
	}

	if input.Surface == SurfaceChannel && input.SupportsMode && isModePrefix(parsed.Resource) {
		remainder := strings.TrimSpace(invocation.Rest)
		if strings.HasPrefix(remainder, "/") || isSlashLike(remainder, input.BotAliases) {
			return Decision{Kind: DecisionReject, Code: CodeUnknownSlash, Directed: effectiveDirected, Invocation: &invocation}
		}
		return Decision{Kind: DecisionNormalChat, Directed: effectiveDirected, Invocation: &invocation}
	}

	if isCommandName(invocation.Selector) && isKnown(input.KnownCommand, parsed.Resource) {
		cmd := Command{Resource: parsed.Resource, Action: parsed.Action, Raw: invocation.CommandText}
		if input.Surface == SurfaceWebWS {
			if input.WebActionSupported != nil && input.WebActionSupported(parsed.Resource, parsed.Action) {
				return Decision{Kind: DecisionCommandAction, Directed: effectiveDirected, Command: cmd, Invocation: &invocation}
			}
			return Decision{Kind: DecisionUnsupportedCommand, Code: CodeUnsupportedWebCommand, Directed: effectiveDirected, Command: cmd, Invocation: &invocation}
		}
		return Decision{Kind: DecisionCommandAction, Directed: effectiveDirected, Command: cmd, Invocation: &invocation}
	}

	if isValidSkillSelector(invocation.Selector) {
		// The attachment fail-closed rule protects skill activation only: a
		// requested-skill turn must not smuggle attachments (or unproven
		// reply/forward attachments) into the model context. Fixed commands
		// never consume attachments, so they classify above regardless — a
		// photo captioned "/status", or a button tap whose synthetic message
		// carries a reply ref the adapter can't vouch for, still executes.
		if input.HasAttachments {
			return Decision{Kind: DecisionReject, Code: CodeSlashAttachmentsUnsupported, Directed: effectiveDirected, Invocation: &invocation}
		}
		return Decision{
			Kind:       DecisionSkillIntent,
			Directed:   effectiveDirected,
			Invocation: &invocation,
			SkillIntent: SkillIntent{
				Names:  []string{invocation.Selector},
				Prompt: strings.TrimSpace(invocation.Rest),
			},
		}
	}

	// The head token is outside every control grammar: not a known command,
	// not command-shaped, not a valid skill selector (every command-shaped
	// token is also a valid selector, so reaching here means the token has
	// characters no command or skill name can carry — the "/" in a Unix path
	// or URL, punctuation, non-ASCII). That is prose that happens to start
	// with a slash ("/etc/hosts what does this line mean"): pass it to the
	// model as normal chat, mirroring the deliberate path/URL carve-out the
	// pre-classifier command handler had. Plausible-but-unregistered control
	// tokens never get here — they classify as skill intents above and fail
	// closed with requested_skill_not_found at resolve time.
	return Decision{Kind: DecisionNormalChat, Directed: effectiveDirected, Invocation: &invocation}
}

func isSlashLike(text string, aliases []string) bool {
	_, err := commandsyntax.ParseInvocation(commandsyntax.InvocationInput{Text: text, BotAliases: aliases})
	return err == nil
}

func isKnown(fn func(string) bool, resource string) bool {
	return fn != nil && fn(resource)
}

func isModePrefix(resource string) bool {
	switch resource {
	case "now", "btw", "next":
		return true
	default:
		return false
	}
}

func isCommandName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func isValidSkillSelector(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}
