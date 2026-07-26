import { describe, expect, it } from 'vitest'
import { joinShellWords, splitShellWords } from './shell-words'

describe('splitShellWords', () => {
  it('splits a plain one-line command', () => {
    expect(splitShellWords('npx -y @modelcontextprotocol/server-everything')).toEqual([
      'npx',
      '-y',
      '@modelcontextprotocol/server-everything',
    ])
  })

  it('collapses repeated whitespace', () => {
    expect(splitShellWords('  cmd   arg1\t\targ2  ')).toEqual(['cmd', 'arg1', 'arg2'])
  })

  it('keeps double-quoted segments together', () => {
    expect(splitShellWords('cmd "hello world" rest')).toEqual(['cmd', 'hello world', 'rest'])
  })

  it('keeps single-quoted segments together', () => {
    expect(splitShellWords('cmd \'it works\'')).toEqual(['cmd', 'it works'])
  })

  it('treats backslash as an escape outside single quotes', () => {
    expect(splitShellWords('cmd a\\ b')).toEqual(['cmd', 'a b'])
  })

  it('keeps backslash literal inside single quotes', () => {
    expect(splitShellWords('cmd \'a\\b\'')).toEqual(['cmd', 'a\\b'])
  })

  it('keeps shell operators as literal tokens', () => {
    expect(splitShellWords('cmd && other')).toEqual(['cmd', '&&', 'other'])
  })

  it('returns a single token for a bare executable', () => {
    expect(splitShellWords('npx')).toEqual(['npx'])
  })

  it('returns nothing for empty input', () => {
    expect(splitShellWords('')).toEqual([])
    expect(splitShellWords('   ')).toEqual([])
  })

  it('handles an unterminated quote by taking the rest verbatim', () => {
    expect(splitShellWords('cmd "dangling')).toEqual(['cmd', 'dangling'])
  })
})

describe('joinShellWords', () => {
  it('joins a plain command and args', () => {
    expect(joinShellWords('npx', ['-y', '@modelcontextprotocol/server-everything'])).toBe(
      'npx -y @modelcontextprotocol/server-everything',
    )
  })

  it('quotes args that contain whitespace', () => {
    expect(joinShellWords('cmd', ['hello world'])).toBe('cmd \'hello world\'')
  })

  it('leaves a legacy whole-line command untouched so it can re-parse', () => {
    expect(joinShellWords('npx -y pkg', [])).toBe('npx -y pkg')
  })

  it('treats a legacy line with a scoped package as legacy (separator in a later word)', () => {
    expect(joinShellWords('npx -y @modelcontextprotocol/server-everything', [])).toBe(
      'npx -y @modelcontextprotocol/server-everything',
    )
  })

  it('treats a flagless legacy line as legacy', () => {
    expect(joinShellWords('node server.js', [])).toBe('node server.js')
  })

  it('quotes a bare relative path that contains whitespace instead of misreading it as legacy', () => {
    // Regression: "my dir/srv" is one executable, not a pasted line. Rendering
    // it raw would re-split into command="my" + phantom args and a save would
    // persist the corruption over a working config.
    expect(joinShellWords('my dir/srv', [])).toBe('\'my dir/srv\'')
    expect(splitShellWords(joinShellWords('my dir/srv', []))).toEqual(['my dir/srv'])
  })

  it('never treats a multi-word command as legacy when args are present', () => {
    expect(joinShellWords('my dir/srv', ['--port', '8'])).toBe('\'my dir/srv\' --port 8')
  })

  it('quotes an executable path that contains whitespace', () => {
    expect(joinShellWords('/opt/my server/bin/mcp', ['--flag'])).toBe('\'/opt/my server/bin/mcp\' --flag')
  })

  it('quotes a token containing a comment char', () => {
    expect(joinShellWords('cmd', ['#notacomment'])).toBe('cmd \'#notacomment\'')
  })

  it('drops empty pieces but preserves an explicit empty arg', () => {
    expect(joinShellWords('', [])).toBe('')
    expect(joinShellWords('cmd', [''])).toBe('cmd \'\'')
  })
})

describe('join/split round-trip', () => {
  it.each([
    ['npx', ['-y', '@modelcontextprotocol/server-everything']],
    ['cmd', ['hello world', 'it\'s', '']],
    ['uvx', ['mcp-server-fetch']],
  ])('join(%s, %j) splits back to the same words', (command, args) => {
    const line = joinShellWords(command, args.filter((a) => a !== ''))
    expect(splitShellWords(line)).toEqual([command, ...args.filter((a) => a !== '')])
  })
})
