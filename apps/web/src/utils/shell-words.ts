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
// escapeShellArg (internal/handlers/mcp_stdio.go).
export function quoteShellWord(value: string): string {
  if (value === '') return '\'\''
  if (!/[\s'"\\$&;|<>*?()[\]{}!`]/.test(value)) return value
  return `'${value.replace(/'/g, '\'\\\'\'')}'`
}

// joinShellWords renders a stored command+args pair as the single line a user
// edits. The command token is NOT quoted: a legit executable never contains
// whitespace, and a legacy config that stored a whole line as one token must
// re-display as that line so the next save parses it back into shape.
export function joinShellWords(command: string, args: string[]): string {
  return [command.trim(), ...args.map(quoteShellWord)].filter((s) => s !== '').join(' ')
}
