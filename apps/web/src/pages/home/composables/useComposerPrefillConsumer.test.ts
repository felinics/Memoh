// @vitest-environment jsdom

import { createApp, defineComponent, effectScope, h, KeepAlive, nextTick, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useComposerPrefillStore } from '@/store/composer-prefill'
import { useComposerPrefillConsumer } from './useComposerPrefillConsumer'

interface PaneState {
  botId?: string
  active?: boolean
  writable?: boolean
  ready?: boolean
  text?: string
  confirm?: boolean
}

function mountPane(state: PaneState = {}) {
  const botId = ref(state.botId ?? 'bot-a')
  const active = ref(state.active ?? true)
  const writable = ref(state.writable ?? true)
  const ready = ref(state.ready ?? true)
  const text = ref(state.text ?? '')
  const apply = vi.fn((next: string) => { text.value = next })
  const confirmReplace = vi.fn(async () => state.confirm ?? true)
  effectScope().run(() => useComposerPrefillConsumer({
    botId: () => botId.value,
    active: () => active.value,
    writable: () => writable.value,
    ready: () => ready.value,
    currentText: () => text.value,
    confirmReplace,
    apply,
  }))
  return { botId, active, writable, ready, text, apply, confirmReplace }
}

/** Let the consumer's async confirm step run to completion. */
async function settle() {
  await nextTick()
  await Promise.resolve()
  await Promise.resolve()
}

describe('useComposerPrefillConsumer', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('applies a request into an empty composer without asking', async () => {
    const pane = mountPane()
    const outcome = useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(pane.confirmReplace).not.toHaveBeenCalled()
    expect(pane.apply).toHaveBeenCalledWith('hello')
    await expect(outcome).resolves.toBe('applied')
  })

  it('treats a whitespace-only draft as empty', async () => {
    const pane = mountPane({ text: '  \n ' })
    void useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(pane.confirmReplace).not.toHaveBeenCalled()
    expect(pane.apply).toHaveBeenCalledWith('hello')
  })

  it('asks before replacing unsent text, and replaces it when confirmed', async () => {
    const pane = mountPane({ text: 'my draft', confirm: true })
    const outcome = useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(pane.confirmReplace).toHaveBeenCalledOnce()
    expect(pane.text.value).toBe('hello')
    await expect(outcome).resolves.toBe('applied')
  })

  it('keeps the unsent text when the user declines', async () => {
    const pane = mountPane({ text: 'my draft', confirm: false })
    const outcome = useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(pane.apply).not.toHaveBeenCalled()
    expect(pane.text.value).toBe('my draft')
    await expect(outcome).resolves.toBe('cancelled')
    expect(useComposerPrefillStore().pending).toBeNull()
  })

  it('does not ask when the composer already holds the same text', async () => {
    const pane = mountPane({ text: 'hello' })
    const outcome = useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(pane.confirmReplace).not.toHaveBeenCalled()
    await expect(outcome).resolves.toBe('applied')
  })

  it('replaces a prompt it prefilled itself without asking while the user has not edited it', async () => {
    const pane = mountPane()
    void useComposerPrefillStore().request('bot-a', 'first prompt')
    await settle()
    const outcome = useComposerPrefillStore().request('bot-a', 'second prompt')
    await settle()
    expect(pane.confirmReplace).not.toHaveBeenCalled()
    expect(pane.text.value).toBe('second prompt')
    await expect(outcome).resolves.toBe('applied')
  })

  it('asks again once the user has edited the prefilled prompt', async () => {
    const pane = mountPane({ confirm: false })
    void useComposerPrefillStore().request('bot-a', 'first prompt')
    await settle()
    pane.text.value = 'first prompt, with my changes'
    const outcome = useComposerPrefillStore().request('bot-a', 'second prompt')
    await settle()
    expect(pane.confirmReplace).toHaveBeenCalledOnce()
    expect(pane.text.value).toBe('first prompt, with my changes')
    await expect(outcome).resolves.toBe('cancelled')
  })

  it('applies a request that was queued before the pane mounted', async () => {
    void useComposerPrefillStore().request('bot-a', 'hello')
    const pane = mountPane()
    await settle()
    expect(pane.apply).toHaveBeenCalledWith('hello')
  })

  it('ignores requests for another bot', async () => {
    const pane = mountPane()
    void useComposerPrefillStore().request('bot-b', 'hello')
    await settle()
    expect(pane.apply).not.toHaveBeenCalled()
    expect(useComposerPrefillStore().pending?.botId).toBe('bot-b')
  })

  it('waits while inactive or read-only, then applies once it can', async () => {
    const pane = mountPane({ active: false, writable: false })
    void useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    pane.active.value = true
    await settle()
    expect(pane.apply).not.toHaveBeenCalled()
    pane.writable.value = true
    await settle()
    expect(pane.apply).toHaveBeenCalledOnce()
  })

  it('waits while the bot is still loading, so a pane of the previous bot cannot take it', async () => {
    const pane = mountPane({ ready: false })
    void useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(pane.apply).not.toHaveBeenCalled()
    pane.ready.value = true
    await settle()
    expect(pane.apply).toHaveBeenCalledWith('hello')
  })

  it('lets only the active pane of two consume the request', async () => {
    const background = mountPane({ active: false })
    const foreground = mountPane()
    void useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(foreground.apply).toHaveBeenCalledOnce()
    expect(background.apply).not.toHaveBeenCalled()
  })

  it('never lets a pane that KeepAlive has deactivated take the request', async () => {
    // A cached tab keeps its last props, so it still claims to be active.
    const applied: string[] = []
    // eslint-disable-next-line vue/one-component-per-file -- a pane and its KeepAlive host
    const Pane = defineComponent({
      props: { name: { type: String, required: true } },
      setup(props) {
        useComposerPrefillConsumer({
          botId: () => 'bot-a',
          active: () => true,
          writable: () => true,
          ready: () => true,
          currentText: () => '',
          confirmReplace: async () => true,
          apply: text => applied.push(`${props.name}:${text}`),
        })
        return () => h('div', props.name)
      },
    })
    const shown = ref('old')
    const root = document.createElement('div')
    // eslint-disable-next-line vue/one-component-per-file -- a pane and its KeepAlive host
    const app = createApp({
      render: () => h(KeepAlive, null, [h(Pane, { key: shown.value, name: shown.value })]),
    })
    app.mount(root)
    shown.value = 'new'
    await settle()
    void useComposerPrefillStore().request('bot-a', 'hello')
    await settle()
    expect(applied).toEqual(['new:hello'])
    app.unmount()
  })
})
