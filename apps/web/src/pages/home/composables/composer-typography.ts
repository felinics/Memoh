import { Plugin } from '@milkdown/kit/prose/state'
import { Decoration, DecorationSet } from '@milkdown/kit/prose/view'
import type { Node } from '@milkdown/kit/prose/model'
import { $prose } from '@milkdown/kit/utils'
import { splitScriptRuns } from '@/utils/script-runs'

function scriptDecorations(doc: Node) {
  const decorations: Decoration[] = []
  doc.descendants((node, pos, parent) => {
    if (!node.isText || parent?.type.spec.code || node.marks.some(mark => mark.type.spec.code)) return
    let offset = pos
    for (const run of splitScriptRuns(node.text ?? '')) {
      decorations.push(Decoration.inline(offset, offset + run.text.length, {
        class: run.script === 'cjk' ? 'chat-cjk' : 'chat-latin',
      }))
      offset += run.text.length
    }
  })
  return DecorationSet.create(doc, decorations)
}

// Use ProseMirror decorations, not DOM rewriting: script styling must preserve
// editor positions, selection, composition and Markdown serialization.
export const composerTypography = $prose(() => new Plugin({
  state: {
    init: (_, state) => scriptDecorations(state.doc),
    apply: (transaction, previous) => transaction.docChanged ? scriptDecorations(transaction.doc) : previous,
  },
  props: {
    decorations(state) { return this.getState(state) },
  },
}))
