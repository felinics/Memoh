// Minimal shell-word lexer for splitting a pasted one-line command into an
// executable + argument list. Handles whitespace splits, single/double quotes,
// and backslash escapes; intentionally NOT a full shell parser (no expansions,
// no operators — a pasted "cmd && rm -rf" must stay literal tokens).
export function splitShellWords(input: string): string[] {
  const tokens: string[] = []
  let current = ''
  let quote: '"' | '\'' | null = null
  let escaped = false
  let started = false // a quoted empty string still counts as a token

  for (const ch of input) {
    if (escaped) {
      current += ch
      escaped = false
      started = true
      continue
    }
    if (ch === '\\' && quote !== '\'') {
      escaped = true
      started = true
      continue
    }
    if (quote) {
      if (ch === quote) {
        quote = null
      } else {
        current += ch
      }
      continue
    }
    if (ch === '"' || ch === '\'') {
      quote = ch
      started = true
      continue
    }
    if (/\s/.test(ch)) {
      if (started) {
        tokens.push(current)
        current = ''
        started = false
      }
      continue
    }
    current += ch
    started = true
  }
  if (escaped) current += '\\' // trailing backslash stays literal
  if (started) tokens.push(current)
  return tokens
}

// quoteShellWord is the display-side inverse of splitShellWords: a stored arg
// that contains whitespace or shell metacharacters gets single-quoted so the
// joined line round-trips through the lexer. Kept in sync with the backend's
// escapeShellArg (internal/handlers/mcp_stdio.go) — including '#', which starts
// a comment at the beginning of a bare word.
export function quoteShellWord(value: string): string {
  if (value === '') return '\'\''
  if (!/[\s'"\\$&;|<>*?()[\]{}!`#]/.test(value)) return value
  return `'${value.replace(/'/g, '\'\\\'\'')}'`
}

// joinShellWords renders a stored command+args pair as the single line a user
// edits. Args are always quoted as needed; the command token needs judgment:
// - a REAL executable whose path contains whitespace ("/opt/my server/bin/mcp")
//   must be quoted, or re-parsing the line splits the path into a bogus
//   command + phantom args (the draft then diffs dirty against the untouched
//   snapshot, and a save would persist the corruption);
// - a LEGACY config that stored a whole pasted line as one token ("npx -y pkg")
//   must re-display raw, so the parsed draft differs from the stored shape and
//   the repair surfaces as unsaved changes (the next save writes the clean
//   split).
// The two shapes are statically indistinguishable in general, so the heuristic
// must err toward NEVER corrupting a valid spaced path (buildShellCommand
// escapeShellArg-quotes the stored command, so a stored "my dir/srv" runs
// fine; a legacy whole-line is what is actually broken at runtime). Rules:
//  1. args must be empty — a true legacy whole-line never has separate args;
//  2. the first word is a bare name (no path separator) — "/opt/my ..." is a path;
//  3. some later word looks like a flag ("-y") or carries no path separator —
//     "npx -y @scope/pkg" is legacy, "my dir/srv" is a spaced path.
// ponytail: known ceiling — a legacy line whose args contain a path separator
// and no flag ("python scripts/gen.py") is misread as a spaced path and stays
// un-repaired (as broken as it already was); a spaced path with a dash-prefixed
// segment would be misread as legacy. Upgrade path: persist an explicit
// shape marker on the connection instead of guessing.
export function joinShellWords(command: string, args: string[]): string {
  const token = command.trim()
  const words = token === '' ? [] : splitShellWords(token)
  const isLegacyWholeLine =
    args.length === 0 &&
    words.length > 1 &&
    !/[/\\]/.test(words[0] ?? '') &&
    words.slice(1).some((w) => w.startsWith('-') || !/[/\\]/.test(w))
  const rendered = token === '' || isLegacyWholeLine ? token : quoteShellWord(token)
  return [rendered, ...args.map(quoteShellWord)].filter((s) => s !== '').join(' ')
}
