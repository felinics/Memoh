import { computed, reactive } from 'vue'
import { beforeAll, describe, expect, it } from 'vitest'
import i18n, { prepareLocale } from '@/i18n'
import { resolveWorkbenchTabTitle } from './titles'
import type { WorkbenchTabTarget } from './types'

describe('workbench reactive tab titles', () => {
  beforeAll(async () => {
    await Promise.all([prepareLocale('en'), prepareLocale('zh'), prepareLocale('ja')])
  })

  it('re-derives draft chat, terminal, browser, display, preview and schedule titles across en -> zh -> ja', () => {
    i18n.global.locale.value = 'en'
    const draftChat: WorkbenchTabTarget = { kind: 'chat', sessionId: null, explicitSelection: false }
    const terminal: WorkbenchTabTarget = { kind: 'terminal', instance: 2 }
    const browser: WorkbenchTabTarget = { kind: 'browser', instance: 1, address: 'localhost:5173/', customTitle: false }
    const display: WorkbenchTabTarget = { kind: 'display' }
    const preview: WorkbenchTabTarget = { kind: 'preview', filePath: '/data/README.md' }
    const schedule: WorkbenchTabTarget = { kind: 'schedule' }

    const titles = computed(() => ({
      draft: resolveWorkbenchTabTitle(draftChat),
      terminal: resolveWorkbenchTabTitle(terminal),
      browser: resolveWorkbenchTabTitle(browser),
      display: resolveWorkbenchTabTitle(display),
      preview: resolveWorkbenchTabTitle(preview),
      schedule: resolveWorkbenchTabTitle(schedule),
    }))

    expect(titles.value.draft).toBe(i18n.global.t('chat.newSession'))
    expect(titles.value.terminal).toContain('2')
    expect(titles.value.preview).toContain('README.md')

    i18n.global.locale.value = 'zh'
    expect(titles.value.draft).toBe('新会话')
    expect(titles.value.terminal).toBe(`${i18n.global.t('bots.terminal.defaultTabLabel')} 2`)
    expect(titles.value.browser).toBe(`${i18n.global.t('chat.tabs.browser')} 1`)
    expect(titles.value.display).toBe(i18n.global.t('chat.display.title'))
    expect(titles.value.schedule).toBe(i18n.global.t('chat.tabs.schedule'))

    i18n.global.locale.value = 'ja'
    expect(titles.value.draft).toBe(i18n.global.t('chat.newSession'))
    expect(titles.value.browser).toBe(`${i18n.global.t('chat.tabs.browser')} 1`)
    expect(titles.value.preview).toContain('README.md')

    i18n.global.locale.value = 'en'
  })

  it('tracks live session summary renames, tentative state, and channel conversation labels', () => {
    i18n.global.locale.value = 'en'
    const state = reactive({
      tentative: true,
      summary: {
        id: 'sess-1',
        title: '',
        route_metadata: { conversation_name: 'Design Room' } as Record<string, unknown>,
      },
    })
    const lookup = {
      isSessionTentative: () => state.tentative,
      knownSessionSummary: () => state.summary,
    }
    const target: WorkbenchTabTarget = { kind: 'chat', sessionId: 'sess-1', explicitSelection: true }
    const label = computed(() => resolveWorkbenchTabTitle(target, lookup))

    expect(label.value).toBe(i18n.global.t('chat.newSession'))

    state.tentative = false
    expect(label.value).toBe('Design Room')

    state.summary.title = 'Workbench Architecture'
    expect(label.value).toBe('Workbench Architecture')
  })
})
