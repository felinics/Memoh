<script setup lang="ts">
import type { DiffSurfaceInput, EditorViewState } from 'stream-diffs'
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import type { CodeSurfaceBackground, CodeSurfaceTheme, CodeSurfaceThemeMode } from './theme'
import { codeSurfaceBackgroundCSS, codeSurfaceThemeKey } from './theme'
import { resolveFenceLanguage } from './language'
import { useDiffSurface } from './useDiffSurface'

// Editable single-file surface. Replaces the Monaco editor: stream-diffs owns
// rendering and `@pierre/diffs` owns the contenteditable edit session, so this
// component is only the attach/detach lifecycle plus the v-model bridge.
//
// No `editStateKey` is passed to `startEditing()`. With a key, `@pierre/diffs`
// prefers its retained draft over the file handed to the next session, so an
// external reload (agent write, conflict "Reload", JSON "Format") would show
// the stale draft while the host already tracks the new text — and saving would
// then overwrite the external write. Without a key the upstream editor keeps
// nothing: every rebuild reads the host's current text, and no file contents or
// undo history are retained in its module-level LRU. The price is that an edit
// session starts with empty undo history, so rebuilds are kept to the cases that
// genuinely need a new surface.
const props = withDefaults(defineProps<{
  modelValue: string
  filename?: string
  /** Explicit language id; otherwise inferred from `filename`. */
  language?: string
  readonly?: boolean
  theme?: CodeSurfaceTheme
  themeType?: CodeSurfaceThemeMode
  fontSize?: number
  lineHeight?: number
  fontFamily?: string
  background?: CodeSurfaceBackground
}>(), {
  filename: undefined,
  language: undefined,
  readonly: false,
  theme: undefined,
  themeType: 'system',
  fontSize: undefined,
  lineHeight: undefined,
  fontFamily: undefined,
  background: 'editor',
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const { host, setHost, run, sync, controller } = useDiffSurface()

// What the mounted surface holds. Comparing against it is what keeps the
// editor's own `onChange` (which emits upward) from rebuilding the surface
// underneath the caret.
let knownText = props.modelValue
let frame = 0
let focusRequested = false

function buildInput(): DiffSurfaceInput {
  const lang = resolveFenceLanguage(props.language)
  return {
    kind: 'file',
    file: {
      name: props.filename || 'untitled.txt',
      contents: props.modelValue,
      ...(lang ? { lang } : {}),
    },
    options: {
      theme: props.theme,
      themeType: props.themeType,
      disableFileHeader: true,
      overflow: 'scroll',
      unsafeCSS: codeSurfaceBackgroundCSS(props.background),
      // The block is known to be editable, so let the editor reuse the rows
      // that are already rendered instead of repainting the file on attach.
      useTokenTransformer: true,
    },
  }
}

function focusIsInside(): boolean {
  const element = host.value
  const active = document.activeElement
  return Boolean(element && active && element.contains(active))
}

async function attach(viewState?: EditorViewState, restoreFocus = false): Promise<void> {
  const surface = controller()
  if (!surface || props.readonly || surface.isEditing())
    return
  try {
    await surface.startEditing({
      onChange: (event) => {
        const text = event.file.contents
        knownText = text
        emit('update:modelValue', text)
      },
    })
    if (viewState)
      surface.setEditorViewState(viewState)
    if (restoreFocus || focusRequested)
      surface.focusEditor()
  }
  catch (error) {
    console.error('[code-editor] failed to start editing', error)
  }
  finally {
    focusRequested = false
  }
}

function rebuild(): Promise<void> {
  return run(async () => {
    const surface = controller()
    const wasEditing = surface?.isEditing() ?? false
    const viewState = wasEditing ? surface?.getEditorViewState() : undefined
    const hadFocus = wasEditing && focusIsInside()
    if (wasEditing)
      surface?.stopEditing('complete')
    await sync(buildInput())
    knownText = props.modelValue
    if (!props.readonly)
      await attach(viewState, hadFocus)
  })
}

function scheduleRebuild(): void {
  if (frame)
    return
  frame = requestAnimationFrame(() => {
    frame = 0
    void rebuild()
  })
}

const hostStyle = computed<Record<string, string | undefined>>(() => ({
  '--diffs-font-size': props.fontSize ? `${props.fontSize}px` : undefined,
  '--diffs-line-height': props.lineHeight ? `${props.lineHeight}px` : undefined,
  '--diffs-font-family': props.fontFamily,
}))

onMounted(() => {
  void rebuild()
})

onBeforeUnmount(() => {
  if (frame) {
    cancelAnimationFrame(frame)
    frame = 0
  }
})

// External text (a disk reload, a "Format" rewrite) must land in the surface.
// The editor's own keystrokes already updated `knownText`, so they never get
// here.
watch(
  () => props.modelValue,
  (value) => {
    if (value === knownText)
      return
    knownText = value
    scheduleRebuild()
  },
)

// Light/dark switches only need the theme type: `setThemeType()` updates the
// live instance and keeps the edit session (and its undo history) alive.
watch(
  () => props.themeType,
  (mode) => {
    const surface = controller()
    if (surface)
      surface.setThemeType(mode)
    else
      scheduleRebuild()
  },
)

// A different theme name, file or language needs a new surface; typography is
// carried by CSS custom properties and must NOT rebuild (it would drop the
// caret and any in-flight IME composition).
watch(
  () => [
    props.filename,
    props.language,
    props.readonly,
    codeSurfaceThemeKey(props.theme),
    props.background,
  ] as const,
  scheduleRebuild,
)

/** Focuses the edit caret, or the read-only surface so keyboard scrolling works. */
async function focus(): Promise<void> {
  focusRequested = true
  // A queued rAF rebuild would replace the surface right after focusing it, so
  // flush it first (the workspace chip does this on the tick it restores focus).
  if (frame) {
    cancelAnimationFrame(frame)
    frame = 0
    await rebuild()
  }
  else {
    await run(async () => {})
  }
  const surface = controller()
  if (surface?.isEditing()) {
    surface.focusEditor()
    focusRequested = false
    return
  }
  host.value?.focus()
}

function getText(): string {
  return controller()?.getEditingText() ?? knownText
}

defineExpose({ focus, getText, getSurface: controller })
</script>

<template>
  <div
    :ref="setHost"
    data-code-surface
    tabindex="-1"
    class="code-surface h-full w-full overflow-hidden outline-none"
    :style="hostStyle"
  />
</template>

<style scoped>
.code-surface {
  min-height: 0;
}

.code-surface :deep(.stream-diffs-shell) {
  width: 100%;
  height: 100%;
}
</style>
