package profile

import (
	"errors"
	"fmt"
	"strings"
)

const (
	genericACPCommandFieldID   = "command"
	genericACPArgumentsFieldID = "arguments"
	maxGenericACPArguments     = 128
	maxGenericACPArgumentBytes = 16 * 1024
)

// ResolveLaunch returns the executable and argv declared by an ACP profile.
// Profiles may pin a command or source the command and newline-delimited argv
// from managed bot metadata.
func ResolveLaunch(profile Profile, setup AgentSetup) (string, []string, error) {
	policy := profile.Launch
	if policy.ManagedCommandField == "" {
		command := strings.TrimSpace(policy.Command)
		if command == "" {
			return "", nil, fmt.Errorf("ACP command is required for %s", profile.DisplayName)
		}
		return command, nil, nil
	}

	commandField := NormalizeAgentID(policy.ManagedCommandField)
	command := strings.TrimSpace(setup.Managed[commandField])
	if command == "" {
		return "", nil, fmt.Errorf("%s required", commandField)
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return "", nil, fmt.Errorf("%s contains an invalid control character", commandField)
	}

	argumentsField := NormalizeAgentID(policy.ManagedArgumentsField)
	arguments, err := parseGenericACPArguments(setup.Managed[argumentsField])
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
			return nil, errors.New("arguments contains an invalid control character")
		}
		if len(argument) > maxGenericACPArgumentBytes {
			return nil, fmt.Errorf("argument exceeds %d bytes", maxGenericACPArgumentBytes)
		}
		arguments = append(arguments, argument)
		if len(arguments) > maxGenericACPArguments {
			return nil, fmt.Errorf("arguments exceeds %d entries", maxGenericACPArguments)
		}
	}
	return arguments, nil
}
