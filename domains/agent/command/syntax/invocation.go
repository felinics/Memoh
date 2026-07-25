package syntax

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotCommand         = errors.New("text is not a slash command")
	ErrCommandForOtherBot = errors.New("slash command targets another bot")
)

// Invocation is the canonical interpretation of one slash-command message.
// Channel code must pass this object through classification, authorization, and
// execution instead of asking each stage to parse the original message again.
type Invocation struct {
	RawText     string
	CommandText string
	Selector    string
	Rest        string
	Parsed      ParsedCommand
	Directed    bool
	Addressed   bool
	Addressing  []string
}

// InvocationInput supplies the channel facts needed to distinguish command
// arguments from syntax that addresses the current bot. BotAliases must contain
// only identities for the current bot; mentions of other users remain arguments.
type InvocationInput struct {
	Text       string
	BotAliases []string
	Directed   bool
}

type invocationToken struct {
	Start  int
	End    int
	Value  string
	Quoted bool
}

type textRange struct {
	Start int
	End   int
}

// ParseInvocation normalizes supported channel addressing forms and parses the
// resulting command exactly once. It recognizes "@bot /command",
// "/command@bot", mentions attached to unquoted arguments, and standalone
// mentions of the current bot. Quoted mentions are data and are never removed.
func ParseInvocation(input InvocationInput) (Invocation, error) {
	raw := strings.TrimSpace(input.Text)
	if raw == "" {
		return Invocation{}, ErrNotCommand
	}

	tokens := lexInvocationTokens(raw)
	if len(tokens) == 0 {
		return Invocation{}, ErrNotCommand
	}

	commandIndex := -1
	addressed := false
	addressing := make([]string, 0, 2)
	for i, token := range tokens {
		if strings.HasPrefix(token.Value, "/") {
			commandIndex = i
			break
		}
		if token.Quoted || !isMentionToken(token.Value) {
			return Invocation{}, ErrNotCommand
		}
		if aliasMatchesInvocation(token.Value, input.BotAliases) {
			addressed = true
			addressing = append(addressing, token.Value)
		}
	}
	if commandIndex < 0 {
		return Invocation{}, ErrNotCommand
	}
	if commandIndex > 0 && !addressed {
		return Invocation{}, ErrCommandForOtherBot
	}

	commandText := strings.TrimSpace(raw[tokens[commandIndex].Start:])
	commandTokens := lexInvocationTokens(commandText)
	if len(commandTokens) == 0 || !strings.HasPrefix(commandTokens[0].Value, "/") {
		return Invocation{}, ErrNotCommand
	}

	removals := make([]textRange, 0, 3)
	head := commandTokens[0]
	selector := strings.TrimPrefix(head.Value, "/")
	if before, after, ok := strings.Cut(selector, "@"); ok {
		if before == "" || !aliasMatchesInvocation(after, input.BotAliases) {
			return Invocation{}, ErrCommandForOtherBot
		}
		at := strings.IndexByte(head.Value, '@')
		removals = append(removals, textRange{Start: head.Start + at, End: head.End})
		selector = before
		addressed = true
		addressing = append(addressing, "@"+after)
	}
	if selector == "" {
		return Invocation{}, ErrNotCommand
	}

	for _, token := range commandTokens[1:] {
		if token.Quoted {
			continue
		}
		if isMentionToken(token.Value) && aliasMatchesInvocation(token.Value, input.BotAliases) {
			removals = append(removals, mentionRemovalRange(commandText, token))
			addressed = true
			addressing = append(addressing, token.Value)
			continue
		}
		// Whitespace separates ordinary arguments, but an exact @current_bot suffix
		// is an IM addressing token even when the user glues it to the preceding
		// value. Other @suffixes remain untouched, and quoting always wins above.
		if at := strings.LastIndexByte(token.Value, '@'); at > 0 && aliasMatchesInvocation(token.Value[at+1:], input.BotAliases) {
			removals = append(removals, textRange{Start: token.Start + at, End: token.End})
			addressed = true
			addressing = append(addressing, "@"+token.Value[at+1:])
		}
	}

	commandText = strings.TrimSpace(removeTextRanges(commandText, removals))
	parsed, err := Parse(commandText)
	if err != nil {
		return Invocation{}, err
	}
	normalizedTokens := lexInvocationTokens(commandText)
	rest := ""
	if len(normalizedTokens) > 0 && normalizedTokens[0].End < len(commandText) {
		rest = strings.TrimSpace(commandText[normalizedTokens[0].End:])
	}

	return Invocation{
		RawText:     raw,
		CommandText: commandText,
		Selector:    selector,
		Rest:        rest,
		Parsed:      parsed,
		Directed:    input.Directed || addressed,
		Addressed:   addressed,
		Addressing:  addressing,
	}, nil
}

func mentionRemovalRange(text string, token invocationToken) textRange {
	item := textRange{Start: token.Start, End: token.End}
	for item.End < len(text) {
		r, width := utf8.DecodeRuneInString(text[item.End:])
		if !unicode.IsSpace(r) {
			break
		}
		item.End += width
	}
	if item.End == len(text) {
		for item.Start > 0 {
			r, width := utf8.DecodeLastRuneInString(text[:item.Start])
			if !unicode.IsSpace(r) {
				break
			}
			item.Start -= width
		}
	}
	return item
}

func lexInvocationTokens(input string) []invocationToken {
	tokens := make([]invocationToken, 0, 4)
	start := -1
	quoted := false
	var quote rune
	var value strings.Builder
	flush := func(end int) {
		if start < 0 {
			return
		}
		tokens = append(tokens, invocationToken{Start: start, End: end, Value: value.String(), Quoted: quoted})
		start = -1
		quoted = false
		value.Reset()
	}

	for pos := 0; pos < len(input); {
		r, width := utf8.DecodeRuneInString(input[pos:])
		if quote != 0 {
			if r == quote {
				quote = 0
				pos += width
				continue
			}
			value.WriteRune(r)
			pos += width
			continue
		}
		if unicode.IsSpace(r) {
			flush(pos)
			pos += width
			continue
		}
		if start < 0 {
			start = pos
		}
		if r == '\'' || r == '"' {
			quote = r
			quoted = true
			pos += width
			continue
		}
		value.WriteRune(r)
		pos += width
	}
	flush(len(input))
	return tokens
}

func removeTextRanges(text string, ranges []textRange) string {
	if len(ranges) == 0 {
		return text
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	var out strings.Builder
	cursor := 0
	for _, item := range ranges {
		if item.Start < cursor || item.Start < 0 || item.End > len(text) || item.Start >= item.End {
			continue
		}
		out.WriteString(text[cursor:item.Start])
		cursor = item.End
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func isMentionToken(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<@") {
		end := strings.IndexByte(value, '>')
		return end > 2 && mentionDelimiterSuffix(value[end+1:])
	}
	if !strings.HasPrefix(value, "@") {
		return false
	}
	identity := strings.TrimPrefix(value, "@")
	return strings.TrimRightFunc(identity, isMentionDelimiter) != ""
}

func aliasMatchesInvocation(value string, aliases []string) bool {
	candidates := normalizeInvocationMentionCandidates(value)
	if len(candidates) == 0 {
		return false
	}
	for _, alias := range aliases {
		normalized := normalizeInvocationAlias(alias)
		if normalized == "" {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(candidate, normalized) {
				return true
			}
		}
	}
	return false
}

func normalizeInvocationMentionCandidates(value string) []string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<@") {
		end := strings.IndexByte(value, '>')
		if end > 2 && mentionDelimiterSuffix(value[end+1:]) {
			identity := strings.TrimPrefix(value[2:end], "!")
			if identity = strings.TrimSpace(identity); identity != "" {
				return []string{identity}
			}
		}
	}

	identity := normalizeInvocationAlias(value)
	if identity == "" {
		return nil
	}
	// Some channel identities may legitimately end in punctuation. Preserve the
	// exact candidate first, then offer a delimiter-stripped fallback for natural
	// forms such as "@bot, /help" without weakening exact alias matching.
	candidates := []string{identity}
	if strings.HasPrefix(value, "@") {
		withoutDelimiter := strings.TrimRightFunc(identity, isMentionDelimiter)
		if withoutDelimiter != "" && withoutDelimiter != identity {
			candidates = append(candidates, withoutDelimiter)
		}
	}
	return candidates
}

func normalizeInvocationAlias(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<@") && strings.HasSuffix(value, ">") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "<@"), ">")
		value = strings.TrimPrefix(value, "!")
		return strings.TrimSpace(value)
	}
	return strings.Trim(strings.TrimSpace(value), "@")
}

func mentionDelimiterSuffix(value string) bool {
	for _, r := range value {
		if !isMentionDelimiter(r) {
			return false
		}
	}
	return true
}

func isMentionDelimiter(r rune) bool {
	switch r {
	case ',', '.', ':', ';', '!', '?', '，', '。', '：', '；', '！', '？':
		return true
	default:
		return false
	}
}
