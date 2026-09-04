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
// 2. The two-layer adaptive header: while a segment streams, a muted phase
//    verb ("Exploring") leads the header with live bare-count details
//    ("2 file operations, 1 command"), and a "now" line below rolls the current
//    call; once the segment settles, the verb turns past tense ("Explored")
//    and diff totals appear. This guards against regressing to a bare ticker
//    of the latest call (which hid the aggregate view until settle).

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
            fragmentSeparator: ', ',
            phase: {
              browse: { ing: 'Exploring', done: 'Explored' },
              edit: { ing: 'Editing', done: 'Edited' },
              run: { ing: 'Running', done: 'Ran' },
              message: { ing: 'Messaging', done: 'Sent' },
              schedule: { ing: 'Scheduling', done: 'Scheduled' },
              media: { ing: 'Generating', done: 'Generated' },
              agent: { ing: 'Delegating', done: 'Delegated' },
              gui: { ing: 'Browsing', done: 'Browsed' },
              other: { ing: 'Working', done: 'Worked' },
            },
            frag: {
              fileOperations: '{count} file operation | {count} file operations',
              searches: '{count} searches',
              commands: '{count} commands',
              messages: '{count} messages',
              schedules: '{count} schedules',
              media: '{count} media files',
              agents: '{count} agents',
              steps: '{count} step | {count} steps',
              sites: '{count} sites',
            },
          },
          tools: {
            run: 'Run',
            read: 'Read',
            exec: 'Run',
            edit: 'Edit',
            ask_user: 'Ask user',
            pending: { generic: 'Working…' },
          },
        },
      },
    },
  }))
  app.mount(root)
  mounted.push({ app, root })
  return root
}

// The header row is the toggleable button; its whole text covers verb +
// details + diffs.
function headerText(root: HTMLElement): string {
  const button = root.querySelector('button')
  return button?.textContent ?? ''
}

function nowLine(root: HTMLElement): HTMLElement | null {
  return root.querySelector('.now-line')
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
    // Single-tool segment: header keeps the tool's own specific label; the now
    // line carries the model's current step. No phase verb stutter ("Running
    // Run xray run") and no bare ticker of the latest item.
    expect(headerText(root)).toContain('xray run')
    expect(nowLine(root)?.textContent).toContain('Thinking')
  })

  it('drops the live header when the turn finished even if a background tool is still running', () => {
    const root = mountGroup([bgExecTool(1, true), reasoning(2)], false)
    // Settled single-tool label, not "Thinking" — the model is done; only the
    // spawned command is alive, and its own row reports that.
    expect(headerText(root)).not.toBe('Thinking')
    expect(nowLine(root)).toBeNull()
  })

  it('shows the settled label once the turn finished and tools settled', () => {
    const root = mountGroup([bgExecTool(1, false), reasoning(2)], false)
    expect(headerText(root)).not.toBe('Thinking')
    expect(nowLine(root)).toBeNull()
  })
})

describe('ToolCallGroup adaptive header layers', () => {
  it('names the phase and live bare counts in the header, and rolls the current call below', () => {
    const root = mountGroup([
      toolBlock('read', { path: 'src/auth/session.ts' }),
      toolBlock('read', { path: 'src/auth/cookie.ts' }),
      toolBlock('exec', { command: 'pnpm test' }, true),
    ], true)

    const header = headerText(root)
    expect(header).toContain('Exploring')
    expect(header).toContain('2 file operations')
    expect(header).toContain('1 command')
    expect(nowLine(root)?.textContent).toContain('pnpm test')
  })

  it('settles to a past-tense verb with final counts and diff totals', () => {
    const root = mountGroup([
      toolBlock('edit', { path: 'a.ts', old_text: 'l1\nl2\nl3', new_text: 'n1\nn2\nn3\nn4\nn5' }),
      toolBlock('edit', { path: 'b.ts', old_text: 'l1\nl2', new_text: 'n1\nn2\nn3\nn4' }),
    ], false)

    const header = headerText(root)
    expect(header).toContain('Edited')
    expect(header).toContain('2 file operations')
    expect(header).toContain('+9')
    expect(header).toContain('-5')
    expect(header).not.toContain('Editing')
    expect(nowLine(root)).toBeNull()
  })

  it('keeps diff totals hidden while the segment is still streaming', () => {
    const root = mountGroup([
      toolBlock('edit', { path: 'a.ts', old_text: 'l1', new_text: 'n1\nn2' }),
      toolBlock('edit', { path: 'b.ts', old_text: 'l1', new_text: 'n1\nn2' }),
    ], true)

    const header = headerText(root)
    expect(header).toContain('Editing')
    expect(header).not.toContain('+4')
  })

  it('describes file tool calls as operations rather than unique files', () => {
    const root = mountGroup([
      toolBlock('edit', { path: 'same.ts', old_text: 'a', new_text: 'b' }),
      toolBlock('edit', { path: 'same.ts', old_text: 'b', new_text: 'c' }),
    ], false)

    const header = headerText(root)
    expect(header).toContain('2 file operations')
    expect(header).not.toContain('2 files')
  })

  it('localizes the fallback count for unclassified tools', () => {
    const root = mountGroup([
      toolBlock('write', { path: 'a.ts', content: 'a' }),
      toolBlock('ask_user', { question: 'Continue?' }, true),
    ], true)

    const header = headerText(root)
    expect(header).toContain('1 file operation')
    expect(header).not.toContain('1 file operations')
    expect(header).toContain('1 step')
    expect(header).not.toContain('1 steps')
    expect(header).not.toContain('chat.process.frag.steps')
  })

  it('keeps a lone live tool on its own specific label, no phase verb', () => {
    const root = mountGroup([
      toolBlock('read', { path: 'src/main.ts' }),
      reasoning(2),
    ], true)

    const header = headerText(root)
    expect(header).toContain('main.ts')
    expect(header).not.toContain('Exploring')
    expect(nowLine(root)?.textContent).toContain('Thinking')
  })

  it('keeps a lone settled tool on its own specific label, no verb', () => {
    const root = mountGroup([
      toolBlock('exec', { command: 'pnpm build' }),
      reasoning(2),
    ], false)

    const header = headerText(root)
    expect(header).toContain('pnpm build')
    expect(header).not.toContain('Ran')
  })

  it('shows the pending label in the now line while a tool input is still streaming in', () => {
    const root = mountGroup([
      toolBlock('read', { path: 'a.ts' }),
      toolBlock('read', { path: 'b.ts' }),
      toolBlock('exec', {}, true),
    ], true)

    expect(nowLine(root)?.textContent).toContain('Working…')
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
