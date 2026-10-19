// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { withLiteralSingleDollars } from './markdown-dollar'

// Two layers of assertions:
// 1. parseMarkdownToStructure (the same stream-markdown-parser@1.2.8 the app
//    resolves) with withLiteralSingleDollars applied to the md instance —
//    checks which node types the raw markdown yields. In jsdom the Vue layer
//    can materialize math_block but never math_inline (the inline math
//    component resolves via an async path that doesn't run there), so inline
//    coverage lives at the parser level.
// 2. Real MarkdownRender in jsdom with :custom-markdown-it — checks the
//    rendered DOM for block math (.math-block) and literal price text.

const storage = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  value: {
    getItem: (k: string) => storage.get(k) ?? null,
    setItem: (k: string, v: string) => void storage.set(k, String(v)),
    removeItem: (k: string) => storage.delete(k),
    clear: () => storage.clear(),
    key: (i: number) => [...storage.keys()][i] ?? null,
    get length() { return storage.size },
  },
  configurable: true,
})

async function mathNodeTypes(markdown: string): Promise<string[]> {
  const { parseMarkdownToStructure, getMarkdown } = await import('markstream-vue')
  const md = withLiteralSingleDollars(getMarkdown())
  const types: string[] = []
  const walk = (node: { type: string, children?: unknown[], cells?: unknown[], rows?: unknown[], header?: unknown }) => {
    types.push(node.type)
    for (const c of node.children ?? []) walk(c as never)
    for (const c of node.cells ?? []) walk(c as never)
    for (const r of node.rows ?? []) walk(r as never)
    if (node.header) walk(node.header as never)
  }
  for (const n of parseMarkdownToStructure(markdown, md, { final: true }) as Array<{ type: string }>) walk(n)
  return types
}

interface RenderOptions {
  final?: boolean
  customId?: string
  customMarkdownIt?: typeof withLiteralSingleDollars
}

async function renderChatMarkdown(content: string, options: RenderOptions = {}): Promise<HTMLElement> {
  const { final = true, customId = 'chat-msg', customMarkdownIt = withLiteralSingleDollars } = options
  const { default: MarkdownRender, enableKatex } = await import('markstream-vue')
  enableKatex()
  const { registerSharedMarkdownComponents } = await import('@/components/markdown')
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
        customId,
        customMarkdownIt,
        final,
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

describe('withLiteralSingleDollars through the markdown pipeline', () => {
  it('keeps \\(...\\) math while literalizing a trailing $ price pair', async () => {
    const types = await mathNodeTypes('参数 \\(\\theta\\)，GPU $3 到 $6 一小时')
    expect(types.filter(t => t === 'math_inline')).toHaveLength(1)
    const host = await renderChatMarkdown('参数 \\(\\theta\\)，GPU $3 到 $6 一小时')
    expect(host.textContent ?? '').toContain('$3 到 $6')
  })

  it('keeps \\(...\\) math before an escaped \\$ price and shows $5', async () => {
    const types = await mathNodeTypes('参数 \\(\\theta\\)，价格 \\$5')
    expect(types.filter(t => t === 'math_inline')).toHaveLength(1)
    const host = await renderChatMarkdown('参数 \\(\\theta\\)，价格 \\$5')
    const text = host.textContent ?? ''
    expect(text).toContain('$5')
    expect(text).not.toContain('\\$5')
  })

  it('renders currency ranges as literal text inside strong', async () => {
    const types = await mathNodeTypes('约 **US$3–6/月**；优质 **US$10–30/月或更高**')
    expect(types).not.toContain('math_inline')
    const host = await renderChatMarkdown('约 **US$3–6/月**；优质 **US$10–30/月或更高**')
    const strongs = [...host.querySelectorAll('strong')].map(el => el.textContent)
    expect(strongs).toContain('US$3–6/月')
    expect(strongs).toContain('US$10–30/月或更高')
    expect(host.querySelector('.math-inline, .math-block, .katex')).toBeNull()
  })

  it('renders dollar pairs and $x^2$ literally', async () => {
    const types = await mathNodeTypes('$3–$6/小时 和 $x^2$')
    expect(types).not.toContain('math_inline')
    const host = await renderChatMarkdown('$3–$6/小时 和 $x^2$')
    expect(host.querySelector('.math-inline, .math-block, .katex')).toBeNull()
    expect(host.textContent ?? '').toContain('$3–$6')
    expect(host.textContent ?? '').toContain('$x^2$')
  })

  it('keeps \\(...\\) math in tables while prices stay literal', async () => {
    const table = '| a | b |\n|---|---|\n| US$3–6 \\(\\eta\\) | $10 到 $30 |'
    const types = await mathNodeTypes(table)
    expect(types.filter(t => t === 'math_inline')).toHaveLength(1)
    const host = await renderChatMarkdown(table)
    expect(host.textContent ?? '').toContain('US$3–6')
    expect(host.textContent ?? '').toContain('$10 到 $30')
  })

  it('keeps inline code dollars untouched', async () => {
    const host = await renderChatMarkdown('代码 `echo \\$HOME`')
    expect(host.querySelector('.math-inline, .math-block, .katex')).toBeNull()
    expect(host.textContent ?? '').toContain('echo \\$HOME')
  })

  it('renders \\(\\$5\\) literally, matching the uncustomized parser', async () => {
    // stream-markdown-parser already fails to treat \(\$5\) as math (the
    // \$ inside the parens swallows the inline-math rule), so this documents
    // parity rather than success: masked and unmasked both render "($5)".
    const { parseMarkdownToStructure, getMarkdown } = await import('markstream-vue')
    const rawTypes: string[] = []
    const countMath = (node: { type: string, children?: unknown[], cells?: unknown[], rows?: unknown[], header?: unknown }) => {
      if (/math/.test(node.type)) rawTypes.push(node.type)
      for (const c of node.children ?? []) countMath(c as never)
      for (const c of node.cells ?? []) countMath(c as never)
      for (const r of node.rows ?? []) countMath(r as never)
      if (node.header) countMath(node.header as never)
    }
    for (const n of parseMarkdownToStructure('\\(\\$5\\)', getMarkdown(), { final: true }) as Array<{ type: string }>) countMath(n)
    expect(rawTypes).toHaveLength(0)
    const host = await renderChatMarkdown('\\(\\$5\\)')
    expect(host.textContent ?? '').toContain('($5)')
  })

  it('keeps $$…$$, \\[…\\] and fenced code unchanged', async () => {
    expect(await mathNodeTypes('$$a+b$$')).toContain('math_block')
    expect(await mathNodeTypes('\\[x^2\\]')).toContain('math_block')
    for (const sample of ['\\[x^2\\]', '$$a+b$$']) {
      const host = await renderChatMarkdown(sample)
      expect(host.querySelector('.math-block, .katex'), sample).not.toBeNull()
    }
    const host = await renderChatMarkdown('```bash\necho $PATH\n```')
    expect(host.textContent ?? '').toContain('echo $PATH')
    expect(host.querySelector('.math-inline, .math-block, .katex')).toBeNull()
  })

  it('does not leak the customizer into other MarkdownRender instances', async () => {
    // mount a customized renderer first, then a plain one; if markstream
    // shares the md instance globally, the second render would also mask.
    await renderChatMarkdown('价格 $3 到 $6')
    const { parseMarkdownToStructure, getMarkdown } = await import('markstream-vue')
    const fresh = getMarkdown()
    const types: string[] = []
    const walk = (node: { type: string, children?: unknown[] }) => {
      types.push(node.type)
      for (const c of node.children ?? []) walk(c as never)
    }
    for (const n of parseMarkdownToStructure('$x^2$', fresh, { final: true }) as Array<{ type: string }>) walk(n)
    expect(types).toContain('math_inline')
  })

  it('does not produce math for unclosed streaming input', async () => {
    const types = await mathNodeTypes('价格 $3 到')
    expect(types).not.toContain('math_inline')
    expect(types).not.toContain('math_block')
    const host = await renderChatMarkdown('价格 $3 到', { final: false })
    expect(host.querySelector('.math-inline, .math-block, .katex')).toBeNull()
    expect(host.textContent ?? '').toContain('$3')
  })
})
