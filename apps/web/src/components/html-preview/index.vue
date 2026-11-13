<script setup lang="ts">
import { computed, onMounted, nextTick, ref, watch } from 'vue'
import { useSettingsStore } from '@/store/settings'
import { DEFAULT_CODE_FONT_SIZE_PX, DEFAULT_UI_FONT_SIZE_PX } from '@/store/settings/typography'

const props = withDefaults(defineProps<{
  content: string
  class?: string
}>(), {
  class: undefined,
})

const settings = useSettingsStore()
const themeStyle = ref('')

// Resolve the current themed defaults at runtime so the iframe's blank canvas
// inherits the app theme (foreground/background) and gets the correct
// `color-scheme` for native scrollbars / form controls.
// User-authored CSS inside the HTML still wins because it appears later in
// the document source order (equal specificity → later rule applies).
function refreshThemeStyle() {
  if (typeof document === 'undefined') return
  const cs = getComputedStyle(document.documentElement)
  const fg = cs.getPropertyValue('--color-foreground').trim()
  // Match the editor plane so the preview canvas is continuous with its tab.
  const bg = cs.getPropertyValue('--color-surface-editor').trim()
  const muted = cs.getPropertyValue('--color-muted-foreground').trim()
  const border = cs.getPropertyValue('--color-border').trim()
  const accent = cs.getPropertyValue('--color-accent').trim()
  const isDark = settings.resolvedColorMode === 'dark'
  const uiFontFamily = settings.uiFontStack
  // Preserve the preview's historical 14px baseline unless the user
  // explicitly overrides the UI font size.
  const uiFontSize = settings.uiFontSizePx === DEFAULT_UI_FONT_SIZE_PX
    ? '14px'
    : `${settings.uiFontSizePx}px`
  const codeFontFamilyRule = settings.codeFontFamily
    ? `\n  font-family: ${settings.codeFontStack};`
    : ''
  // Inline code keeps the surrounding text size; an explicit code font size
  // only applies to block-level <pre>.
  const codeFontSizeRule = settings.codeFontSizePx === DEFAULT_CODE_FONT_SIZE_PX
    ? ''
    : `\npre { font-size: ${settings.codeFontSizePx}px; }`

  themeStyle.value = `<style>
:root { color-scheme: ${isDark ? 'dark' : 'light'}; }
html, body {
  color: ${fg || 'inherit'};
  background-color: ${bg || 'transparent'};
  font-family: ${uiFontFamily};
  font-size: ${uiFontSize};
  line-height: 1.6;
}
body { margin: 16px; }
a { color: ${fg || 'inherit'}; }
hr { border-color: ${border || 'currentColor'}; }
code, pre {
  background-color: ${accent || 'transparent'};
  color: ${fg || 'inherit'};${codeFontFamilyRule}
}${codeFontSizeRule}
blockquote { color: ${muted || 'inherit'}; border-left: 3px solid ${border || 'currentColor'}; padding-left: 12px; margin-left: 0; }
::selection { background-color: ${accent || 'rgba(127,127,127,0.3)'}; }
</style>`
}

onMounted(() => refreshThemeStyle())

watch([
  () => settings.theme,
  () => settings.resolvedColorMode,
  () => settings.uiFontStack,
  () => settings.uiFontSizePx,
  () => settings.codeFontStack,
  () => settings.codeFontSizePx,
], async () => {
  await nextTick()
  refreshThemeStyle()
})

const srcdoc = computed(() => themeStyle.value + (props.content ?? ''))
</script>

<template>
  <div :class="['flex h-full w-full min-h-0 bg-surface-editor', props.class]">
    <iframe
      :srcdoc="srcdoc"
      sandbox=""
      class="h-full w-full border-0"
      title="HTML preview"
      referrerpolicy="no-referrer"
    />
  </div>
</template>
