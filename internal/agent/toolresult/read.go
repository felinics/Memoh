package toolresult

import "strings"

// Read is the result of a text read. TotalLines counts the pieces of the
// whole file split on newlines, whatever window Content covers.
type Read struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
}

func (r Read) Map() map[string]any { return toMap(r) }

// CountLines is the TotalLines of text.
func CountLines(text string) int {
	return strings.Count(text, "\n") + 1
}

// Write is the result of a completed write.
type Write struct {
	OK bool `json:"ok"`
}

func (r Write) Map() map[string]any { return toMap(r) }
