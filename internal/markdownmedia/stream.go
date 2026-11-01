package markdownmedia

import "strings"

// Bound how long an unfinished construct may hold streamed text. Longer link
// labels or destinations are prose, not references the model is writing.
const maxHeldConstruct = 2048

// StreamHold reports how much of streamed Markdown can be forwarded now.
// From the returned offset on, the text is either an unfinished construct
// that may still become a media reference, or (media == true) starts with a
// complete one. Ordinary links, brackets, exclamation marks and code never
// hold, so most replies keep streaming. resume is a line start at or before
// offset, outside code fences, where a later scan of the same text may begin
// instead of rescanning from the start.
func StreamHold(text string) (offset int, media bool, resume int) {
	bang := -1
	lineStart := true
	line := 0
	for i := 0; i < len(text); i++ {
		if lineStart {
			lineStart = false
			line = i
			if end, ok := skipFence(text, i); ok {
				if end < 0 {
					// Code inside an unfinished fence never publishes media.
					return len(text), false, line
				}
				i = end
				lineStart = true
				continue
			}
		}
		switch c := text[i]; c {
		case '\n':
			lineStart = true
		case '\\':
			i++
		case '`':
			// An unclosed run is literal text, as CommonMark reads it, so the
			// references after it still hold.
			if end, ok := skipCodeSpan(text, i); ok {
				i = end
			} else {
				for i+1 < len(text) && text[i+1] == '`' {
					i++
				}
			}
		case '!':
			if i == len(text)-1 {
				return i, false, line
			}
			bang = i
		case '[':
			start := i
			if bang >= 0 && bang == i-1 {
				start = bang
			}
			switch classifyCandidate(text, start, i) {
			case candidatePending:
				return start, false, line
			case candidateMedia:
				return start, true, line
			}
		}
	}
	return len(text), false, line
}

type candidateState int

const (
	candidateDead candidateState = iota
	candidatePending
	candidateMedia
)

// classifyCandidate decides a construct opening at text[open] == '['. start
// includes a preceding '!' for images.
func classifyCandidate(text string, start, open int) candidateState {
	depth := 0
	closeLabel := -1
	for j := open; j < len(text) && closeLabel < 0; j++ {
		if j-open > maxHeldConstruct {
			return candidateDead
		}
		switch text[j] {
		case '\\':
			j++
		case '`':
			if end, ok := skipCodeSpan(text, j); ok {
				j = end
			}
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				closeLabel = j
			}
		case '\n':
			if strings.HasPrefix(text[j+1:], "\n") {
				return candidateDead
			}
		}
	}
	if closeLabel < 0 || closeLabel+1 >= len(text) {
		return candidatePending
	}
	if text[closeLabel+1] != '(' {
		// A reference image or full reference link may resolve against a
		// definition that arrives later, so it holds until the message is
		// complete. A bare [label] is far more often prose, so it streams.
		if text[closeLabel+1] == '[' {
			if strings.IndexByte(text[closeLabel+2:], ']') < 0 {
				if len(text)-closeLabel > maxHeldConstruct {
					return candidateDead
				}
				return candidatePending
			}
			return candidateMedia
		}
		if start < open {
			return candidateMedia
		}
		return candidateDead
	}
	parens := 0
	angle := false
	for m := closeLabel + 1; m < len(text); m++ {
		if m-closeLabel > maxHeldConstruct {
			return candidateDead
		}
		c := text[m]
		if angle {
			switch c {
			case '\\':
				m++
			case '>':
				angle = false
			case '\n':
				return candidateDead
			}
			continue
		}
		switch c {
		case '\\':
			m++
		case '<':
			angle = strings.TrimSpace(text[closeLabel+2:m]) == ""
		case '(':
			parens++
		case ')':
			parens--
			if parens == 0 {
				if len(Parse(text[start:m+1])) > 0 {
					return candidateMedia
				}
				return candidateDead
			}
		case '\n':
			if strings.HasPrefix(text[m+1:], "\n") {
				return candidateDead
			}
		}
	}
	return candidatePending
}

// skipFence recognizes a fenced code block opening at a line start and
// returns the index of the newline ending its closing fence, or -1 while the
// fence is still open.
func skipFence(text string, lineStart int) (int, bool) {
	i := lineStart
	for i < len(text) && i-lineStart < 3 && text[i] == ' ' {
		i++
	}
	marker := fenceMarker(text[i:])
	if marker == "" {
		return 0, false
	}
	pos := strings.IndexByte(text[i:], '\n')
	if pos < 0 {
		return -1, true
	}
	pos += i
	for pos+1 < len(text) {
		next := pos + 1
		end := strings.IndexByte(text[next:], '\n')
		line := text[next:]
		if end >= 0 {
			line = text[next : next+end]
		}
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) < 4 && strings.HasPrefix(trimmed, marker) && strings.Trim(trimmed, marker[:1]+" \t") == "" {
			if end < 0 {
				return len(text) - 1, true
			}
			return next + end, true
		}
		if end < 0 {
			return -1, true
		}
		pos = next + end
	}
	return -1, true
}

func fenceMarker(line string) string {
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(line) && line[n] == c {
			n++
		}
		if n >= 3 {
			return line[:n]
		}
	}
	return ""
}

// skipCodeSpan returns the index of the last backtick closing the span that
// opens at text[i].
func skipCodeSpan(text string, i int) (int, bool) {
	n := 0
	for i+n < len(text) && text[i+n] == '`' {
		n++
	}
	run := text[i : i+n]
	for j := i + n; j < len(text); {
		k := strings.Index(text[j:], run)
		if k < 0 {
			return 0, false
		}
		k += j
		end := k + n
		if end < len(text) && text[end] == '`' {
			for end < len(text) && text[end] == '`' {
				end++
			}
			j = end
			continue
		}
		// A closing run that is the last text may still grow.
		if end == len(text) {
			return 0, false
		}
		return end - 1, true
	}
	return 0, false
}
