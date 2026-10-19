import { describe, expect, it } from 'vitest'

import { maskSingleDollars, withLiteralSingleDollars } from './markdown-dollar'

const PH = '\uE000'

describe('maskSingleDollars', () => {
  it.each([
    ['US$3–6/月；优质 US$10–30/月', `US${PH}3–6/月；优质 US${PH}10–30/月`],
    ['$3–$6/小时', `${PH}3–${PH}6/小时`],
    ['$3 到 $6', `${PH}3 到 ${PH}6`],
    ['$x^2$', `${PH}x^2${PH}`],
    ['$$a+b$$', '$$a+b$$'],
    ['$$\nx\n$$', '$$\nx\n$$'],
    ['\\(\\theta\\)', '\\(\\theta\\)'],
    ['\\[x\\]', '\\[x\\]'],
    ['\\$5', `${PH}5`],
    ['\\\\$5', `\\\\${PH}5`],
    ['`echo $HOME`', '`echo $HOME`'],
    ['`echo \\$HOME`', '`echo \\$HOME`'],
    ['``a ` $b``', '``a ` $b``'],
    ['a ` $b', `a \` ${PH}b`],
    ['a `x``y` $b', `a \`x\`\`y\` ${PH}b`],
    ['`` a ` `` $c', `\`\` a \` \`\` ${PH}c`],
    ['```bash\necho $PATH\n```', '```bash\necho $PATH\n```'],
    ['no dollars', 'no dollars'],
  ])('masks %j', (input, expected) => {
    expect(maskSingleDollars(input)).toBe(expected)
  })
})

describe('withLiteralSingleDollars', () => {
  it('registers core rules once per md instance', async () => {
    const { getMarkdown } = await import('markstream-vue')
    const md = getMarkdown()
    withLiteralSingleDollars(md)
    const count = () => ((md.core.ruler as unknown as { __rules__?: { name: string }[], rules?: { name: string }[] }).__rules__ ?? (md.core.ruler as unknown as { rules: { name: string }[] }).rules)
      .filter(r => r.name.startsWith('memoh_dollar_')).length
    expect(count()).toBe(2)
    withLiteralSingleDollars(md)
    expect(count()).toBe(2)
  })
})
