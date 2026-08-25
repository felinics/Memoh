// @vitest-environment jsdom

import { createApp, defineComponent, h, nextTick, shallowRef } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it } from 'vitest'
import type { ThinkingBlock as ThinkingBlockType } from '@/store/chat-list'
import { finalizeReasoning, markReasoningSeen } from './reasoning-timing'
import ThinkingBlock from './thinking-block.vue'

interface MountedThinkingBlock {
  app: ReturnType<typeof createApp>
  root: HTMLDivElement
  setContent: (content: string) => void
}

const mounted: MountedThinkingBlock[] = []

afterEach(() => {
  for (const item of mounted.splice(0)) {
    item.app.unmount()
    item.root.remove()
  }
})

let nextMessageId = 0

function mountThinkingBlock(
  content: string,
  streaming = true,
  messageId = `thinking-message-${nextMessageId++}`,
  reasoningTiming?: ThinkingBlockType['reasoning_timing'],
): MountedThinkingBlock {
  const block = shallowRef<ThinkingBlockType>({ id: 1, type: 'reasoning', content, reasoning_timing: reasoningTiming })
  const harness = defineComponent({
    setup() {
      return () => h(ThinkingBlock, { block: block.value, messageId, streaming })
    },
  })
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(harness)
  app.use(createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        chat: {
          thinkingInProgress: 'Thinking',
          process: {
            thoughtBriefly: 'Thought briefly',
            thoughtSeconds: 'Thought for {seconds}s',
          },
        },
      },
    },
  }))
  app.mount(root)

  const result = {
    app,
    root,
    setContent: (nextContent: string) => {
      block.value = { ...block.value, content: nextContent }
    },
  }
  mounted.push(result)
  return result
}

function unmountThinkingBlock(item: MountedThinkingBlock): void {
  item.app.unmount()
  item.root.remove()
  mounted.splice(mounted.indexOf(item), 1)
}

function disclosureButton(root: HTMLElement): HTMLButtonElement {
  const button = root.querySelector<HTMLButtonElement>('button')
  if (!button) throw new Error('Thinking disclosure button was not rendered')
  return button
}

function isExpanded(button: HTMLButtonElement): boolean {
  return button.querySelector('.lucide-chevron-down') !== null
}

describe('ThinkingBlock', () => {
  it('keeps a user-expanded block open as streamed content grows', async () => {
    const thinking = mountThinkingBlock('first thought')
    const button = disclosureButton(thinking.root)

    button.click()
    await nextTick()
    expect(isExpanded(button)).toBe(true)

    thinking.setContent('first thought with another token')
    await nextTick()

    expect(isExpanded(button)).toBe(true)
  })

  it('keeps a user-collapsed block closed as streamed content grows', async () => {
    const thinking = mountThinkingBlock('thought the user will close')
    const button = disclosureButton(thinking.root)

    button.click()
    await nextTick()
    button.click()
    await nextTick()
    expect(isExpanded(button)).toBe(false)

    thinking.setContent('thought the user will close with another token')
    await nextTick()

    expect(isExpanded(button)).toBe(false)
  })

  it('restores the latest streamed state after the final content remounts', async () => {
    const finalContent = 'thought that will be re-fetched after streaming'
    const messageId = 'message-refetched-after-streaming'
    const streaming = mountThinkingBlock('thought that will be re-fetched', true, messageId)
    const streamingButton = disclosureButton(streaming.root)

    streamingButton.click()
    await nextTick()
    streaming.setContent(finalContent)
    await nextTick()
    unmountThinkingBlock(streaming)

    const completed = mountThinkingBlock(finalContent, false, messageId)

    expect(isExpanded(disclosureButton(completed.root))).toBe(true)
  })

  it('does not share collapse state between different messages with identical reasoning', async () => {
    const content = 'the same thought in two different messages'
    const streaming = mountThinkingBlock(content, true, 'message-with-open-thought')
    const streamingButton = disclosureButton(streaming.root)

    streamingButton.click()
    await nextTick()

    const separateMessage = mountThinkingBlock(content, false, 'message-with-closed-thought')

    expect(isExpanded(disclosureButton(separateMessage.root))).toBe(false)
  })

  it('uses persisted server timing for a historical reasoning block', () => {
    const messageId = 'persisted-reasoning-message'
    const blockIdentity = { id: 1, type: 'reasoning' }
    markReasoningSeen(messageId, blockIdentity)
    finalizeReasoning(messageId, blockIdentity)

    const historical = mountThinkingBlock(
      'reasoning loaded from history',
      false,
      messageId,
      {
        duration_ms: 4_200,
      },
    )

    expect(disclosureButton(historical.root).textContent).toContain('Thought for 4s')
  })
})
