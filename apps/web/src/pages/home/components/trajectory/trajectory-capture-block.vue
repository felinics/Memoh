<script setup lang="ts">
import { computed, nextTick, shallowRef, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, toast, useClipboard } from '@felinic/ui'
import { Check, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, Copy, Download, Search } from 'lucide-vue-next'
import type { HandlersContextTrajectoryBlock } from '@memohai/sdk'
import { captureTextPageOffsets } from '../../composables/context-trajectory-text'

const props = defineProps<{ block: HandlersContextTrajectoryBlock, label: string }>()
const { t, locale } = useI18n()
const { copyText } = useClipboard()
const expanded = shallowRef(false)
const copied = shallowRef(false)
const page = shallowRef(0)
const query = shallowRef('')
const found = shallowRef(-1)
const notFound = shallowRef(false)
const textView = useTemplateRef<HTMLElement>('text-view')
const matchView = useTemplateRef<HTMLElement>('match-view')
const content = computed(() => props.block.content ?? '')
const long = computed(() => content.value.length > 4096)
const offsets = computed(() => captureTextPageOffsets(content.value))
const pages = computed(() => offsets.value.length - 1)
const visible = computed(() => {
  const index = Math.min(page.value, pages.value - 1)
  const start = expanded.value ? offsets.value[index]! : 0
  const end = expanded.value ? offsets.value[index + 1]! : captureTextPageOffsets(content.value, 4096)[1]!
  const first = Math.max(start, found.value)
  const last = Math.min(end, found.value + query.value.length)
  if (!expanded.value || found.value < 0 || first >= last) return { before: content.value.slice(start, end), match: '', after: '' }
  return { before: content.value.slice(start, first), match: content.value.slice(first, last), after: content.value.slice(last, end) }
})
const bytes = computed(() => new Intl.NumberFormat(locale.value).format(props.block.bytes ?? 0))
watch(() => props.block, () => { expanded.value = false; copied.value = false; page.value = 0; query.value = ''; found.value = -1; notFound.value = false })
watch(query, () => { found.value = -1; notFound.value = false })
watch(page, () => { if (textView.value) textView.value.scrollTop = 0 }, { flush: 'post' })

function setPage(index: number) {
  page.value = Math.max(0, Math.min(pages.value - 1, index))
}

function changePage(event: Event) {
  if (event.target instanceof HTMLInputElement) {
    setPage(Math.trunc(Number(event.target.value) || 1) - 1)
    event.target.value = String(page.value + 1)
  }
}

async function findNext() {
  if (!query.value) return
  const start = found.value < 0 ? offsets.value[page.value]! : found.value + query.value.length
  let match = content.value.indexOf(query.value, start)
  if (match < 0) match = content.value.indexOf(query.value)
  found.value = match
  notFound.value = match < 0
  if (match < 0) return
  setPage(Math.max(0, offsets.value.findIndex(end => end > match) - 1))
  await nextTick()
  matchView.value?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
}

async function copy() {
  const block = props.block
  const ok = await copyText(content.value)
  if (props.block !== block) return
  if (ok) copied.value = true
  else toast.error(t('common.copyFailed'))
}

function download() {
  try {
    const binary = props.block.encoding === 'base64'
    const blob = binary
      ? new Blob([Uint8Array.from(atob(content.value), char => char.charCodeAt(0))])
      : new Blob([content.value], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `trajectory-${props.block.hash ?? 'content'}.${binary ? 'bin' : props.block.format === 'json' ? 'json' : 'txt'}`
    document.body.append(link)
    link.click()
    link.remove()
    setTimeout(() => URL.revokeObjectURL(url), 0)
  } catch {
    toast.error(t('chat.trajectory.captureDownloadFailed'))
  }
}
</script>

<template>
  <div class="min-w-0 space-y-1">
    <div class="flex items-center gap-1">
      <span class="min-w-0 flex-1 text-caption text-muted-foreground">
        {{ label }} · {{ $t('chat.trajectory.captureBytes', { n: bytes }) }}
      </span>
      <template v-if="block.available">
        <Button
          variant="ghost"
          size="icon-sm"
          :aria-label="$t('chat.trajectory.captureCopy')"
          data-testid="capture-copy"
          @click="copy"
        >
          <Check v-if="copied" /><Copy v-else />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          :aria-label="$t('chat.trajectory.captureDownload')"
          data-testid="capture-download"
          @click="download"
        >
          <Download />
        </Button>
      </template>
    </div>
    <p
      v-if="!block.available"
      class="text-caption text-warning"
    >
      {{ $t('chat.trajectory.captureUnavailable') }}
    </p>
    <template v-else>
      <template v-if="expanded">
        <div
          class="flex items-center gap-1"
          role="group"
          :aria-label="$t('chat.trajectory.capturePageControls')"
        >
          <Button
            variant="ghost"
            size="icon-sm"
            :disabled="page === 0"
            :aria-label="$t('chat.trajectory.captureFirstPage')"
            data-testid="capture-first-page"
            @click="setPage(0)"
          >
            <ChevronsLeft />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            :disabled="page === 0"
            :aria-label="$t('chat.trajectory.capturePreviousPage')"
            @click="setPage(page - 1)"
          >
            <ChevronLeft />
          </Button>
          <input
            type="number"
            :value="page + 1"
            min="1"
            :max="pages"
            :aria-label="$t('chat.trajectory.capturePage')"
            class="w-14 rounded-md border border-border bg-background px-1 py-1 text-center text-caption tabular-nums outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
            data-testid="capture-page"
            @change="changePage"
            @keydown.enter="changePage"
          >
          <span class="text-caption tabular-nums text-muted-foreground">/ {{ pages }}</span>
          <Button
            variant="ghost"
            size="icon-sm"
            :disabled="page === pages - 1"
            :aria-label="$t('chat.trajectory.captureNextPage')"
            data-testid="capture-next-page"
            @click="setPage(page + 1)"
          >
            <ChevronRight />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            :disabled="page === pages - 1"
            :aria-label="$t('chat.trajectory.captureLastPage')"
            data-testid="capture-last-page"
            @click="setPage(pages - 1)"
          >
            <ChevronsRight />
          </Button>
        </div>
        <div class="flex items-center gap-1">
          <input
            v-model="query"
            type="search"
            autocomplete="off"
            :spellcheck="false"
            :aria-label="$t('chat.trajectory.captureSearch')"
            :placeholder="$t('chat.trajectory.captureSearch')"
            class="min-w-0 flex-1 rounded-md border border-border bg-background px-2 py-1 text-caption outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
            data-testid="capture-search"
            @keydown.enter.prevent="findNext"
          >
          <Button
            variant="ghost"
            size="icon-sm"
            :disabled="!query"
            :aria-label="$t('chat.trajectory.captureFindNext')"
            @click="findNext"
          >
            <Search />
          </Button>
        </div>
        <p
          v-if="notFound"
          role="status"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.trajectory.captureNotFound') }}
        </p>
      </template>
      <p
        v-if="block.encoding === 'base64'"
        class="text-caption text-muted-foreground"
      >
        {{ $t('chat.trajectory.captureBase64') }}
      </p>
      <pre
        v-if="content"
        ref="text-view"
        tabindex="0"
        :aria-label="label"
        class="max-h-96 overflow-auto whitespace-pre-wrap break-all font-mono text-body"
      >{{ visible.before }}<mark
v-if="visible.match"
                                 ref="match-view"
class="bg-warning-soft text-foreground"
      >{{ visible.match }}</mark>{{ visible.after }}</pre>
      <p
        v-else
        class="text-caption text-muted-foreground"
      >
        {{ $t('chat.trajectory.captureEmpty') }}
      </p>
      <Button
        v-if="long"
        variant="ghost"
        size="sm"
        :aria-expanded="expanded"
        data-testid="capture-expand"
        @click="expanded = !expanded"
      >
        {{ $t(expanded ? 'chat.trajectory.captureCollapse' : 'chat.trajectory.captureExpand') }}
      </Button>
    </template>
  </div>
</template>
