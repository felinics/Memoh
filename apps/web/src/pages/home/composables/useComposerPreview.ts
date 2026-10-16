import { nextTick, ref, watch, type Ref } from 'vue'

/**
 * A prompt shown in the empty composer as a faint preview. `landing` turns
 * the preview into the text color just before the real prefill replaces it.
 */
export interface ComposerPreview {
  text: string
  landing: boolean
}

/** How long the preview inks before the prefill lands. Matches the color transition on [data-composer-preview] in style.css. */
export const COMPOSER_PREVIEW_LANDING_MS = 170

/**
 * Where the preview layer sits: exactly over the textarea's box inside the
 * composer, so the preview wraps line for line like the text it becomes. The
 * layer is a sibling of the textarea (the composer's `> textarea` focus
 * selectors need it to stay a direct child), so its box is measured from the
 * textarea each time the preview appears; nothing in the composer moves
 * while it is empty.
 */
export function useComposerPreviewBox(textarea: Ref<HTMLTextAreaElement | null | undefined>, shown: () => boolean) {
  const box = ref<{ top: string, left: string, width: string } | null>(null)
  watch(shown, async (value) => {
    if (!value) return
    await nextTick()
    const el = textarea.value
    box.value = el ? { top: `${el.offsetTop}px`, left: `${el.offsetLeft}px`, width: `${el.offsetWidth}px` } : null
  }, { immediate: true })
  return box
}
