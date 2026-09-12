package tools

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// editDiffContextLines is the number of unchanged lines kept around each
// changed line in the edit tool's diff, matching the git default.
const editDiffContextLines = 3

// editContextDiff renders a unified diff between the file content before and
// after an edit tool call, so the UI can show the change where it happened —
// unchanged context lines stay plain, only actually removed/added lines get
// -/+ markers. Both sides are normalized to LF and stripped of any BOM first:
// the diff is display-only, and applyEdit already preserves the file's real
// line endings and BOM for the write, so neither should show up as fake
// changes here.
//
// Returns "" when there is nothing useful to show: identical content, or a
// file too large for a line-based diff to be worth computing and persisting
// (the chat UI then falls back to the plain old/new block view).
func editContextDiff(filePath, before, after string) (string, error) {
	if len(before) > largeFileThreshold || len(after) > largeFileThreshold {
		return "", nil
	}
	_, before = stripBOM(before)
	_, after = stripBOM(after)
	before = normalizeToLF(before)
	after = normalizeToLF(after)
	if before == after {
		return "", nil
	}
	displayPath := strings.TrimPrefix(filePath, "/")
	// SplitLines("") yields one empty line, which would show up as a phantom
	// empty context row for whole-file creations/deletions; an empty side
	// must be a truly empty slice. SplitLines also appends a phantom empty
	// line when the text ends with a newline — trim it on both sides so a
	// trailing newline is treated as a line terminator, not an extra line.
	var aLines, bLines []string
	if before != "" {
		aLines = trimPhantomLine(difflib.SplitLines(before))
	}
	if after != "" {
		bLines = trimPhantomLine(difflib.SplitLines(after))
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        aLines,
		B:        bLines,
		FromFile: "a/" + displayPath,
		ToFile:   "b/" + displayPath,
		Context:  editDiffContextLines,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(diff, "\n"), nil
}

func trimPhantomLine(lines []string) []string {
	if n := len(lines); n > 0 && (lines[n-1] == "" || lines[n-1] == "\n") {
		return lines[:n-1]
	}
	return lines
}
