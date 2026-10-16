import { describe, expect, it } from 'vitest'
import type { BotsBot } from '@memohai/sdk'
import { switcherBots } from './bot-switcher-list'

function bot(id: string, status = 'ready'): BotsBot {
  return { id, status } as BotsBot
}

const keepOrder = <T>(list: T[]) => [...list]

describe('switcherBots', () => {
  it('hides bots that are being deleted', () => {
    const list = switcherBots([bot('a'), bot('b', 'deleting'), bot('c', 'creating')], keepOrder, [])
    expect(list.map(b => b.id)).toEqual(['a', 'c'])
  })

  it('applies the pin sort, then the saved manual order', () => {
    const pinFirst = (list: BotsBot[]) => [...list].sort(a => (a.id === 'c' ? -1 : 0))
    expect(switcherBots([bot('a'), bot('b'), bot('c')], pinFirst, []).map(b => b.id))
      .toEqual(['c', 'a', 'b'])
    expect(switcherBots([bot('a'), bot('b'), bot('c')], pinFirst, ['b', 'a']).map(b => b.id))
      .toEqual(['b', 'a', 'c'])
  })
})
