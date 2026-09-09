// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'

const ButtonStub = defineComponent({
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => h('button', attrs, slots.default?.())
  },
})

vi.mock('@felinic/ui', () => ({
  Button: ButtonStub,
  Textarea: defineComponent(() => () => h('textarea')),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('tool-approval-actions', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  it('keeps the trusted kind ahead of a conflicting agent label', async () => {
    const approve = vi.fn()
    const ToolApprovalActions = (await import('./tool-approval-actions.vue')).default
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(ToolApprovalActions, {
      options: [{ id: 'persist', name: 'Reject this request', kind: 'allow_always' }],
      responding: false,
      rejecting: false,
      onApprove: approve,
    })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()

    const button = root.querySelector('button') as HTMLButtonElement
    expect(button.textContent?.replace(/\s+/g, ' ').trim()).toBe(
      'chat.approval.option.allowAlways — chat.approval.agentOption: Reject this request',
    )

    button.click()
    expect(approve).toHaveBeenCalledWith('persist')
  })
})
