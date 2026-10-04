import { describe, expect, it } from 'vitest'
import { createSessionSelection } from './session-selection'

describe('createSessionSelection', () => {
  it('is active exactly while something is checked', () => {
    const selection = createSessionSelection()
    expect(selection.active.value).toBe(false)

    selection.select('a')
    expect(selection.active.value).toBe(true)
    expect(selection.isSelected('a')).toBe(true)

    selection.toggle('b')
    selection.toggle('a')
    expect([...selection.selectedIds.value]).toEqual(['b'])

    selection.toggle('b')
    expect(selection.active.value).toBe(false)
  })

  it('retains only ids that are still selected', () => {
    const selection = createSessionSelection()
    selection.select('a')
    selection.select('b')
    selection.select('c')

    selection.retain(['b', 'x'])
    expect([...selection.selectedIds.value]).toEqual(['b'])
  })

  it('removes and clears without touching other ids', () => {
    const selection = createSessionSelection()
    selection.select('a')
    selection.select('b')

    selection.remove('missing')
    selection.remove('a')
    expect([...selection.selectedIds.value]).toEqual(['b'])

    selection.clear()
    expect(selection.active.value).toBe(false)
  })
})
