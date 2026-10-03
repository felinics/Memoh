import { describe, expect, it } from 'vitest'
import { effectScope, ref } from 'vue'
import type { SessionSummary } from '@/composables/api/useChat'
import { useSessionSelection } from './use-session-selection'

describe('sidebar session selection', () => {
  it('selects only displayed rows, keeps collapsed selections, and clears on bot changes', () => {
    const scope = effectScope()
    const botId = ref<string | null>('bot-1')
    const locked = ref(false)
    const selection = scope.run(() => useSessionSelection(botId, locked))!
    function row(id: string, bot = 'bot-1') {
      const rowScope = scope.run(() => effectScope())!
      rowScope.run(() => selection.registerRow(() => ({ id, bot_id: bot } as SessionSummary)))
      return rowScope
    }
    row('recent')
    const folder = row('folder')
    row('foreign', 'bot-2')
    selection.active.value = true
    selection.toggleDisplayed()
    expect([...selection.selectedIds.value]).toEqual(['recent', 'folder'])

    folder.stop()
    row('new-page')
    expect(selection.allDisplayedSelected.value).toBe(false)
    expect(selection.selectedIds.value.has('new-page')).toBe(false)
    selection.toggleDisplayed()
    expect([...selection.selectedIds.value]).toEqual(['recent', 'folder', 'new-page'])
    selection.toggleDisplayed()
    expect([...selection.selectedIds.value]).toEqual(['folder'])

    locked.value = true
    selection.toggle('recent')
    selection.toggleDisplayed()
    expect([...selection.selectedIds.value]).toEqual(['folder'])
    locked.value = false
    selection.toggle('recent')
    expect(selection.selectedIds.value.has('recent')).toBe(true)

    botId.value = 'bot-2'
    expect(selection.active.value).toBe(false)
    expect(selection.selectedIds.value.size).toBe(0)
    expect([...selection.displayedIds.value]).toEqual(['foreign'])
    scope.stop()
    expect(selection.displayedIds.value.size).toBe(0)
  })
})
