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

    <div
      v-if="patchText && shiki.loading.value"
      class="flex items-center gap-1.5 text-xs text-muted-foreground"
    >
      <LoaderCircle class="size-3 animate-spin" />
    </div>
    <!-- eslint-disable vue/no-v-html -->
    <div
      v-else-if="patchText"
      class="shiki-patch-container max-h-96 overflow-x-auto overflow-y-auto rounded-sm text-xs [&_pre]:m-0 [&_pre]:bg-transparent! [&_pre]:p-2 [&_code]:text-xs"
      v-html="shiki.html.value"
    />
    <!-- eslint-enable vue/no-v-html -->

    <EmptyRow v-if="!summary && !files.length && !patchText && !errorText">
      {{ t('chat.tools.detail.noData') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { LoaderCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { useShikiHighlighter } from '@/composables/useShikiHighlighter'
import EmptyRow from './tool-detail/empty-row.vue'

type PatchOperation = 'add' | 'modify' | 'delete'

interface PatchFile {
  operation: PatchOperation
  path: string
}

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()
const shiki = useShikiHighlighter()

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

watch(
  patchText,
  (patch) => {
    if (patch) void shiki.highlightLang(patch, 'diff')
  },
  { immediate: true },
)
</script>
