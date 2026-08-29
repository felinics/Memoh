package command

import "github.com/felinics/memoh/internal/commandsyntax"

// ParsedCommand remains an alias so command handlers and callers can migrate to
// the shared syntax package without duplicating the parser implementation.
type (
	ParsedCommand   = commandsyntax.ParsedCommand
	Invocation      = commandsyntax.Invocation
	InvocationInput = commandsyntax.InvocationInput
)

var (
	ErrNotCommand         = commandsyntax.ErrNotCommand
	ErrCommandForOtherBot = commandsyntax.ErrCommandForOtherBot
)

func Parse(text string) (ParsedCommand, error) {
	return commandsyntax.Parse(text)
}

func ParseInvocation(input InvocationInput) (Invocation, error) {
	return commandsyntax.ParseInvocation(input)
}

func ExtractCommandText(text string) string {
	return commandsyntax.ExtractCommandText(text)
}

func tokenize(input string) []string {
	return commandsyntax.Tokenize(input)
}
