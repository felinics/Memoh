import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import { useWorkdirsStore } from './workdirs'

function workdir(id: string, targetKind: 'native' | 'remote'): BotWorkdir {
  return {
    id,
    bot_id: 'bot-1',
    name: `${targetKind} folder`,
    path: targetKind === 'remote' ? '/Users/example/project' : '/data/project',
    target_kind: targetKind,
    workspace_target_id: targetKind === 'remote' ? 'runtime-1' : 'native',
  }
}

describe('workdir session binding', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it.each([
    ['native', 'native-workdir'],
    ['remote', 'remote-workdir'],
  ] as const)('keeps a selected %s workdir for new sessions', (targetKind, workdirId) => {
    const store = useWorkdirsStore()
    store.workdirsByBot = {
      'bot-1': [workdir(workdirId, targetKind)],
    }

    store.setWorkingWorkdir('bot-1', workdirId)

    expect(store.sessionWorkdirIdFor('bot-1')).toBe(workdirId)
  })

  it('does not bind a session after the working folder is cleared', () => {
    const store = useWorkdirsStore()
    store.workdirsByBot = {
      'bot-1': [workdir('remote-workdir', 'remote')],
    }
    store.setWorkingWorkdir('bot-1', 'remote-workdir')

    store.setWorkingWorkdir('bot-1', null)

    expect(store.sessionWorkdirIdFor('bot-1')).toBe('')
  })
})

describe('sessionWorkdirBindingFor', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('reports an unknown kind until the authoritative list is loaded', () => {
    const store = useWorkdirsStore()
    // Directly seeded lists (e.g. cache restores) are not authoritative.
    store.workdirsByBot = { 'bot-1': [workdir('native-workdir', 'native')] }
    store.setWorkingWorkdir('bot-1', 'native-workdir')

    expect(store.sessionWorkdirBindingFor('bot-1')).toEqual({
      id: 'native-workdir',
      kind: '',
      path: '',
    })
  })

  it('returns an empty binding without a working folder', () => {
    const store = useWorkdirsStore()
    expect(store.sessionWorkdirBindingFor('bot-1')).toEqual({ id: '', kind: '', path: '' })
  })
})
