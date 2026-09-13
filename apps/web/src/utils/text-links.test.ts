import { describe, expect, it } from 'vitest'

import { splitTextLinks } from './text-links'

describe('splitTextLinks', () => {
  it('leaves sentence punctuation outside the link', () => {
    expect(splitTextLinks('Open https://example.com/a, then (https://example.com/b).')).toEqual([
      { text: 'Open ' },
      { text: 'https://example.com/a', href: 'https://example.com/a' },
      { text: ', then (' },
      { text: 'https://example.com/b', href: 'https://example.com/b' },
      { text: ').' },
    ])
  })

  it('keeps a closing bracket that the address opened', () => {
    expect(splitTextLinks('https://example.com/wiki/A_(b)')).toEqual([
      { text: 'https://example.com/wiki/A_(b)', href: 'https://example.com/wiki/A_(b)' },
    ])
  })
})
