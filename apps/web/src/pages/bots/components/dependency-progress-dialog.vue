<script setup lang="ts">
// Live log of one streamed dependency operation. The dialog never closes on
// its own — the user must see the final version — and it offers no cancel:
// there is no cancel API, and aborting the HTTP stream would not stop the
// script inside the workspace. Closing while running is always allowed and
// means "run in background": the caller keeps the stream in the shared store
// and the outcome lands as a toast. Failure keeps the raw log below a summary
// so it can be copied whole.
//
// Layout: every grid row of the panel is `min-w-0` so a long unbreakable
// token (an npm warning, a URL, an install path) wraps or scrolls inside its
// box instead of widening the dialog past its rung.
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
import type { DependencyOperationAction } from '@/composables/api/useWorkspaceDependencies'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'

const props = withDefaults(defineProps<{
  open: boolean
  /** Localized dependency name; the heading. */
  name: string
  /** Names the running-state subtitle ("Installing X" / "Removing X"). */
  action?: DependencyOperationAction
  lines: DependencyLogLine[]
  status: DependencyProgressStatus
  /** Localized failure summary; the raw log stays visible underneath. */
  error?: string
  /** From the `done` event. */
  resultVersion?: string
  /** First entrypoint path from the `done` event. */
  entrypoint?: string
  /** Error: show a Retry button (the caller replays the operation). */
  canRetry?: boolean
  /** Overrides the "Done" label when the caller's `done` leads somewhere ("View bot dependencies"). */
  doneLabel?: string
}>(), {
  action: 'install',
  error: '',
  resultVersion: '',
  entrypoint: '',
  canRetry: true,
  doneLabel: '',
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  retry: []
  done: []
}>()

const { t } = useI18n()
const { copyText } = useClipboard()

const running = computed(() => props.status === 'running')

// The subtitle states the phase, never a log line: scripts print long
// unbroken warnings that would otherwise become the dialog's widest row.
const subtitle = computed(() => {
  if (props.status === 'done') return t('bots.dependencies.progress.doneTitle')
  if (props.status === 'error') return t('bots.dependencies.progress.failedTitle')
  const args = { name: props.name }
  switch (props.action) {
    case 'remove':
      return t('bots.dependencies.progress.removing', args)
    case 'update':
      return t('bots.dependencies.progress.updating', args)
    case 'reinstall':
      return t('bots.dependencies.progress.reinstalling', args)
    default:
      return t('bots.dependencies.progress.installing', args)
  }
})

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

function finish() {
  emit('done')
  emit('update:open', false)
}
</script>

<template>
  <Dialog
    :open="open"
    @update:open="(value) => emit('update:open', value)"
  >
    <DialogPanel
      width="2xl"
      footer
    >
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ name }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ subtitle }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0 space-y-4">
        <div
          ref="scroller"
          role="log"
          aria-live="polite"
          class="max-h-72 min-w-0 overflow-auto rounded-lg border border-border bg-muted-soft p-3 font-mono text-caption leading-relaxed text-foreground"
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
            class="min-w-0 whitespace-pre-wrap break-all"
            :class="{ 'text-muted-foreground': line.stream !== 'stdout' }"
          >
            {{ line.data }}
          </div>
        </div>

        <Alert
          v-if="status === 'error'"
          variant="destructive"
          class="min-w-0"
        >
          <AlertTitle class="break-words">
            {{ error || t('bots.dependencies.progress.failedTitle') }}
          </AlertTitle>
          <AlertDescription>{{ t('bots.dependencies.progress.failedHint') }}</AlertDescription>
        </Alert>

        <DependencyKvList
          v-if="status === 'done'"
          :rows="resultRows"
        />
      </DialogBody>

      <DialogFooter class="min-w-0 items-center gap-2 sm:justify-between">
        <TextButton
          :disabled="lines.length === 0"
          @click="copyLog"
        >
          {{ t('common.copy') }}
        </TextButton>
        <div class="flex items-center gap-2">
          <Button
            v-if="running"
            variant="outline"
            @click="emit('update:open', false)"
          >
            {{ t('bots.dependencies.progress.runInBackground') }}
          </Button>
          <Button
            v-else-if="status === 'done'"
            @click="finish"
          >
            {{ doneLabel || t('bots.dependencies.done') }}
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
