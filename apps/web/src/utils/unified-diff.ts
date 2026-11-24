// Pure unified-diff parsing helpers shared by tool-call titles and the code
// surfaces. They contain no rendering and no highlighter dependency so they can
// be used outside a component (and unit-tested) directly.

export type ContextDiffRowKind = 'context' | 'remove' | 'add'

export interface ParsedDiffLine {
  kind: ContextDiffRowKind
  lineNumber: number
  text: string
}

// UnifiedDiffFile is one file's section of a multi-file unified diff, with
// the a/ and b/ header prefixes stripped from its paths.
export interface UnifiedDiffFile {
  oldPath: string
  newPath: string
  diff: string
}

const HUNK_HEADER = /^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@/

function diffHeaderPath(line: string, prefix: string): string {
  const path = line.slice(4)
  if (path === '/dev/null') return ''
  return path.startsWith(prefix) ? path.slice(prefix.length) : path
}

// splitUnifiedDiffFiles cuts a multi-file unified diff (apply_patch's UI diff)
// into per-file sections, since a diff surface takes one file. Hunk ends come
// from the line counts in each @@ header, not from spotting the next ---/+++
// pair: a removed "-- x" line followed by an added "++ y" line looks exactly
// like a file header. Input that does not parse cleanly — wrong counts, text
// outside a hunk — yields [] so callers fall back to their raw view instead of
// rendering misattributed rows.
export function splitUnifiedDiffFiles(diffText: string): UnifiedDiffFile[] {
  const lines = diffText.split('\n')
  if (lines.at(-1) === '') lines.pop()
  const files: UnifiedDiffFile[] = []
  let i = 0
  while (i < lines.length) {
    const fromLine = lines[i]!
    const toLine = lines[i + 1]
    if (!fromLine.startsWith('--- ') || !toLine?.startsWith('+++ ')) return []
    const start = i
    i += 2
    while (i < lines.length && lines[i]!.startsWith('@@')) {
      const match = lines[i]!.match(HUNK_HEADER)
      if (!match) return []
      // An omitted count means one line.
      let oldLeft = match[1] === undefined ? 1 : Number(match[1])
      let newLeft = match[2] === undefined ? 1 : Number(match[2])
      i += 1
      while (oldLeft > 0 || newLeft > 0) {
        const line = lines[i]
        if (line === undefined) return []
        i += 1
        if (line.startsWith('\\')) continue
        if (line.startsWith('-')) oldLeft -= 1
        else if (line.startsWith('+')) newLeft -= 1
        else {
          oldLeft -= 1
          newLeft -= 1
        }
        if (oldLeft < 0 || newLeft < 0) return []
      }
      // The last line of a side may still carry its "\ No newline" marker.
      while (i < lines.length && lines[i]!.startsWith('\\')) i += 1
    }
    files.push({
      oldPath: diffHeaderPath(fromLine, 'a/'),
      newPath: diffHeaderPath(toLine, 'b/'),
      diff: lines.slice(start, i).join('\n'),
    })
  }
  return files
}

// parseUnifiedDiffRows drops the ---/+++ file headers (they duplicate the row
// title) and the "\ No newline" marker, strips the one-character prefix from
// content lines, and uses the @@ hunk headers to track the old/new line
// counters: removal rows show the old line number, addition and context rows
// the new one. Hunk header lines themselves are never rendered.
export function parseUnifiedDiffRows(diffText: string): ParsedDiffLine[] {
  const rows: ParsedDiffLine[] = []
  let oldLine = 1
  let newLine = 1
  let seenHunk = false
  // A trailing newline would split into a phantom empty content line; real
  // content lines always carry their prefix, so only the tail can be bare.
  const rawLines = diffText.split('\n')
  if (rawLines.at(-1) === '') rawLines.pop()
  for (const raw of rawLines) {
    if (raw.startsWith('@@')) {
      seenHunk = true
      const match = raw.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/)
      if (match) {
        oldLine = Number(match[1])
        newLine = Number(match[2])
      }
      continue
    }
    // ---/+++ are file headers only before the first hunk. Inside a hunk they
    // are content lines whose text starts with "-- " or "++ " (a removed SQL
    // comment, an added diff marker) — eating them would also desync the line
    // counters for the rest of the hunk.
    if (!seenHunk && (raw.startsWith('--- ') || raw.startsWith('+++ '))) continue
    if (raw.startsWith('\\')) continue
    if (raw.startsWith('-')) {
      rows.push({ kind: 'remove', lineNumber: oldLine, text: raw.slice(1) })
      oldLine += 1
      continue
    }
    if (raw.startsWith('+')) {
      rows.push({ kind: 'add', lineNumber: newLine, text: raw.slice(1) })
      newLine += 1
      continue
    }
    // Unified diff context lines carry a leading space; tolerate its absence.
    rows.push({ kind: 'context', lineNumber: newLine, text: raw.startsWith(' ') ? raw.slice(1) : raw })
    oldLine += 1
    newLine += 1
  }
  return rows
}

// hasUnifiedDiffHunks reports whether the text is a unified diff a patch
// surface can parse. apply_patch's own `*** Begin Patch` format is not one.
export function hasUnifiedDiffHunks(diffText: string): boolean {
  return /^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@/m.test(diffText)
    && /^--- /m.test(diffText)
    && /^\+\+\+ /m.test(diffText)
}

export function extractFilename(path: string): string {
  return path.split('/').pop() ?? path
}
