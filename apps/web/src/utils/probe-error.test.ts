import { describe, expect, it } from 'vitest'
import { formatProbeError } from './probe-error'

describe('formatProbeError', () => {
  it('returns the fallback for empty input', () => {
    expect(formatProbeError(undefined, 'unreachable')).toBe('unreachable')
    expect(formatProbeError('   ', 'unreachable')).toBe('unreachable')
  })

  it('passes short plain messages through', () => {
    expect(formatProbeError('authentication failed (HTTP 401)', 'x')).toBe('authentication failed (HTTP 401)')
  })

  it('truncates long messages with an ellipsis', () => {
    const long = 'a'.repeat(300)
    const out = formatProbeError(long, 'x')
    expect(out).toHaveLength(221)
    expect(out.endsWith('…')).toBe(true)
  })

  it('drops html from an embedded [body: …] payload', () => {
    const raw = 'service error (404): [body: <!doctype html><html><head><style>p{color:red}</style></head><body><p>Not Found</p></body></html>]'
    expect(formatProbeError(raw, 'x')).toBe('service error (404):')
  })

  it('keeps only the head when the body collapses to nothing', () => {
    const raw = 'service error (502): [body: <html><script>boom()</script></html>]'
    expect(formatProbeError(raw, 'x')).toBe('service error (502):')
  })
})
