// Derived from @tencent-weixin/openclaw-weixin (MIT License, Copyright (c) 2026 Tencent Inc.)
// See LICENSE in this directory for the full license text.
//
// Ported from src/messaging/markdown-filter.ts (openclaw-weixin 2.4.9). The
// state machine is kept structurally identical to upstream so later upstream
// fixes can be diffed in. All markers are ASCII, so byte indexing never splits
// a multi-byte UTF-8 rune.

package weixin

import (
	"strings"
)

// filterMarkdown strips the markdown WeChat renders badly and keeps what it
// renders, as upstream does for every outbound text payload.
func filterMarkdown(text string) string {
	var f streamingMarkdownFilter
	f.sol = true
	return f.feed(text) + f.flush()
}

type inlineKind int

const (
	inlineImage inlineKind = iota
	inlineBold3
	inlineItalic
	inlineUBold3
	inlineUItalic
)

var inlineMarkers = map[inlineKind]string{
	inlineImage:   "![",
	inlineBold3:   "***",
	inlineItalic:  "*",
	inlineUBold3:  "___",
	inlineUItalic: "_",
}

type inlineState struct {
	kind inlineKind
	acc  string
}

// streamingMarkdownFilter is a character-level state machine that strips
// unsupported markdown syntax on the fly, holding back only the characters
// needed to disambiguate a pattern (e.g. a trailing `*` that might become `***`).
//
// Passed through: code fences, inline code, tables, horizontal rules, bold
// (**), and italic/bold-italic wrapping non-CJK content.
// Filtered (markers stripped, content kept): italic/bold-italic wrapping CJK
// content, H5/H6 headings. Images (![alt](url)) are removed entirely.
//
// States: sol (start of line), body, fence (inside ```), inline (inside a
// marker pair).
type streamingMarkdownFilter struct {
	buf   string
	fence bool
	sol   bool
	inl   *inlineState
}

func (f *streamingMarkdownFilter) feed(delta string) string {
	f.buf += delta
	return f.pump(false)
}

func (f *streamingMarkdownFilter) flush() string {
	return f.pump(true)
}

func (f *streamingMarkdownFilter) pump(eof bool) string {
	var out strings.Builder
	for f.buf != "" {
		sLen, sSol, sFence, sInl := len(f.buf), f.sol, f.fence, f.inl

		switch {
		case f.fence:
			out.WriteString(f.pumpFence(eof))
		case f.inl != nil:
			out.WriteString(f.pumpInline())
		case f.sol:
			out.WriteString(f.pumpSOL(eof))
		default:
			out.WriteString(f.pumpBody(eof))
		}

		if len(f.buf) == sLen && f.sol == sSol && f.fence == sFence && f.inl == sInl {
			break
		}
	}

	if eof && f.inl != nil {
		out.WriteString(inlineMarkers[f.inl.kind] + f.inl.acc)
		f.inl = nil
	}
	return out.String()
}

// pumpFence passes content and markers inside a code fence through verbatim.
func (f *streamingMarkdownFilter) pumpFence(eof bool) string {
	if f.sol {
		if len(f.buf) < 3 && !eof {
			return ""
		}
		if strings.HasPrefix(f.buf, "```") {
			if nl := indexFrom(f.buf, "\n", 3); nl != -1 {
				f.fence = false
				line := f.buf[:nl+1]
				f.buf = f.buf[nl+1:]
				f.sol = true
				return line
			}
			if eof {
				f.fence = false
				line := f.buf
				f.buf = ""
				return line
			}
			return ""
		}
		f.sol = false
	}
	if nl := strings.IndexByte(f.buf, '\n'); nl != -1 {
		chunk := f.buf[:nl+1]
		f.buf = f.buf[nl+1:]
		f.sol = true
		return chunk
	}
	chunk := f.buf
	f.buf = ""
	return chunk
}

// pumpSOL detects and consumes line-start patterns, then hands over to body.
func (f *streamingMarkdownFilter) pumpSOL(eof bool) string {
	b := f.buf

	switch b[0] {
	case '\n':
		f.buf = b[1:]
		return "\n"

	case '`':
		if len(b) < 3 && !eof {
			return ""
		}
		if strings.HasPrefix(b, "```") {
			if nl := indexFrom(b, "\n", 3); nl != -1 {
				f.fence = true
				line := b[:nl+1]
				f.buf = b[nl+1:]
				f.sol = true
				return line
			}
			if eof {
				f.buf = ""
				return b
			}
			return ""
		}
		f.sol = false
		return ""

	case '>':
		f.sol = false
		return ""

	case '#':
		n := 0
		for n < len(b) && b[n] == '#' {
			n++
		}
		if n == len(b) && !eof {
			return ""
		}
		if n >= 5 && n <= 6 && n < len(b) && b[n] == ' ' {
			f.buf = b[n+1:]
			f.sol = false
			return ""
		}
		f.sol = false
		return ""

	case ' ', '\t':
		if strings.TrimLeft(b, " \t") == "" && !eof {
			return ""
		}
		f.sol = false
		return ""

	case '-', '*', '_':
		ch := b[0]
		j := 0
		for j < len(b) && (b[j] == ch || b[j] == ' ') {
			j++
		}
		if j == len(b) && !eof {
			return ""
		}
		if j == len(b) || b[j] == '\n' {
			count := strings.Count(b[:j], string(ch))
			if count >= 3 {
				if j < len(b) {
					f.buf = b[j+1:]
					f.sol = true
					return b[:j+1]
				}
				f.buf = ""
				return b
			}
		}
		f.sol = false
		return ""
	}

	f.sol = false
	return ""
}

// pumpBody scans a line body for inline pattern triggers and emits safe
// characters eagerly.
func (f *streamingMarkdownFilter) pumpBody(eof bool) string {
	b := f.buf
	i := 0
	for i < len(b) {
		c := b[i]
		switch {
		case c == '\n':
			out := b[:i+1]
			f.buf = b[i+1:]
			f.sol = true
			return out
		case c == '!' && i+1 < len(b) && b[i+1] == '[':
			out := b[:i]
			f.buf = b[i+2:]
			f.inl = &inlineState{kind: inlineImage}
			return out
		case c == '*' || c == '_':
			kind3, kind1 := inlineBold3, inlineItalic
			if c == '_' {
				kind3, kind1 = inlineUBold3, inlineUItalic
			}
			if i+2 < len(b) && b[i+1] == c && b[i+2] == c {
				out := b[:i]
				f.buf = b[i+3:]
				f.inl = &inlineState{kind: kind3}
				return out
			}
			if i+1 < len(b) && b[i+1] == c {
				i += 2
				continue
			}
			if i+1 < len(b) && b[i+1] != ' ' && b[i+1] != '\n' {
				out := b[:i]
				f.buf = b[i+1:]
				f.inl = &inlineState{kind: kind1}
				return out
			}
		}
		i++
	}

	hold := 0
	if !eof {
		switch {
		case strings.HasSuffix(b, "**"), strings.HasSuffix(b, "__"):
			hold = 2
		case strings.HasSuffix(b, "*"), strings.HasSuffix(b, "_"), strings.HasSuffix(b, "!"):
			hold = 1
		}
	}
	out := b[:len(b)-hold]
	f.buf = b[len(b)-hold:]
	return out
}

// pumpInline accumulates inline content until the closing marker arrives.
func (f *streamingMarkdownFilter) pumpInline() string {
	inl := f.inl
	inl.acc += f.buf
	f.buf = ""

	switch inl.kind {
	case inlineBold3, inlineUBold3:
		marker := inlineMarkers[inl.kind]
		idx := strings.Index(inl.acc, marker)
		if idx == -1 {
			return ""
		}
		content := inl.acc[:idx]
		f.buf = inl.acc[idx+3:]
		f.inl = nil
		if containsCJK(content) {
			return content
		}
		return marker + content + marker

	case inlineItalic, inlineUItalic:
		marker := inlineMarkers[inl.kind]
		m := marker[0]
		for j := 0; j < len(inl.acc); j++ {
			if inl.acc[j] == '\n' {
				r := marker + inl.acc[:j+1]
				f.buf = inl.acc[j+1:]
				f.inl = nil
				f.sol = true
				return r
			}
			if inl.acc[j] == m {
				if j+1 < len(inl.acc) && inl.acc[j+1] == m {
					j++
					continue
				}
				content := inl.acc[:j]
				f.buf = inl.acc[j+1:]
				f.inl = nil
				if containsCJK(content) {
					return content
				}
				return marker + content + marker
			}
		}
		return ""

	case inlineImage:
		cb := strings.IndexByte(inl.acc, ']')
		if cb == -1 || cb+1 >= len(inl.acc) {
			return ""
		}
		if inl.acc[cb+1] != '(' {
			r := "![" + inl.acc[:cb+1]
			f.buf = inl.acc[cb+1:]
			f.inl = nil
			return r
		}
		if cp := indexFrom(inl.acc, ")", cb+2); cp != -1 {
			f.buf = inl.acc[cp+1:]
			f.inl = nil
		}
		return ""
	}
	return ""
}

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	if i := strings.Index(s[from:], sub); i != -1 {
		return i + from
	}
	return -1
}

// containsCJK mirrors upstream's /[⺀-鿿가-힯豈-﫿]/.
func containsCJK(text string) bool {
	for _, r := range text {
		if (r >= 0x2E80 && r <= 0x9FFF) || (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0xF900 && r <= 0xFAFF) {
			return true
		}
	}
	return false
}
