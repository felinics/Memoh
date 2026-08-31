// @vitest-environment jsdom

import { createApp, defineComponent, h } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ContentBlock, ThinkingBlock as ThinkingBlockType, ToolCallBlock as ToolCallBlockType } from '@/store/chat-list'

// The component tree reaches the store module graph, which builds the app-wide
// i18n instance on import and reads localStorage. This suite mounts its own
// i18n below, so the global instance is stubbed away.
vi.mock('@/i18n', () => ({
  default: { global: { t: (key: string) => key } },
  i18nRef: (key: string) => ({ value: key }),
}))
import ToolCallGroup from './tool-call-group.vue'

// Pins the ghost-"Thinking…" fix: the collapsed header is a live ticker only
// while the turn streams (`active`). A background task that outlives the turn
// must not pin the ticker — its `running` block used to keep the header saying
// "Thinking…" indefinitely on a finished turn (issue #1106).

interface MountedGroup {
  app: ReturnType<typeof createApp>
  root: HTMLDivElement
}

const mounted: MountedGroup[] = []

afterEach(() => {
  for (const item of mounted.splice(0)) {
    item.app.unmount()
    item.root.remove()
  }
})

let nextMessageId = 0

function mountGroup(items: ContentBlock[], active: boolean | undefined): HTMLDivElement {
  const messageId = `group-message-${nextMessageId++}`
  const harness = defineComponent({
    setup: () => () => h(ToolCallGroup, { items, messageId, active }),
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
          process: { thought: 'Thought', steps: '{count} steps', run: 'Ran {count} commands' },
          tools: { run: 'Run' },
        },
      },
    },
  }))
  app.mount(root)
  mounted.push({ app, root })
  return root
}

function headerText(root: HTMLElement): string {
  // The header label is the span inside the toggleable header row; the capsule
  // body holds the per-item rows, so restricting to the first button-bound
  // region would be brittle — take the first span with the tracking class.
  const spans = [...root.querySelectorAll('span')]
  const header = spans.find(span => (span.textContent ?? '').length > 0)
  return header?.textContent ?? ''
}

function bgExecTool(id: number, running: boolean): ToolCallBlockType {
  return {
    id,
    type: 'tool',
    toolCallId: `call-${id}`,
    toolName: 'exec',
    input: { command: 'xray run' },
    result: { status: 'background_started', task_id: `bg_${id}` },
    running,
    done: !running,
  } as unknown as ToolCallBlockType
}

function reasoning(id: number): ThinkingBlockType {
  return { id, type: 'reasoning', content: 'planning the tunnel' } as ThinkingBlockType
}

describe('ToolCallGroup header ticker', () => {
  it('shows the ticker label while the turn is streaming', () => {
    const root = mountGroup([bgExecTool(1, true), reasoning(2)], true)
    expect(headerText(root)).toBe('Thinking')
  })

  it('drops the ticker when the turn finished even if a background tool is still running', () => {
    const root = mountGroup([bgExecTool(1, true), reasoning(2)], false)
    // Aggregate summary, not "Thinking" — the model is done; only the spawned
    // command is alive, and its own row reports that.
    expect(headerText(root)).not.toBe('Thinking')
  })

  it('shows the aggregate summary once the turn finished and tools settled', () => {
    const root = mountGroup([bgExecTool(1, false), reasoning(2)], false)
    expect(headerText(root)).not.toBe('Thinking')
  })
})
