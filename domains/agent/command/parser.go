package command

import "github.com/memohai/memoh/domains/agent/command/syntax"

// ParsedCommand remains an alias so command handlers and callers can migrate to
// the shared syntax package without duplicating the parser implementation.
type (
	ParsedCommand   = syntax.ParsedCommand
	Invocation      = syntax.Invocation
	InvocationInput = syntax.InvocationInput
)

var (
	ErrNotCommand         = syntax.ErrNotCommand
	ErrCommandForOtherBot = syntax.ErrCommandForOtherBot
)

func Parse(text string) (ParsedCommand, error) {
	return syntax.Parse(text)
}

func ParseInvocation(input InvocationInput) (Invocation, error) {
	return syntax.ParseInvocation(input)
}

func ExtractCommandText(text string) string {
	return syntax.ExtractCommandText(text)
}

func tokenize(input string) []string {
	return syntax.Tokenize(input)
}
