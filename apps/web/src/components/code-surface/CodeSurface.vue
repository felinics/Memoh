<script setup lang="ts">
import type { DiffSurfaceInput } from 'stream-diffs'
import { computed, onMounted, watch } from 'vue'
import type { CodeSurfaceBackground, CodeSurfaceTheme, CodeSurfaceThemeMode } from './theme'
import { codeSurfaceBackgroundCSS, codeSurfaceThemeKey } from './theme'
import { resolveFenceLanguage } from './language'
import { useDiffSurface } from './useDiffSurface'

// Read-only single-file surface. Replaces the Monaco viewer for assets, tool
// output and the appearance previews: syntax highlighting comes from
// stream-diffs, geometry and typography from the design system tokens.
const props = withDefaults(defineProps<{
  code: string
  filename?: string
  /** Explicit language id (a fenced block's info string). */
  language?: string
  theme?: CodeSurfaceTheme
  themeType?: CodeSurfaceThemeMode
  fontSize?: number
  lineHeight?: number
  fontFamily?: string
  wrap?: boolean
  lineNumbers?: boolean
  /** Show stream-diffs' own file header (name + change counts). */
  header?: boolean
  maxHeight?: string
  /**
   * Which plane the surface paints on: `card` matches the panels tool details
   * sit in, `editor` the workspace editor plane, and `theme` keeps the selected
   * theme's own background (the appearance previews).
   */
  background?: CodeSurfaceBackground
}>(), {
  filename: undefined,
  language: undefined,
  theme: undefined,
  themeType: 'system',
  fontSize: undefined,
  lineHeight: undefined,
  fontFamily: undefined,
  wrap: false,
  lineNumbers: true,
  header: false,
  maxHeight: undefined,
  background: 'card',
})

const { setHost, run, sync, controller } = useDiffSurface()

function buildInput(): DiffSurfaceInput {
  const lang = resolveFenceLanguage(props.language)
  return {
    kind: 'file',
    file: {
      name: props.filename || 'snippet.txt',
      contents: props.code,
      ...(lang ? { lang } : {}),
    },
    options: {
      theme: props.theme,
      themeType: props.themeType,
      disableFileHeader: !props.header,
      disableLineNumbers: !props.lineNumbers,
      overflow: props.wrap ? 'wrap' : 'scroll',
      unsafeCSS: codeSurfaceBackgroundCSS(props.background),
    },
  }
}

const hostStyle = computed<Record<string, string | undefined>>(() => ({
  '--diffs-font-size': props.fontSize ? `${props.fontSize}px` : undefined,
  '--diffs-line-height': props.lineHeight ? `${props.lineHeight}px` : undefined,
  '--diffs-font-family': props.fontFamily,
  'max-height': props.maxHeight,
}))

onMounted(() => {
  void run(async () => sync(buildInput()))
})

watch(
  () => [
    props.code,
    props.filename,
    props.language,
    codeSurfaceThemeKey(props.theme),
    props.themeType,
    props.wrap,
    props.lineNumbers,
    props.header,
  ] as const,
  () => {
    void run(async () => sync(buildInput()))
  },
)

defineExpose({ getSurface: controller })
</script>

<template>
  <div
    :ref="setHost"
    data-code-surface
    class="code-surface max-w-full overflow-auto"
    :style="hostStyle"
  />
</template>

<style scoped>
.code-surface {
  min-height: 0;
}

.code-surface :deep(.stream-diffs-shell) {
  width: 100%;
}
</style>
