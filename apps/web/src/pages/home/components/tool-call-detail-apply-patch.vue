<template>
  <div class="space-y-2 text-xs leading-relaxed">
    <div
      v-if="summary"
      class="rounded-sm border border-border bg-muted/30 px-2 py-1 font-mono whitespace-pre-wrap break-all text-muted-foreground"
    >
      {{ summary }}
    </div>

    <div
      v-if="files.length"
      class="space-y-1"
    >
      <div
        v-for="file in files"
        :key="`${file.operation}:${file.path}`"
        class="flex items-center gap-2 font-mono"
      >
        <span
          class="inline-flex h-4 min-w-4 shrink-0 items-center justify-center rounded-sm border px-1 text-caption font-semibold leading-none"
          :class="operationClass(file.operation)"
        >
          {{ operationLabel(file.operation) }}
        </span>
        <span
          class="min-w-0 truncate text-foreground"
          :title="file.path"
        >{{ file.path }}</span>
      </div>
    </div>

    <div
      v-if="errorText"
      class="font-mono whitespace-pre-wrap break-all text-destructive"
    >
      {{ errorText }}
    </div>

    <!-- A unified diff gets one real diff surface per file (a patch surface
         renders a single file's hunks); apply_patch's own `*** Begin Patch`
         text is not parseable as one, so it stays a diff-highlighted code
         block. -->
    <template v-if="unifiedFiles.length">
      <PatchSurface
        v-for="(file, index) in unifiedFiles"
        :key="`${index}:${file.oldPath}:${file.newPath}`"
        :patch="file.diff"
        :theme="theme"
        :theme-type="themeType"
        :font-size="fontSize"
        :line-height="lineHeight"
        :font-family="fontFamily"
        max-height="24rem"
      />
    </template>
    <CodeSurface
      v-else-if="patchText"
      :code="patchText"
      language="diff"
      :theme="theme"
      :theme-type="themeType"
      :font-size="fontSize"
      :line-height="lineHeight"
      :font-family="fontFamily"
      max-height="24rem"
    />

    <EmptyRow v-if="!summary && !files.length && !patchText && !errorText">
      {{ t('chat.tools.detail.noData') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { hasUnifiedDiffHunks, splitUnifiedDiffFiles } from '@/utils/unified-diff'
import { useCodeSurfaceAppearance } from '@/components/code-surface/theme'
import CodeSurface from '@/components/code-surface/CodeSurface.vue'
import PatchSurface from '@/components/code-surface/PatchSurface.vue'
import EmptyRow from './tool-detail/empty-row.vue'

type PatchOperation = 'add' | 'modify' | 'delete'

interface PatchFile {
  operation: PatchOperation
  path: string
}

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()
const { theme, themeType, fontSize, lineHeight, fontFamily } = useCodeSurfaceAppearance()

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : {}
}

function resultObject(): Record<string, unknown> {
  const result = asObject(props.block.result)
  const sc = asObject(result.structuredContent)
  return Object.keys(sc).length > 0 ? sc : result
}

const patchText = computed(() => {
  const input = asObject(props.block.input)
  if (typeof input.patch === 'string') return input.patch
  const changes = Array.isArray(input.changes) ? input.changes : resultObject().changes
  if (!Array.isArray(changes)) return ''
  return changes
    .map(item => asObject(item).diff)
    .filter((diff): diff is string => typeof diff === 'string' && diff.length > 0)
    .join('\n')
})

// apply_patch's own input is `*** Begin Patch` text, which no diff parser can
// consume; the server also attaches a real unified diff for this tool call
// (`block.diff`), and that is what the surfaces below should show.
const diffSource = computed(() => {
  const serverDiff = props.block.diff ?? ''
  if (serverDiff && hasUnifiedDiffHunks(serverDiff)) return serverDiff
  return hasUnifiedDiffHunks(patchText.value) ? patchText.value : ''
})
// One patch surface per file: a patch surface renders a single file's hunks,
// and its default 0/0 indices would hide the rest of a multi-file diff.
const unifiedFiles = computed(() => (diffSource.value ? splitUnifiedDiffFiles(diffSource.value) : []))

const summary = computed(() => {
  const r = resultObject()
  return typeof r.summary === 'string' ? r.summary : ''
})

const errorText = computed(() => {
  const result = asObject(props.block.result)
  if (result.isError !== true) return ''
  const content = result.content
  if (!Array.isArray(content)) return ''
  return (content as Array<Record<string, unknown>>)
    .filter(item => item.type === 'text')
    .map(item => item.text)
    .filter((text): text is string => typeof text === 'string' && text.length > 0)
    .join('\n')
})

const files = computed<PatchFile[]>(() => {
  const fromResult = filesFromResult(resultObject())
  if (fromResult.length > 0) return fromResult
  return filesFromPatch(patchText.value)
})

function filesFromResult(result: Record<string, unknown>): PatchFile[] {
  if (Array.isArray(result.changes)) {
    const changes = filesFromChanges(result.changes)
    if (changes.length > 0) return changes
  }
  const rawFiles = result.files
  if (Array.isArray(rawFiles)) {
    return rawFiles
      .map((item) => {
        const obj = asObject(item)
        const path = typeof obj.path === 'string' ? obj.path : ''
        const operation = normalizeOperation(obj.operation)
        return path && operation ? { operation, path } : null
      })
      .filter((item): item is PatchFile => Boolean(item))
  }

  const out: PatchFile[] = []
  for (const [key, operation] of [
    ['added', 'add'],
    ['modified', 'modify'],
    ['deleted', 'delete'],
  ] as const) {
    const paths = result[key]
    if (!Array.isArray(paths)) continue
    for (const path of paths) {
      if (typeof path === 'string' && path) out.push({ operation, path })
    }
  }
  return out
}

function filesFromChanges(changes: unknown[]): PatchFile[] {
  return changes
    .map((item) => {
      const obj = asObject(item)
      const path = typeof obj.path === 'string' ? obj.path : ''
      const kind = asObject(obj.kind)
      const operation = normalizeOperation(kind.type ?? obj.kind ?? obj.operation)
      return path && operation ? { operation, path } : null
    })
    .filter((item): item is PatchFile => Boolean(item))
}

function filesFromPatch(patch: string): PatchFile[] {
  if (!patch) return []
  const out: PatchFile[] = []
  for (const rawLine of patch.split('\n')) {
    const line = rawLine.trim()
    if (line.startsWith('*** Add File: ')) {
      const path = line.slice('*** Add File: '.length).trim()
      if (path) out.push({ operation: 'add', path })
    }
    else if (line.startsWith('*** Delete File: ')) {
      const path = line.slice('*** Delete File: '.length).trim()
      if (path) out.push({ operation: 'delete', path })
    }
    else if (line.startsWith('*** Update File: ')) {
      const path = line.slice('*** Update File: '.length).trim()
      if (path) out.push({ operation: 'modify', path })
    }
  }
  return out
}

function normalizeOperation(value: unknown): PatchOperation | '' {
  if (value === 'add' || value === 'added') return 'add'
  if (value === 'modify' || value === 'modified' || value === 'update') return 'modify'
  if (value === 'delete' || value === 'deleted') return 'delete'
  return ''
}

function operationLabel(operation: PatchOperation): string {
  if (operation === 'add') return 'A'
  if (operation === 'delete') return 'D'
  return 'M'
}

function operationClass(operation: PatchOperation): string {
  if (operation === 'add') return 'border-success/30 bg-success/10 text-success-foreground'
  if (operation === 'delete') return 'border-destructive/30 bg-destructive/10 text-destructive'
  return 'border-warning/30 bg-warning/10 text-warning-foreground'
}
</script>
