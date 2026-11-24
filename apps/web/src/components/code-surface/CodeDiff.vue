<script setup lang="ts">
import type { DiffSurfaceInput } from 'stream-diffs'
import { computed, onMounted, watch } from 'vue'
import type { CodeSurfaceBackground, CodeSurfaceTheme, CodeSurfaceThemeMode } from './theme'
import { codeSurfaceBackgroundCSS, codeSurfaceThemeKey } from './theme'
import { useDiffSurface } from './useDiffSurface'

// Read-only two-file comparison. Replaces Monaco's diff editor in the Compare
// view: both sides are already-final text, so the streaming API is not needed.
const props = withDefaults(defineProps<{
  original: string
  modified: string
  filename?: string
  diffStyle?: 'unified' | 'split'
  theme?: CodeSurfaceTheme
  themeType?: CodeSurfaceThemeMode
  fontSize?: number
  lineHeight?: number
  fontFamily?: string
  /** Fill the parent box (Compare view) or size to content up to `maxHeight`. */
  fill?: boolean
  maxHeight?: string
  background?: CodeSurfaceBackground
}>(), {
  filename: undefined,
  diffStyle: 'split',
  theme: undefined,
  themeType: 'system',
  fontSize: undefined,
  lineHeight: undefined,
  fontFamily: undefined,
  fill: true,
  maxHeight: undefined,
  background: 'editor',
})

const { setHost, run, sync, controller } = useDiffSurface()

function file(contents: string) {
  return { name: props.filename || 'file.txt', contents }
}

function buildInput(): DiffSurfaceInput {
  return {
    kind: 'diff',
    oldFile: file(props.original),
    newFile: file(props.modified),
    options: {
      theme: props.theme,
      themeType: props.themeType,
      diffStyle: props.diffStyle,
      disableFileHeader: true,
      // The Compare view is about exact content, so keep whitespace changes.
      expandUnchanged: true,
      overflow: 'scroll',
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
    props.original,
    props.modified,
    props.filename,
    props.diffStyle,
    codeSurfaceThemeKey(props.theme),
    props.themeType,
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
    class="code-surface"
    :class="[
      fill ? 'code-surface-fill h-full w-full overflow-hidden' : 'max-w-full overflow-auto',
    ]"
    :style="hostStyle"
  />
</template>

<style scoped>
.code-surface {
  min-height: 0;
}

.code-surface.code-surface-fill :deep(.stream-diffs-shell) {
  width: 100%;
  height: 100%;
}

.code-surface:not(.code-surface-fill) :deep(.stream-diffs-shell) {
  width: 100%;
}
</style>
