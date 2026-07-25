package profile

import "strings"

// ToolQuirks captures per-agent presentation heuristics: how an agent's
// human-facing tool-call titles map onto canonical tool identities. This is
// the ONLY place agent wording may influence tool mapping - when an agent
// update changes its titles, adjust that agent's profile here instead of
// patching the shared mapping code in runtime/acp/client.
//
// The zero value behaves like DefaultToolQuirks, so an agent without explicit
// quirks gets the shared defaults.
type ToolQuirks struct {
	// WriteTitleKeywords reclassify an edit-kind tool call as a whole-file
	// write when its title contains one of them (case-insensitive).
	WriteTitleKeywords []string
	// GenericExecTitles are execute-kind titles that name the shell itself
	// rather than the command ("Shell", "Run command", ...). Such a title is
	// useless as a command fallback and must be ignored.
	GenericExecTitles []string
}

var defaultWriteTitleKeywords = []string{"write", "create", "new file"}

var defaultGenericExecTitles = []string{
	"shell", "shell command", "command", "run", "run command",
	"execute", "exec", "bash", "terminal", "terminal command",
}

// DefaultToolQuirks returns the shared title heuristics that match every
// agent observed so far (Claude Code, Codex).
func DefaultToolQuirks() ToolQuirks {
	return ToolQuirks{
		WriteTitleKeywords: append([]string(nil), defaultWriteTitleKeywords...),
		GenericExecTitles:  append([]string(nil), defaultGenericExecTitles...),
	}
}

// QuirksFor resolves the tool quirks for an agent ID, falling back to the
// defaults for unknown agents.
func QuirksFor(agentID string) ToolQuirks {
	profile, ok := Lookup(agentID)
	if !ok {
		return DefaultToolQuirks()
	}
	return profile.Quirks()
}

// Quirks returns the profile's tool quirks; fields the profile leaves unset
// fall back to the defaults.
func (p Profile) Quirks() ToolQuirks {
	quirks := DefaultToolQuirks()
	if p.ToolQuirks == nil {
		return quirks
	}
	if len(p.ToolQuirks.WriteTitleKeywords) > 0 {
		quirks.WriteTitleKeywords = append([]string(nil), p.ToolQuirks.WriteTitleKeywords...)
	}
	if len(p.ToolQuirks.GenericExecTitles) > 0 {
		quirks.GenericExecTitles = append([]string(nil), p.ToolQuirks.GenericExecTitles...)
	}
	return quirks
}

// TitleIndicatesWrite reports whether an edit-kind tool call's title marks it
// as a whole-file write rather than a diff edit.
func (q ToolQuirks) TitleIndicatesWrite(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return false
	}
	for _, keyword := range q.writeTitleKeywords() {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(title, keyword) {
			return true
		}
	}
	return false
}

// CommandFromTitle extracts a usable command from an execute-kind tool call's
// title: generic shell labels yield "", anything else is the command text.
func (q ToolQuirks) CommandFromTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	lowered := strings.ToLower(title)
	for _, generic := range q.genericExecTitles() {
		if lowered == strings.ToLower(strings.TrimSpace(generic)) {
			return ""
		}
	}
	return title
}

func (q ToolQuirks) writeTitleKeywords() []string {
	if len(q.WriteTitleKeywords) > 0 {
		return q.WriteTitleKeywords
	}
	return defaultWriteTitleKeywords
}

func (q ToolQuirks) genericExecTitles() []string {
	if len(q.GenericExecTitles) > 0 {
		return q.GenericExecTitles
	}
	return defaultGenericExecTitles
}
