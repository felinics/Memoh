<script setup lang="ts">
import { DialogBody } from '@felinic/ui'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { CrepeBuilder } from '@milkdown/crepe/builder'
import { placeholder as placeholderFeature, placeholderConfig } from '@milkdown/crepe/feature/placeholder'
import { editorViewCtx, serializerCtx } from '@milkdown/kit/core'
import { composerTypography } from '../composables/composer-typography'
import { Plugin } from '@milkdown/kit/prose/state'
import { $prose, replaceAll } from '@milkdown/kit/utils'
import '@milkdown/crepe/theme/common/prosemirror.css'
import '@milkdown/crepe/theme/common/placeholder.css'

const props = defineProps<{ modelValue: string, disabled: boolean, placeholder: string }>()
const emit = defineEmits<{
  'update:modelValue': [value: string]
  keydown: [event: KeyboardEvent]
  paste: [event: ClipboardEvent]
}>()
const root = ref<HTMLElement | null>(null)
let crepe: CrepeBuilder | undefined
let ready = false
let disposed = false
let published = props.modelValue

function focus(options?: FocusOptions) {
  if (ready && !props.disabled) crepe?.editor.action(ctx => ctx.get(editorViewCtx).dom.focus(options))
}
function insertText(text: string) {
  if (ready && !props.disabled) crepe?.editor.action(ctx => {
    const view = ctx.get(editorViewCtx)
    view.dispatch(view.state.tr.insertText(text).scrollIntoView())
  })
}
// Let chat attachment handling run before Milkdown sees file drops.
function onDrop(event: DragEvent) {
  if (event.dataTransfer?.files.length) event.preventDefault()
}
function paste(event: ClipboardEvent) {
  if (ready && !props.disabled) crepe?.editor.action(ctx => {
    ctx.get(editorViewCtx).pasteText(event.clipboardData?.getData('text/plain') ?? '')
  })
}
defineExpose({
  get element() { return root.value },
  get disabled() { return props.disabled || !ready },
  focus, insertText, paste,
})

onMounted(async () => {
  crepe = new CrepeBuilder({ root: root.value, defaultValue: props.modelValue })
    .addFeature(placeholderFeature, { text: props.placeholder, mode: 'doc' })
  // Publish synchronously: a send click must see the last edit, without waiting
  // for Milkdown's debounced markdownUpdated listener.
  crepe.editor.use(composerTypography)
  crepe.editor.use($prose(ctx => new Plugin({
    view: () => ({ update(view, previous) {
      if (view.state.doc.eq(previous.doc)) return
      published = ctx.get(serializerCtx)(view.state.doc).replace(/\n$/, '')
      emit('update:modelValue', published)
    } }),
    props: {
      attributes: { role: 'textbox', 'aria-multiline': 'true' },
    },
  })))
  await crepe.create()
  if (disposed) { await crepe.destroy(); return }
  ready = true
  crepe.setReadonly(props.disabled)
  if (props.modelValue !== published) crepe.editor.action(replaceAll(props.modelValue, true))
  published = props.modelValue
})
watch(() => props.modelValue, value => {
  if (!ready || value === published) return
  published = value
  // External replacement means a draft switch, send/reset, or voice insertion.
  // Clear history so undo cannot resurrect a sent or another session's draft.
  crepe?.editor.action(replaceAll(value, true))
})
watch(() => props.disabled, value => { if (ready) crepe?.setReadonly(value) })
watch(() => props.placeholder, text => {
  if (ready) crepe?.editor.action(ctx => ctx.update(placeholderConfig.key, value => ({ ...value, text })))
})
onBeforeUnmount(() => {
  disposed = true
  if (ready) { ready = false; void crepe?.destroy() }
})
</script>

<template>
  <DialogBody class="composer-scroll-fade order-none mr-0! max-h-52 w-full basis-full pr-0!">
    <div
      ref="root"
      data-chat-content
      class="markstream-vue composer-markdown-input break-words bg-transparent pl-2 pr-1 pt-2 pb-1.5 text-base text-foreground"
      :aria-label="placeholder"
      :aria-disabled="disabled"
      @keydown.capture="emit('keydown', $event)"
      @paste.capture="emit('paste', $event)"
      @drop.capture="onDrop"
    />
  </DialogBody>
</template>
