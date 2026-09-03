// @vitest-environment jsdom

import { createApp, defineComponent, h, nextTick } from 'vue'
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

// Two contracts pinned here:
//
// 1. Ghost-"Thinking…" fix (issue #1106): the collapsed header is a live
//    two-layer summary only while the turn streams (`active`). A background
//    task that outlives the turn must not pin the live header — its `running`
//    block used to keep the header saying "Thinking…" indefinitely on a
//    finished turn.
//
// 2. The two-layer adaptive header: while a segment streams, the header names
//    the phase + live tally and a "now" line below rolls the current call;
//    once the segment settles, the header shows the final tally plus the diff
//    totals. This guards against regressing to a bare ticker of the latest
//    call (which hid the aggregate view until the segment settled).

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

let nextBlockId = 0
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
          process: {
            thought: 'Thought',
            steps: '{count} steps',
            browse: 'Read {count} files',
            edit: 'Edited {count} files',
            run: 'Ran {count} commands',
            phase: {
              browse: 'Exploring',
              edit: 'Editing',
              run: 'Running',
              message: 'Messaging',
              schedule: 'Scheduling',
              media: 'Generating',
              agent: 'Delegating',
              gui: 'Browsing',
              other: 'Working',
            },
          },
          tools: { run: 'Run', read: 'Read', exec: 'Run', edit: 'Edit' },
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

function nowLine(root: HTMLElement): HTMLElement | null {
  return root.querySelector('.live-peek-line')
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

function toolBlock(toolName: string, input: unknown, running = false): ToolCallBlockType {
  const id = ++nextBlockId
  return {
    id,
    type: 'tool',
    name: toolName,
    toolName,
    input,
    running,
    done: !running,
    tool_call_id: `tc-${id}`,
    toolCallId: `tc-${id}`,
    result: null,
  }
}

function reasoning(id: number): ThinkingBlockType {
  return { id, type: 'reasoning', content: 'planning the tunnel' } as ThinkingBlockType
}

describe('ToolCallGroup live header gating (#1106)', () => {
  it('keeps the live two-layer header while the turn is streaming', () => {
    const root = mountGroup([bgExecTool(1, true), reasoning(2)], true)
    // Header = the segment's phase (a command run), now line = the model's
    // current step — the layering the old ticker-only header couldn't express.
    expect(headerText(root)).toBe('Running')
    expect(nowLine(root)?.textContent).toContain('Thinking')
  })

  it('drops the live header when the turn finished even if a background tool is still running', () => {
    const root = mountGroup([bgExecTool(1, true), reasoning(2)], false)
    // Aggregate summary, not "Thinking" — the model is done; only the spawned
    // command is alive, and its own row reports that.
    expect(headerText(root)).not.toBe('Thinking')
    expect(nowLine(root)).toBeNull()
  })

  it('shows the aggregate summary once the turn finished and tools settled', () => {
    const root = mountGroup([bgExecTool(1, false), reasoning(2)], false)
    expect(headerText(root)).not.toBe('Thinking')
    expect(nowLine(root)).toBeNull()
  })
})

describe('ToolCallGroup adaptive header layers', () => {
  it('names the phase and live tally in the header while streaming, and rolls the current call below', () => {
    const root = mountGroup([
      toolBlock('read', { path: 'src/auth/session.ts' }),
      toolBlock('read', { path: 'src/auth/cookie.ts' }),
      toolBlock('exec', { command: 'pnpm test' }, true),
    ], true)

    expect(headerText(root)).toContain('Exploring')
    expect(headerText(root)).toContain('Read 2 files')
    expect(headerText(root)).toContain('Ran 1 command')
    expect(nowLine(root)?.textContent).toContain('pnpm test')
  })

  it('settles to the final tally with diff totals and no now line', () => {
    const root = mountGroup([
      toolBlock('edit', { path: 'a.ts', old_text: 'l1\nl2\nl3', new_text: 'n1\nn2\nn3\nn4\nn5' }),
      toolBlock('edit', { path: 'b.ts', old_text: 'l1\nl2', new_text: 'n1\nn2\nn3\nn4' }),
    ], false)

    const header = headerText(root)
    expect(header).toContain('Edited 2 files')
    expect(header).not.toContain('Editing')
    expect(nowLine(root)).toBeNull()
    // Diff totals are separate mono spans next to the label, not part of the
    // label text — assert them on the header row as a whole.
    const row = root.querySelector('button')
    expect(row?.textContent).toContain('+9')
    expect(row?.textContent).toContain('-5')
  })

  it('keeps diff totals hidden while the segment is still streaming', () => {
    const root = mountGroup([
      toolBlock('edit', { path: 'a.ts', old_text: 'l1', new_text: 'n1\nn2' }),
      toolBlock('edit', { path: 'b.ts', old_text: 'l1', new_text: 'n1\nn2' }),
    ], true)

    const row = root.querySelector('button')
    expect(headerText(root)).toContain('Editing')
    expect(row?.textContent).not.toContain('+4')
  })

  it('hides the now line once the user opens the body', async () => {
    const root = mountGroup([
      toolBlock('read', { path: 'a.ts' }),
      toolBlock('read', { path: 'b.ts' }),
      toolBlock('exec', { command: 'ls' }, true),
    ], true)
    expect(nowLine(root)).not.toBeNull()

    root.querySelector('button')?.click()
    await nextTick()

    expect(nowLine(root)).toBeNull()
  })
})
