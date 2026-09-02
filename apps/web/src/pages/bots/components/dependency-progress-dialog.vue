<script setup lang="ts">
// Live log of one streamed dependency operation. The dialog never closes on
// its own — the user must see the final version — and it offers no cancel:
// there is no cancel API, and aborting the HTTP stream would not stop the
// script inside the workspace. While running, closing is refused unless the
// caller opts into "run in background" (the panel keeps the stream alive and
// the row shows "View progress"); after done/error the dialog closes freely.
// Failure keeps the raw log below a summary so it can be copied whole.
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Button,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  TextButton,
  toast,
  useClipboard,
} from '@felinic/ui'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'

const props = withDefaults(defineProps<{
  open: boolean
  /** The running-state title, already localized ("Installing Codex"). */
  title: string
  lines: DependencyLogLine[]
  status: DependencyProgressStatus
  /** Localized failure summary; the raw log stays visible underneath. */
  error?: string
  /** From the `done` event. */
  resultVersion?: string
  /** First entrypoint path from the `done` event. */
  entrypoint?: string
  /** Running: allow closing (the caller keeps the stream); off → close disabled. */
  allowBackground?: boolean
  /** Error: show a Retry button (the caller replays the operation). */
  canRetry?: boolean
}>(), {
  error: '',
  resultVersion: '',
  entrypoint: '',
  allowBackground: false,
  canRetry: true,
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  retry: []
  done: []
}>()

const { t } = useI18n()
const { copyText } = useClipboard()

const running = computed(() => props.status === 'running')
const canClose = computed(() => !running.value || props.allowBackground)

const heading = computed(() => {
  if (props.status === 'done') return t('bots.dependencies.progress.doneTitle')
  if (props.status === 'error') return t('bots.dependencies.progress.failedTitle')
  return props.title
})

// Scripts report their stage on stderr (`dep_log`), so the latest stderr line
// is the honest subtitle; the UI never guesses a phase of its own.
const stageLine = computed(() => {
  for (let index = props.lines.length - 1; index >= 0; index -= 1) {
    const line = props.lines[index]
    if (line && line.stream !== 'stdout' && line.data.trim()) return line.data.trim()
  }
  return ''
})
const subtitle = computed(() => stageLine.value || t('bots.dependencies.progress.logHint'))

const resultRows = computed<DependencyKvRow[]>(() => [
  { label: t('bots.dependencies.progress.version'), value: props.resultVersion, mono: true },
  { label: t('bots.dependencies.progress.entrypoint'), value: props.entrypoint, mono: true },
])

// Follow the tail only while the user is already at the bottom; scrolling up
// to read an earlier line must not be yanked back by the next chunk.
const scroller = ref<HTMLElement | null>(null)
watch(() => props.lines.length, async () => {
  const el = scroller.value
  if (!el) return
  const stickToBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  await nextTick()
  if (stickToBottom) el.scrollTop = el.scrollHeight
})

watch(() => props.open, async (open) => {
  if (!open) return
  await nextTick()
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
})

function lineKey(line: DependencyLogLine, index: number): string | number {
  return line.id ?? index
}

async function copyLog() {
  const text = props.lines.map(line => line.data).join('\n')
  const ok = await copyText(text)
  if (ok) toast.success(t('common.copied'))
  else toast.error(t('common.copyFailed'))
}

function onOpenChange(value: boolean) {
  if (!value && !canClose.value) return
  emit('update:open', value)
}

function guardDismiss(event: Event) {
  if (!canClose.value) event.preventDefault()
}

function finish() {
  emit('done')
  emit('update:open', false)
}
</script>

<template>
  <Dialog
    :open="open"
    @update:open="onOpenChange"
  >
    <DialogPanel
      width="2xl"
      footer
      @escape-key-down="guardDismiss"
      @interact-outside="guardDismiss"
    >
      <DialogHeader>
        <DialogTitle>{{ heading }}</DialogTitle>
        <DialogDescription class="truncate">
          {{ subtitle }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="space-y-4">
        <div
          ref="scroller"
          role="log"
          aria-live="polite"
          class="max-h-72 overflow-y-auto rounded-lg border border-border bg-muted-soft p-3 font-mono text-caption leading-relaxed text-foreground"
        >
          <p
            v-if="lines.length === 0"
            class="text-muted-foreground"
          >
            {{ t('bots.dependencies.progress.preparing') }}
          </p>
          <div
            v-for="(line, index) in lines"
            :key="lineKey(line, index)"
            class="whitespace-pre-wrap break-all"
            :class="{ 'text-muted-foreground': line.stream !== 'stdout' }"
          >
            {{ line.data }}
          </div>
        </div>

        <Alert
          v-if="status === 'error'"
          variant="destructive"
        >
          <AlertTitle>{{ error || t('bots.dependencies.progress.failedTitle') }}</AlertTitle>
          <AlertDescription>{{ t('bots.dependencies.progress.failedHint') }}</AlertDescription>
        </Alert>

        <DependencyKvList
          v-if="status === 'done'"
          :rows="resultRows"
        />
      </DialogBody>

      <DialogFooter class="items-center gap-2 sm:justify-between">
        <TextButton
          :disabled="lines.length === 0"
          @click="copyLog"
        >
          {{ t('common.copy') }}
        </TextButton>
        <div class="flex items-center gap-2">
          <template v-if="running">
            <Button
              v-if="allowBackground"
              variant="outline"
              @click="emit('update:open', false)"
            >
              {{ t('bots.dependencies.progress.runInBackground') }}
            </Button>
            <Button
              v-else
              variant="outline"
              disabled
            >
              {{ t('bots.dependencies.close') }}
            </Button>
          </template>
          <Button
            v-else-if="status === 'done'"
            @click="finish"
          >
            {{ t('bots.dependencies.done') }}
          </Button>
          <template v-else>
            <Button
              variant="outline"
              @click="emit('update:open', false)"
            >
              {{ t('bots.dependencies.close') }}
            </Button>
            <Button
              v-if="canRetry"
              @click="emit('retry')"
            >
              {{ t('common.retry') }}
            </Button>
          </template>
        </div>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
