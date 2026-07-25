package command

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// callbackNamespace prefixes every interactive callback_data string so it does
// not collide with the existing "approve:"/"reject:" tool-approval callbacks.
const callbackNamespace = "m~"

// telegramCallbackLimit is Telegram's hard limit on callback_data (64 bytes).
const telegramCallbackLimit = 64

// Callback kinds returned by DecodeCallback.
const (
	callbackKindListPage      = "list_page"
	callbackKindModelProvider = "model_provider"
	callbackKindModelSelect   = "model_select"
	callbackKindRange         = "range"
	callbackKindConfirmNew    = "confirm_new"
	callbackKindSkillActivate = "skill_activate"
	callbackKindDismiss       = "dismiss"
	callbackKindNoop          = "noop"
)

// ParsedCallback is the decoded form of an interactive callback_data string.
type ParsedCallback struct {
	Kind          string
	Resource      string
	Action        string
	Args          []string
	Page          int
	ProviderIndex int
	SelectID      string
	Range         string
}

// IsInteractiveCallback reports whether data is one of our interactive
// callbacks (as opposed to a tool-approval callback or unrelated data).
func IsInteractiveCallback(data string) bool {
	return strings.HasPrefix(data, callbackNamespace)
}

// IsDismiss reports whether the callback closes the interactive message.
func (p ParsedCallback) IsDismiss() bool { return p.Kind == callbackKindDismiss }

// IsSkillActivation reports whether the callback activates a skill. Unlike
// pagination/selection callbacks, activation starts a fresh chat turn: adapters
// must dispatch it as a new directed message instead of editing the tapped
// card in place.
func (p ParsedCallback) IsSkillActivation() bool { return p.Kind == callbackKindSkillActivate }

// IsNoop reports whether the callback is inert (e.g. the page indicator).
func (p ParsedCallback) IsNoop() bool { return p.Kind == callbackKindNoop }

// DismissCallback returns the callback_data that closes an interactive message.
func DismissCallback() string { return callbackNamespace + "x" }

// NoopCallback returns the callback_data for inert buttons (e.g. the page
// indicator) that should be acknowledged but otherwise ignored.
func NoopCallback() string { return callbackNamespace + "noop" }

// EncodeListCallback builds the callback_data for a list-pagination button.
// Layout: "m~lp~{resource}~{action}~{page}~{argsToken}". When the encoded args
// would push the string past Telegram's 64-byte limit, the args are stashed in
// a bounded process-local table and referenced by a short token ("#<hash>").
func EncodeListCallback(resource, action string, args []string, page int) string {
	base := fmt.Sprintf("%slp~%s~%s~%d~", callbackNamespace, resource, action, page)
	argsStr := encodeArgs(args)
	if argsStr == "" {
		return base
	}
	encoded := base + argsStr
	if len(encoded) <= telegramCallbackLimit {
		return encoded
	}
	return base + "#" + stashArgs(argsStr)
}

// encodeArgs escapes each arg individually, then joins them with spaces.
// Escaping per-arg (rather than the joined string) is what keeps a space
// *within* an arg from being mistaken for an arg boundary on decode: a real
// space becomes "+", while the join delimiter stays a literal space. The prior
// approach (join first, then escape the whole string) collapsed an arg like
// "My Server" into two tokens on decode — a row tap on a space-bearing MCP
// connection / schedule / memory / search name then re-dispatched the wrong
// target. decodeArgs reverses this exactly.
func encodeArgs(args []string) string {
	escaped := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		escaped = append(escaped, url.QueryEscape(a))
	}
	return strings.Join(escaped, " ")
}

// EncodeModelProviderCallback builds the callback_data for drilling into a
// provider's paginated model list. Layout: "m~mpl~{providerIndex}~{page}".
func EncodeModelProviderCallback(providerIndex, page int) string {
	return fmt.Sprintf("%smpl~%d~%d", callbackNamespace, providerIndex, page)
}

// EncodeModelSelectCallback builds the callback_data for selecting a model by
// its stable DB id. Layout: "m~ms~{modelDBID}". Using the stable id (not a
// positional/flat index) means a model-list change between render and tap can't
// silently resolve the tap to a different model. The id is a UUID (~36 bytes),
// so "m~ms~"+id stays well within Telegram's 64-byte callback_data limit.
func EncodeModelSelectCallback(modelDBID string) string {
	return fmt.Sprintf("%sms~%s", callbackNamespace, url.QueryEscape(strings.TrimSpace(modelDBID)))
}

// EncodeRangeCallback builds the callback_data for a time-window preset button.
// Layout: "m~rg~{resource}~{action}~{rangeKey}".
func EncodeRangeCallback(resource, action, rangeKey string) string {
	return fmt.Sprintf("%srg~%s~%s~%s", callbackNamespace, resource, action, rangeKey)
}

// EncodeSkillActivateCallback builds the callback_data for a tap-to-activate
// skill row on /skill list. Layout: "m~sk~{name}". Tapping re-dispatches the
// canonical "/{name}" skill slash, so the activation runs through the exact
// same classify→resolve→activate pipeline as a typed slash — permissions and
// runtime-usability are judged at tap time, never baked into the keyboard.
// Names too long for Telegram's 64-byte limit are stashed like list args
// ("#<hash>"); a stash miss after restart/rollover simply invalidates the
// stale button (the user re-runs /skill list).
func EncodeSkillActivateCallback(name string) string {
	base := callbackNamespace + "sk~"
	encoded := base + url.QueryEscape(strings.TrimSpace(name))
	if len(encoded) <= telegramCallbackLimit {
		return encoded
	}
	return base + "#" + stashArgs(url.QueryEscape(strings.TrimSpace(name)))
}

// EncodeConfirmNewCallback builds the callback_data for confirming a /new reset.
// Layout: "m~cn~{mode}" where mode is chat|discuss. Tapping re-dispatches
// "/new {mode} --confirm", which performs the actual session reset.
func EncodeConfirmNewCallback(mode string) string {
	return fmt.Sprintf("%scn~%s", callbackNamespace, mode)
}

// DecodeCallback parses an interactive callback_data string. The bool is false
// for data that is not one of our interactive callbacks.
func DecodeCallback(data string) (ParsedCallback, bool) {
	if !strings.HasPrefix(data, callbackNamespace) {
		return ParsedCallback{}, false
	}
	body := strings.TrimPrefix(data, callbackNamespace)
	switch {
	case body == "x":
		return ParsedCallback{Kind: callbackKindDismiss}, true
	case body == "noop":
		return ParsedCallback{Kind: callbackKindNoop}, true
	case strings.HasPrefix(body, "lp~"):
		parts := strings.SplitN(strings.TrimPrefix(body, "lp~"), "~", 4)
		if len(parts) < 3 {
			return ParsedCallback{}, false
		}
		page, err := strconv.Atoi(parts[2])
		if err != nil || page < 0 {
			return ParsedCallback{}, false
		}
		var args []string
		if len(parts) == 4 {
			args = decodeArgsToken(parts[3])
		}
		return ParsedCallback{
			Kind:     callbackKindListPage,
			Resource: parts[0],
			Action:   parts[1],
			Page:     page,
			Args:     args,
		}, true
	case strings.HasPrefix(body, "mpl~"):
		parts := strings.SplitN(strings.TrimPrefix(body, "mpl~"), "~", 2)
		if len(parts) != 2 {
			return ParsedCallback{}, false
		}
		prov, errP := strconv.Atoi(parts[0])
		page, errPg := strconv.Atoi(parts[1])
		if errP != nil || errPg != nil || prov < 0 || page < 0 {
			return ParsedCallback{}, false
		}
		return ParsedCallback{Kind: callbackKindModelProvider, ProviderIndex: prov, Page: page}, true
	case strings.HasPrefix(body, "ms~"):
		id, err := url.QueryUnescape(strings.TrimPrefix(body, "ms~"))
		if err != nil || strings.TrimSpace(id) == "" {
			return ParsedCallback{}, false
		}
		return ParsedCallback{Kind: callbackKindModelSelect, SelectID: strings.TrimSpace(id)}, true
	case strings.HasPrefix(body, "rg~"):
		parts := strings.SplitN(strings.TrimPrefix(body, "rg~"), "~", 3)
		if len(parts) != 3 || parts[2] == "" {
			return ParsedCallback{}, false
		}
		return ParsedCallback{Kind: callbackKindRange, Resource: parts[0], Action: parts[1], Range: parts[2]}, true
	case strings.HasPrefix(body, "cn~"):
		mode := strings.TrimPrefix(body, "cn~")
		if mode == "" {
			return ParsedCallback{}, false
		}
		return ParsedCallback{Kind: callbackKindConfirmNew, Action: mode}, true
	case strings.HasPrefix(body, "sk~"):
		token := strings.TrimPrefix(body, "sk~")
		if strings.HasPrefix(token, "#") {
			hash := strings.TrimPrefix(token, "#")
			argsStashMu.Lock()
			token = argsStash[hash]
			argsStashMu.Unlock()
		}
		name, err := url.QueryUnescape(token)
		if err != nil || strings.TrimSpace(name) == "" {
			// Undecodable or stash-expired button: report "not ours" so the
			// adapter acknowledges the tap and does nothing, instead of
			// dispatching a broken activation.
			return ParsedCallback{}, false
		}
		return ParsedCallback{Kind: callbackKindSkillActivate, SelectID: strings.TrimSpace(name)}, true
	}
	return ParsedCallback{}, false
}

// SyntheticCommand returns the slash command text to re-dispatch for a parsed
// callback, or "" when the callback has no command (dismiss/noop).
func (p ParsedCallback) SyntheticCommand() string {
	switch p.Kind {
	case callbackKindListPage:
		base := formatSlashCommand(p.Resource, p.Action, p.Args, true)
		if base == "" {
			return ""
		}
		return base + " --page " + strconv.Itoa(p.Page)
	case callbackKindModelProvider:
		return fmt.Sprintf("/model list --prov %d --page %d", p.ProviderIndex, p.Page)
	case callbackKindModelSelect:
		return fmt.Sprintf("/model set --id %s", p.SelectID)
	case callbackKindRange:
		return fmt.Sprintf("/%s %s --range %s", p.Resource, p.Action, p.Range)
	case callbackKindConfirmNew:
		return fmt.Sprintf("/new %s --confirm", p.Action)
	case callbackKindSkillActivate:
		name := strings.TrimSpace(p.SelectID)
		if name == "" {
			return ""
		}
		return "/" + name
	default:
		return ""
	}
}

// decodeArgsToken decodes the args segment of a callback, resolving the stashed
// token form ("#<hash>") when present. A miss returns nil (unfiltered).
//
// A miss can happen when an old paginated keyboard is tapped after the
// bounded stash (256 entries, FIFO) has rolled past the original entry.
// The downstream synthetic command then re-runs the list without the
// narrowing args, showing the user an unfiltered view rather than the
// filtered subset they originally requested.
func decodeArgsToken(token string) []string {
	if token == "" {
		return nil
	}
	if strings.HasPrefix(token, "#") {
		hash := strings.TrimPrefix(token, "#")
		argsStashMu.Lock()
		stored := argsStash[hash]
		argsStashMu.Unlock()
		return decodeArgs(stored)
	}
	return decodeArgs(token)
}

// decodeArgs reverses encodeArgs: split on the literal-space delimiter FIRST,
// then QueryUnescape each field so an escaped in-arg space ("+") is restored to
// a single arg. Splitting before unescaping is precisely what preserves arg
// boundaries — unescaping first (then Fields) would reintroduce the boundary
// loss this is built to avoid.
func decodeArgs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		dec, err := url.QueryUnescape(f)
		if err != nil {
			dec = f
		}
		if dec = strings.TrimSpace(dec); dec == "" {
			continue
		}
		out = append(out, dec)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Bounded process-local table for callback args too long to inline. This is
// ephemeral presentation state (a keyboard's lifetime), not persisted user
// preference, so it carries no backend/semantic meaning.
var (
	argsStashMu    sync.Mutex
	argsStash      = make(map[string]string)
	argsStashOrder []string
)

const argsStashLimit = 256

func stashArgs(args string) string {
	token := shortHash(args)
	argsStashMu.Lock()
	defer argsStashMu.Unlock()
	if _, ok := argsStash[token]; !ok {
		argsStash[token] = args
		argsStashOrder = append(argsStashOrder, token)
		if len(argsStashOrder) > argsStashLimit {
			oldest := argsStashOrder[0]
			argsStashOrder = argsStashOrder[1:]
			delete(argsStash, oldest)
		}
	}
	return token
}

func shortHash(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return strconv.FormatUint(h.Sum64(), 36)
}
