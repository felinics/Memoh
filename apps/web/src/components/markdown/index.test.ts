// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia } from 'pinia'

// Regression: markstream resolves a code fence's component by its LANGUAGE
// name before the `code_block` key, in the same namespace as node-type
// overrides — so a ```text fence used to hit the prose `text` component and
// render nothing (no `content` on code_block nodes). The registered text
// router must send code_block nodes to the surface's code block component
// while plain prose keeps the per-script span split.

const storage = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  value: {
    getItem: (k: string) => storage.get(k) ?? null,
    setItem: (k: string, v: string) => void storage.set(k, String(v)),
    removeItem: (k: string) => void storage.delete(k),
    clear: () => storage.clear(),
    key: (i: number) => [...storage.keys()][i] ?? null,
    get length() { return storage.size },
  },
  configurable: true,
})

async function renderChatMarkdown(content: string): Promise<HTMLElement> {
  const { default: MarkdownRender } = await import('markstream-vue')
  const { disableInlineMath, registerSharedMarkdownComponents } = await import('@/components/markdown')
  const { default: ChatCodeBlock } = await import('@/pages/home/components/chat-code-block.vue')
  const { createI18n } = await import('vue-i18n')
  registerSharedMarkdownComponents('chat-msg', { code_block: ChatCodeBlock, shell: ChatCodeBlock })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp({
    render() {
      return h(MarkdownRender, {
        content,
        isDark: false,
        smoothStreaming: false,
        typewriter: false,
        fade: false,
        showTooltips: false,
        customMarkdownIt: disableInlineMath,
        customId: 'chat-msg',
      })
    },
  })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en: {} } }))
  app.use(createPinia())
  app.mount(host)
  await nextTick()
  await new Promise((r) => setTimeout(r, 300))
  await nextTick()
  return host
}

describe('registerSharedMarkdownComponents', () => {
  it('renders a ```text fence through the code block component', async () => {
    const host = await renderChatMarkdown('不是空白,输出的是:\n\n```text\n192.0.2.1\n```\n')
    expect(host.textContent ?? '').toContain('192.0.2.1')
    expect(host.querySelector('.chat-code-block')).not.toBeNull()
  })

  it('keeps prose text on the per-script span split', async () => {
    const host = await renderChatMarkdown('中文 mixed with Latin')
    expect(host.querySelector('.chat-cjk')?.textContent).toContain('中文')
    expect(host.querySelector('.chat-latin')?.textContent).toContain('Latin')
  })

  it('leaves paired dollar amounts as literal text', async () => {
    const host = await renderChatMarkdown('价格是 $5 和 $15')
    expect(host.textContent ?? '').toContain('$5 和 $15')
    expect(host.querySelector('.katex')).toBeNull()
  })
})

describe('disableInlineMath', () => {
  it('stops $...$ pairing without dropping block $$ math', async () => {
    const { getMarkdown, parseMarkdownToStructure } = await import('markstream-vue')
    const { disableInlineMath } = await import('@/components/markdown')

    const md = disableInlineMath(getMarkdown())
    const inline = parseMarkdownToStructure('价格是 $5 和 $15', md, { final: true })
    expect(nodeTypes(inline)).not.toContain('math_inline')
    expect(collectText(inline)).toContain('$5 和 $15')

    const block = parseMarkdownToStructure('$$\nE=mc^2\n$$', md, { final: true })
    expect(nodeTypes(block)).toContain('math_block')
  })
})

function nodeTypes(value: unknown): string[] {
  const out: string[] = []
  const walk = (node: unknown) => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (!node || typeof node !== 'object') return
    const record = node as { type?: string, children?: unknown, items?: unknown }
    if (typeof record.type === 'string') out.push(record.type)
    walk(record.children)
    walk(record.items)
  }
  walk(value)
  return out
}

function collectText(value: unknown): string {
  const parts: string[] = []
  const walk = (node: unknown) => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (!node || typeof node !== 'object') return
    const record = node as { content?: unknown, children?: unknown, items?: unknown }
    if (typeof record.content === 'string') parts.push(record.content)
    walk(record.children)
    walk(record.items)
  }
  walk(value)
  return parts.join('')
}
