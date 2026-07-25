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
//   split). Heuristic: a multi-word token whose first word is a bare name (no
//   path separator) is a pasted line, not a spaced path.
export function joinShellWords(command: string, args: string[]): string {
  const token = command.trim()
  const words = token === '' ? [] : splitShellWords(token)
  const isLegacyWholeLine = words.length > 1 && !/[/\\]/.test(words[0] ?? '')
  const rendered = token === '' || isLegacyWholeLine ? token : quoteShellWord(token)
  return [rendered, ...args.map(quoteShellWord)].filter((s) => s !== '').join(' ')
}
