<script setup lang="ts">
import type { DiffSurfaceInput } from 'stream-diffs'
import { computed, onMounted, watch } from 'vue'
import type { CodeSurfaceBackground, CodeSurfaceTheme, CodeSurfaceThemeMode } from './theme'
import { codeSurfaceBackgroundCSS, codeSurfaceThemeKey } from './theme'
import { useDiffSurface } from './useDiffSurface'

// Read-only unified-diff surface. The server attaches a unified diff to edit /
// write tool calls; apply_patch may attach its own `*** Begin Patch` text
// instead, which is not a diff a parser can consume — callers check
// `hasUnifiedDiffHunks` first and fall back to CodeSurface.
const props = withDefaults(defineProps<{
  patch: string
  diffStyle?: 'unified' | 'split'
  theme?: CodeSurfaceTheme
  themeType?: CodeSurfaceThemeMode
  fontSize?: number
  lineHeight?: number
  fontFamily?: string
  maxHeight?: string
  background?: CodeSurfaceBackground
}>(), {
  diffStyle: 'unified',
  theme: undefined,
  themeType: 'system',
  fontSize: undefined,
  lineHeight: undefined,
  fontFamily: undefined,
  maxHeight: undefined,
  background: 'card',
})

const { setHost, run, sync, controller } = useDiffSurface()

function buildInput(): DiffSurfaceInput {
  return {
    kind: 'patch',
    patch: props.patch,
    options: {
      theme: props.theme,
      themeType: props.themeType,
      diffStyle: props.diffStyle,
      disableFileHeader: true,
      // Match the chat code block's diff defaults: keep the +/- counts in the
      // hunk separators and collapse long unchanged runs to two context lines.
      hunkSeparators: 'line-info',
      expandUnchanged: false,
      collapsedContextThreshold: 5,
      parseDiffOptions: { context: 2 },
      overflow: 'scroll',
      unsafeCSS: codeSurfaceBackgroundCSS(props.background),
    },
    parsePatchOptions: { throwOnError: false },
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
    props.patch,
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
