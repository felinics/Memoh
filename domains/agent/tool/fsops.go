package tool

import (
	"fmt"
	"strings"
	"unicode"
)

func applyEdit(raw, filePath, oldText, newText string) (string, error) {
	bom, content := stripBOM(raw)
	originalEnding := detectLineEnding(content)
	normalizedContent := normalizeToLF(content)
	normalizedOld := normalizeToLF(oldText)
	normalizedNew := normalizeToLF(newText)
	match := fuzzyFindText(normalizedContent, normalizedOld)
	if !match.Found {
		return "", fmt.Errorf(
			"could not find the exact text in %s. the old text must match exactly including all whitespace and newlines",
			filePath,
		)
	}
	fuzzyContent := normalizeForFuzzyMatch(normalizedContent)
	fuzzyOld := normalizeForFuzzyMatch(normalizedOld)
	occurrences := strings.Count(fuzzyContent, fuzzyOld)
	if occurrences > 1 {
		return "", fmt.Errorf(
			"found %d occurrences of the text in %s. the text must be unique. please provide more context to make it unique",
			occurrences, filePath,
		)
	}
	baseContent := match.ContentForReplacement
	updated := baseContent[:match.Index] + normalizedNew + baseContent[match.Index+match.MatchLength:]
	if baseContent == updated {
		return "", fmt.Errorf(
			"no changes made to %s. the replacement produced identical content. this might indicate an issue with special characters or the text not existing as expected",
			filePath,
		)
	}
	return bom + restoreLineEndings(updated, originalEnding), nil
}

type fuzzyMatchResult struct {
	Found                 bool
	Index                 int
	MatchLength           int
	ContentForReplacement string
}

func detectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")
	if lfIdx == -1 {
		return "\n"
	}
	if crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

func stripBOM(content string) (string, string) {
	const bom = "\uFEFF"
	if strings.HasPrefix(content, bom) {
		return bom, content[len(bom):]
	}
	return "", content
}

func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	trimmed := strings.Join(lines, "\n")
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u2018', '\u2019', '\u201A', '\u201B':
			return '\''
		case '\u201C', '\u201D', '\u201E', '\u201F':
			return '"'
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			return '-'
		case '\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000':
			return ' '
		default:
			return r
		}
	}, trimmed)
}

func fuzzyFindText(content, oldText string) fuzzyMatchResult {
	exactIndex := strings.Index(content, oldText)
	if exactIndex != -1 {
		return fuzzyMatchResult{Found: true, Index: exactIndex, MatchLength: len(oldText), ContentForReplacement: content}
	}
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOld := normalizeForFuzzyMatch(oldText)
	fuzzyIndex := strings.Index(fuzzyContent, fuzzyOld)
	if fuzzyIndex == -1 {
		return fuzzyMatchResult{Found: false, Index: -1, ContentForReplacement: content}
	}
	return fuzzyMatchResult{Found: true, Index: fuzzyIndex, MatchLength: len(fuzzyOld), ContentForReplacement: fuzzyContent}
}
