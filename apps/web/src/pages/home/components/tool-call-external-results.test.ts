// @vitest-environment jsdom

import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import type { ToolCallBlock } from '@/store/chat-list'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/composables/useShikiHighlighter', () => ({
  extractFilename: (name: string) => name,
  splitUnifiedDiffFiles: () => [],
  useShikiHighlighter: () => ({
    html: ref(''),
    loading: ref(false),
    highlight: async () => {},
    highlightLang: async () => {},
  }),
}))

// The translated results of every external runtime, written by the Go adapter
// tests (internal/agent/toolresult/conformance).
function findGoldenDir(): string {
  let dir = resolve(process.cwd())
  for (;;) {
    const candidate = join(dir, 'internal/agent/toolresult/testdata/golden')
    if (existsSync(candidate)) return candidate
    const parent = dirname(dir)
    if (parent === dir) throw new Error('internal/agent/toolresult/testdata/golden not found above the working directory')
    dir = parent
  }
}

const goldenDir = findGoldenDir()

interface GoldenEntry {
  tool: string
  input?: unknown
  result: Record<string, unknown>
  status?: string
}

const goldens = readdirSync(goldenDir)
  .filter(name => name.endsWith('.json'))
  .map(name => ({
    runtime: name.replace(/\.json$/, ''),
    entries: JSON.parse(readFileSync(join(goldenDir, name), 'utf8')) as GoldenEntry[],
  }))

function blockFor(entry: GoldenEntry, index: number): ToolCallBlock {
  return {
    id: index,
    type: 'tool',
    name: entry.tool,
    toolName: entry.tool,
    toolCallId: `call-${index}`,
    tool_call_id: `call-${index}`,
    input: entry.input ?? {},
    result: entry.result,
    output: entry.result,
    done: true,
    running: false,
  }
}

// Whether the sample's own result carries output the panel has to show.
function hasOutput(entry: GoldenEntry): boolean {
  if (entry.status === 'failed') return true
  const r = entry.result
  switch (entry.tool) {
    case 'exec': return Boolean(r.stdout) || Boolean(r.stderr)
    case 'read': return Boolean(r.content)
    case 'web_fetch': return Boolean(r.content)
    case 'web_search': return Array.isArray(r.results) && r.results.length > 0
    case 'apply_patch': return Array.isArray(r.files) && r.files.length > 0
    default: return false
  }
}

// The fields a successful result of each native tool always carries; a result
// in another shape (an external runtime's own) lacks them.
const NATIVE_FIELDS: Record<string, string[]> = {
  exec: ['stdout', 'stderr'],
  read: ['content', 'total_lines'],
  web_fetch: ['content', 'url'],
  web_search: ['query', 'results'],
  apply_patch: ['summary', 'files'],
}

const EMPTY_MARKERS = [
  'chat.tools.detail.noOutput',
  'chat.tools.detail.noResults',
  'chat.tools.detail.noData',
  'chat.tools.detail.waitingOutput',
]

describe('external runtime tool results in the tool detail panels', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  it('has samples for the runtimes', () => {
    expect(goldens.length).toBeGreaterThanOrEqual(4)
  })

  for (const { runtime, entries } of goldens) {
    entries.forEach((entry, index) => {
      it(`${runtime}: ${entry.tool} #${index} is in the native format`, () => {
        if (entry.status === 'failed') {
          expect(entry.result.isError).toBe(true)
          return
        }
        for (const field of NATIVE_FIELDS[entry.tool] ?? []) {
          expect(entry.result, `${entry.tool} result field ${field}`).toHaveProperty(field)
        }
      })
      if (!hasOutput(entry)) return
      it(`${runtime}: ${entry.tool} #${index} shows its output`, async () => {
        const { getToolDisplay } = await import('./tool-call-registry')
        const block = blockFor(entry, index)
        const detail = getToolDisplay(block).detail
        expect(detail, `${entry.tool} has a detail panel`).toBeTruthy()

        root = document.createElement('div')
        document.body.append(root)
        app = createApp(defineComponent({ render: () => h(detail!, { block }) }))
        app.config.globalProperties.$t = (key: string) => key
        app.mount(root)
        await nextTick()

        const text = root.textContent ?? ''
        for (const marker of EMPTY_MARKERS) {
          expect(text, `${runtime} ${entry.tool} #${index} rendered an empty state`).not.toContain(marker)
        }
        expect(text.trim().length).toBeGreaterThan(0)
      })
    })
  }
})
