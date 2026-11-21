package profile

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/errs"
)

const (
	commandFieldID             = "command"
	argumentsFieldID           = "arguments"
	maxGenericACPArguments     = 128
	maxGenericACPArgumentBytes = 16 * 1024
)

// ResolveLaunch returns the executable and argv an agent instance configured:
// its command field and its newline-delimited arguments field.
func ResolveLaunch(setup AgentSetup) (string, []string, error) {
	command := strings.TrimSpace(setup.Managed[commandFieldID])
	if command == "" {
		return "", nil, errs.New(commandFieldID + " required")
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return "", nil, errs.New(commandFieldID + " contains an invalid control character")
	}
	arguments, err := parseGenericACPArguments(setup.Managed[argumentsFieldID])
	if err != nil {
		return "", nil, err
	}
	return command, arguments, nil
}

func parseGenericACPArguments(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	arguments := make([]string, 0, len(lines))
	for _, line := range lines {
		argument := strings.TrimSpace(line)
		if argument == "" {
			continue
		}
		if strings.ContainsAny(argument, "\x00\r\n") {
			return nil, errs.New("arguments contains an invalid control character")
		}
		if len(argument) > maxGenericACPArgumentBytes {
			return nil, errs.New(fmt.Sprintf("argument exceeds %d bytes", maxGenericACPArgumentBytes))
		}
		arguments = append(arguments, argument)
		if len(arguments) > maxGenericACPArguments {
			return nil, errs.New(fmt.Sprintf("arguments exceeds %d entries", maxGenericACPArguments))
		}
	}
	return arguments, nil
}
