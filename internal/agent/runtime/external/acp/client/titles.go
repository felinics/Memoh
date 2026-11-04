package client

import "strings"

// Agents describe tool calls in human-facing titles; these two heuristics
// map that wording onto canonical tools. They are the only place agent
// wording may influence tool mapping.

var writeTitleKeywords = []string{"write", "create", "new file"}

// genericExecTitles are execute-kind titles that name the shell itself rather
// than the command ("Shell", "Run command", ...).
var genericExecTitles = []string{
	"shell", "shell command", "command", "run", "run command",
	"execute", "exec", "bash", "terminal", "terminal command",
}

// titleIndicatesWrite reports whether an edit-kind tool call's title marks it
// as a whole-file write rather than a diff edit.
func titleIndicatesWrite(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return false
	}
	for _, keyword := range writeTitleKeywords {
		if strings.Contains(title, keyword) {
			return true
		}
	}
	return false
}

// commandFromTitle extracts a usable command from an execute-kind tool call's
// title: generic shell labels yield "", a generic "label: command" title
// yields the command suffix, and anything else is returned unchanged.
func commandFromTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	label, command, hasLabel := strings.Cut(title, ":")
	for _, generic := range genericExecTitles {
		if strings.EqualFold(title, generic) {
			return ""
		}
		if hasLabel && strings.EqualFold(strings.TrimSpace(label), generic) {
			return strings.TrimSpace(command)
		}
	}
	return title
}
