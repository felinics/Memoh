import { describe, expect, it } from 'vitest'
import { captureTextPageOffsets } from './context-trajectory-text'

describe('captured text pages', () => {
  it.each([2, 3, 4, 8192])('preserves every code unit without splitting surrogate pairs or CRLF at size %s', (size) => {
    const text = `a🙂\r\nb汉字${'🙂z\r\n'.repeat(3000)}LAST_BYTE`
    const offsets = captureTextPageOffsets(text, size)
    const pages = offsets.slice(0, -1).map((start, index) => text.slice(start, offsets[index + 1]))
    expect(pages.join('')).toBe(text)
    for (const [index, page] of pages.entries()) {
      expect(page.length).toBeGreaterThan(0)
      expect(page.length).toBeLessThanOrEqual(size)
      expect(page).not.toMatch(/^[\uDC00-\uDFFF]|[\uD800-\uDBFF]$/u)
      expect(page.endsWith('\r') && pages[index + 1]?.startsWith('\n')).not.toBe(true)
    }
  })

  it('handles empty text and invalid sizes without losing content', () => {
    expect(captureTextPageOffsets('')).toEqual([0, 0])
    for (const size of [0, 1, Number.NaN, Number.POSITIVE_INFINITY]) {
      const offsets = captureTextPageOffsets('🙂\r\ntail', size)
      expect(offsets.at(-1)).toBe(8)
      expect(offsets.every(Number.isFinite)).toBe(true)
    }
  })
})
