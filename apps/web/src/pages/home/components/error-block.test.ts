import { describe, expect, it } from 'vitest'
import { errorBlockText } from './error-block'

const copy: Record<string, string> = {
  'errors.agent.response_interrupted': 'The response was interrupted.',
  'errors.bot_agent.not_found': 'Agent {name} was not found.',
  'errors.internal': 'Something went wrong on the server. Please try again.',
}
const te = (key: string) => key in copy
const t = (key: string, args: Record<string, string> = {}) =>
  (copy[key] ?? key).replace(/\{(\w+)\}/g, (_, name: string) => args[name] ?? '')

describe('errorBlockText', () => {
  it('renders the copy for the block code, not its content', () => {
    expect(errorBlockText({ code: ' agent.response_interrupted ', content: 'stream reset' }, t, te))
      .toBe('The response was interrupted.')
  })

  it('passes the block args to the copy', () => {
    expect(errorBlockText({ code: 'bot_agent.not_found', content: '', args: { name: 'codex' } }, t, te))
      .toBe('Agent codex was not found.')
  })

  it('renders generic copy for a code without copy instead of the server detail', () => {
    expect(errorBlockText({ code: 'payload_conflict', content: 'raw server detail' }, t, te))
      .toBe('Something went wrong on the server. Please try again.')
  })

  it('renders the content of a block without a code', () => {
    expect(errorBlockText({ content: 'model failed' }, t, te)).toBe('model failed')
  })
})
