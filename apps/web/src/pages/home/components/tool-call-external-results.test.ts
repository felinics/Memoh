// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { ToolCallBlock } from '@/store/chat-list'
import ExecDetail from './tool-call-detail-exec.vue'
import SearchDetail from './tool-call-detail-web-search.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('external tool output compatibility', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined
  afterEach(() => {
    app?.unmount()
    root?.remove()
  })

  for (const [name, component, result, text] of [
    ['old Codex', ExecDetail, { output: 'old command output', exitCode: 0 }, 'old command output'],
    ['Claude text', ExecDetail, 'plain command output', 'plain command output'],
    ['Claude blocks', ExecDetail, [{ type: 'text', text: 'block output' }], 'block output'],
    ['native exec', ExecDetail, { stdout: 'native output', stderr: '' }, 'native output'],
    ['search text', SearchDetail, { results: [], content: 'search source text' }, 'search source text'],
    ['search error', SearchDetail, { isError: true, content: [{ type: 'text', text: 'search failed' }] }, 'search failed'],
    ['empty search', SearchDetail, { results: [] }, 'chat.tools.detail.noResults'],
  ] as const) {
    it(`shows ${name}`, async () => {
      const block = { result, input: {}, done: true } as unknown as ToolCallBlock
      root = document.createElement('div')
      document.body.append(root)
      app = createApp({ render: () => h(component, { block }) })
      app.mount(root)
      await nextTick()
      expect(root.textContent).toContain(text)
      expect(root.textContent).not.toContain('chat.tools.detail.noOutput')
    })
  }
})
