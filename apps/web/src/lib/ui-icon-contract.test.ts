import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { compileTemplate } from 'vue/compiler-sfc'
import { Plus } from 'lucide-vue-next'
import * as icons from '@memohai/icon/ui'

const { uiIconStrokeWidth, ...glyphs } = icons

describe('shared UI icon contract', () => {
  it('uses one default stroke and forwards SVG attributes for every UI glyph', async () => {
    for (const icon of Object.values(glyphs)) {
      const svg = await renderToString(createSSRApp(() => h(icon, {
        size: 16,
        class: 'contract-icon',
        'aria-hidden': 'true',
      })))
      expect(svg).toContain(`stroke-width="${uiIconStrokeWidth}"`)
      expect(svg).toContain('stroke="currentColor"')
      expect(svg).toContain('width="16"')
      expect(svg).toContain('contract-icon')
      expect(svg).toContain('aria-hidden="true"')
    }
  })

  it('keeps icons imported straight from lucide on the shared stroke', async () => {
    // lucide-vue-next 0.562 has no app-wide default, so icons imported from it
    // directly render lucide's built-in stroke. They match the icon layer only
    // while that built-in equals uiIconStrokeWidth.
    const svg = await renderToString(createSSRApp(() => h(Plus)))
    expect(svg).toContain(`stroke-width="${uiIconStrokeWidth}"`)
  })

  it('keeps composer and panel action icon styling out of call sites', () => {
    const files = [
      '../pages/home/components/chat-pane.vue',
      '../pages/home/components/composer-continue-on.vue',
      '../pages/home/components/dockview/header-add-actions.vue',
      '../pages/home/components/dockview/prefix-header-actions.vue',
    ]
    const names = new Set(Object.keys(glyphs))
    for (const file of files) {
      const source = readFileSync(new URL(file, import.meta.url), 'utf8')
      const template = source.slice(source.indexOf('<template>') + 10, source.lastIndexOf('</template>'))
      const result = compileTemplate({
        source: template,
        filename: file,
        id: file,
        compilerOptions: { nodeTransforms: [node => {
          if (node.type !== 1) return
          if (node.tag === 'Button' && /<(?:AddIcon|ComputerIcon|BackIcon|ForwardIcon|SidebarOpenIcon|SidebarCloseIcon)\b/.test(node.loc.source)) {
            // Toolbar actions are ghost + muted tone; the session controls under the
            // input are quiet text triggers, whose variant already starts muted.
            const quiet = node.props.some(prop => prop.type === 6 && prop.name === 'variant' && prop.value?.content === 'quiet')
            const muted = node.props.some(prop => prop.type === 6 && prop.name === 'tone' && prop.value?.content === 'muted')
            expect(quiet || muted).toBe(true)
            const colorClasses = node.props.filter(prop => prop.type === 6 && prop.name === 'class')
            for (const prop of colorClasses) {
              if (prop.type === 6) expect(prop.value?.content ?? '').not.toMatch(/(?:^|[\s:])(?:text-|opacity-|stroke-)/)
            }
          }
          if (!names.has(node.tag)) return
          for (const prop of node.props) {
            const name = prop.type === 6 ? prop.name : prop.arg?.type === 4 ? prop.arg.content : ''
            expect(['stroke-width', 'strokeWidth', 'color', 'opacity', 'style']).not.toContain(name)
            if (prop.type === 6 && prop.name === 'class') {
              expect(prop.value?.content ?? '').not.toMatch(/(?:^|\s)(?:text-|opacity-|stroke-|rotate-|translate-)/)
            }
          }
        }] },
      })
      expect(result.errors).toEqual([])
    }
  })
})
