<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useMonaco } from 'stream-monaco'
import { DiffTitleBar } from '@felinic/ui'
import { useSettingsStore } from '@/store/settings'
import { getLanguageByFilename } from '@/components/file-manager/utils'

// Side-by-side Monaco diff editor used by the Compare view inside the file
// viewer. Reuses stream-monaco's useMonaco so the theme, font, and language
// resolution match the regular editor. Both sides are read-only — diff resolves
// to a separate user action (Reload / Save anyway) outside this component.

const props = withDefaults(defineProps<{
  original: string
  modified: string
  filename?: string
  originalTitle?: string
  modifiedTitle?: string
}>(), {
  filename: undefined,
  originalTitle: undefined,
  modifiedTitle: undefined,
})

const containerRef = ref<HTMLDivElement>()
const settings = useSettingsStore()
const fontSize = computed(() => settings.codeFontSizePx)
const fontFamily = computed(() => settings.codeFontFamily ? settings.codeFontStack : undefined)

function resolveLanguage(): string {
  if (props.filename) return getLanguageByFilename(props.filename)
  return 'plaintext'
}

function resolveThemeName(): string {
  return settings.resolvedColorMode === 'dark'
    ? settings.shikiThemeDark
    : settings.shikiThemeLight
}

const {
  createDiffEditor,
  cleanupEditor,
  updateOriginal,
  updateModified,
  setTheme,
  setLanguage,
  getDiffEditorView,
} = useMonaco({
  theme: resolveThemeName(),
  themes: [settings.shikiThemeDark, settings.shikiThemeLight],
  readOnly: true,
  automaticLayout: true,
  autoScrollInitial: false,
  autoScrollOnUpdate: false,
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  fontSize: fontSize.value,
  fontFamily: fontFamily.value,
  padding: { top: 8, bottom: 8 },
})

let themeObserver: MutationObserver | null = null

onMounted(async () => {
  if (!containerRef.value) return
  await createDiffEditor(
    containerRef.value,
    props.original,
    props.modified,
    resolveLanguage(),
  )
  const view = getDiffEditorView()
  view?.updateOptions({
    fontSize: fontSize.value,
    fontFamily: fontFamily.value,
    renderSideBySide: true,
    ignoreTrimWhitespace: false,
  })

  // Re-apply theme when dark mode flips so the diff editor stays in sync with
  // the rest of the chrome.
  themeObserver = new MutationObserver(() => {
    void setTheme(resolveThemeName(), true)
  })
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['class', 'data-color-scheme'],
  })
})

onBeforeUnmount(() => {
  themeObserver?.disconnect()
  themeObserver = null
  cleanupEditor()
})

watch(() => props.original, (v) => updateOriginal(v, resolveLanguage()))
watch(() => props.modified, (v) => updateModified(v, resolveLanguage()))
watch(() => props.filename, () => setLanguage(resolveLanguage()))
watch(fontSize, (v) => getDiffEditorView()?.updateOptions({ fontSize: v }))
watch(fontFamily, (v) => getDiffEditorView()?.updateOptions({ fontFamily: v }))
watch(
  () => [settings.shikiThemeLight, settings.shikiThemeDark, settings.resolvedColorMode] as const,
  () => { void setTheme(resolveThemeName(), true) },
)
</script>

<template>
  <div class="flex h-full flex-col overflow-hidden bg-surface-editor">
    <DiffTitleBar v-if="originalTitle || modifiedTitle">
      <span class="min-w-0 flex-1 truncate">{{ originalTitle }}</span>
      <span class="text-muted-foreground/60">↔</span>
      <span class="min-w-0 flex-1 truncate text-right">{{ modifiedTitle }}</span>
    </DiffTitleBar>
    <div
      ref="containerRef"
      class="min-h-0 flex-1 overflow-hidden bg-surface-editor"
    />
  </div>
</template>

<style scoped>
/* Match the regular editor's surface alignment so the diff plane is continuous
 * with the workspace chrome. The same selectors that fix the main editor's
 * background also apply to the diff editor's two panes and gutters. */
:deep(.monaco-editor),
:deep(.monaco-diff-editor),
:deep(.monaco-editor .overflow-guard),
:deep(.monaco-editor-background),
:deep(.monaco-editor .margin) {
  background-color: var(--surface-editor) !important;
}
</style>
