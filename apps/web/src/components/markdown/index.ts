import { defineComponent, h, type Component } from 'vue'
import { setCustomComponents } from 'markstream-vue'
import MdCheckbox from './md-checkbox.vue'
import MdFootnoteReference from './md-footnote-reference.vue'
import MdFootnoteAnchor from './md-footnote-anchor.vue'
import MdText from './md-text.vue'
import MdLink from './md-link.vue'

// Custom markstream node components shared by every markdown surface (chat +
// file preview). They replace markstream's built-in glyphs with design-system
// equivalents — the library Checkbox for task markers, link-language footnote
// markers (dotted underline + up-right arrow), and a text node that splits mixed
// CJK/Latin runs into per-script spans for independent font-weight (it delegates
// back to markstream's TextNode mid-stream to keep the typewriter/fade) — without
// touching the renderer's id/scroll wiring.
const sharedComponents: Record<string, Component> = {
  checkbox: MdCheckbox,
  footnote_reference: MdFootnoteReference,
  footnote_anchor: MdFootnoteAnchor,
  text: MdText,
  // Intercept clicks on container-local (localhost) links to open the workspace
  // browser panel; delegates rendering/external links to markstream's LinkNode.
  link: MdLink,
}

const registered = new Set<string>()

// markstream resolves a code fence's component by its LANGUAGE name before
// falling back to the `code_block` key, and that lookup shares one namespace
// with node-type overrides. A ```text fence therefore resolves to the `text`
// mapping — our prose text node, which reads `node.content` (absent on
// code_block nodes) and renders nothing. Route by node type so the fence
// reaches the surface's code block component instead.
function textNodeRouter(codeBlock: Component | undefined): Component {
  if (!codeBlock) return MdText
  return defineComponent({
    name: 'MdTextRouter',
    inheritAttrs: false,
    props: { node: { type: Object, required: true } },
    setup(props, { attrs }) {
      return () =>
        h(
          (props.node as { type?: string }).type === 'code_block' ? codeBlock : MdText,
          { ...attrs, node: props.node },
        )
    },
  })
}

// Register the shared components (plus any surface-specific extras, e.g. the
// chat code block) in ONE call per `customId`, so the result is correct
// regardless of whether markstream merges or replaces a scope's mapping.
export function registerSharedMarkdownComponents(
  customId: string,
  extra?: Record<string, Component>,
): void {
  if (registered.has(customId)) return
  registered.add(customId)
  const merged = { ...sharedComponents, ...extra }
  merged.text = textNodeRouter(extra?.code_block)
  setCustomComponents(customId, merged)
}
