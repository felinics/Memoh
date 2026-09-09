import { describe, expect, it } from 'vitest'
import { isCommandResultItemSelectable, resolveCommandResultSelection } from './slash-command-result'

describe('slash command result helpers', () => {
  it('maps known help quick actions to sendable slash text', () => {
    expect(resolveCommandResultSelection({ id: 'help', title: 'Help', kind: 'quick_action' })).toEqual({
      kind: 'quick_action',
      id: 'help',
      text: '/help',
    })
    expect(resolveCommandResultSelection({ id: 'skill.list', title: 'Skills', kind: 'quick_action' })).toEqual({
      kind: 'quick_action',
      id: 'skill.list',
      text: '/skill list',
    })
  })

  it('allows server-provided slash titles for unknown quick actions', () => {
    expect(resolveCommandResultSelection({ id: 'custom', title: '/custom action', kind: 'quick_action' })).toEqual({
      kind: 'quick_action',
      id: 'custom',
      text: '/custom action',
    })
  })

  it('does not make the current quick action selectable from its own result', () => {
    const item = { id: 'help', title: '/help', kind: 'quick_action' }

    expect(resolveCommandResultSelection(item, 'help')).toBeNull()
    expect(isCommandResultItemSelectable(item, 'help')).toBe(false)
  })

  it('routes an opaque ACP mode id through permission', () => {
    const modeId = ' review:deep '
    expect(resolveCommandResultSelection({ id: modeId, title: modeId, kind: 'acp_mode' })).toEqual({
      kind: 'acp_permission',
      modeId,
    })
  })

  it('marks only executable command result rows as selectable', () => {
    expect(isCommandResultItemSelectable({ id: 'skill.list', title: '/skill list', kind: 'quick_action' })).toBe(true)
    expect(isCommandResultItemSelectable({ id: 'skill-a', title: 'skill-a', kind: 'skill' })).toBe(true)
    expect(isCommandResultItemSelectable({ id: 'note', title: 'Note', kind: 'quick_action' })).toBe(false)
    expect(isCommandResultItemSelectable({ id: 'note', title: 'Note' })).toBe(false)
  })

})
