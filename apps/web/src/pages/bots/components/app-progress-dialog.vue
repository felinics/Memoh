<script setup lang="ts">
// Live progress of one streamed App operation: the component steps the
// Server walks through (dependencies, Skills, connectors) above the raw script
// log. Closing while running means "run in background"; the store keeps the
// stream and the verdict lands as a toast.
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
  Button,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  Spinner,
  TextButton,
  toast,
  useClipboard,
} from '@felinic/ui'
import { AlertTriangle, Check, CircleDashed, Link2, KeyRound } from 'lucide-vue-next'
import type { AppOperationAction } from '@/composables/api/useAppStream'
import type { AppOperationStep } from '@/store/app-operations'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'

const props = withDefaults(defineProps<{
  open: boolean
  name: string
  action?: AppOperationAction
  steps: AppOperationStep[]
  lines: DependencyLogLine[]
  status: DependencyProgressStatus
  /** App status reported by `done` (installed, partial, removed). */
  result?: string
  error?: string
  canRetry?: boolean
  doneLabel?: string
}>(), {
  action: 'install',
  result: '',
  error: '',
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

const subtitle = computed(() => {
  if (props.status === 'done') {
    return props.result === 'partial' ? t('apps.progress.partialTitle') : t('apps.progress.doneTitle')
  }
  if (props.status === 'error') return t('apps.progress.failedTitle')
  if (props.status === 'unknown') return t('apps.progress.unknownTitle')
  const args = { name: props.name }
  switch (props.action) {
    case 'remove':
      return t('apps.progress.removing', args)
    case 'update':
      return t('apps.progress.updating', args)
    case 'resume':
      return t('apps.progress.resuming', args)
    default:
      return t('apps.progress.installing', args)
  }
})

function stepLabel(step: AppOperationStep): string {
  switch (step.kind) {
    case 'skills':
      return t('apps.steps.skills')
    case 'connector':
      return t('apps.steps.connector', { name: step.id })
    case 'dependency':
      return t('apps.steps.dependency', { name: step.id })
    default:
      return step.id
  }
}

function stepStatusLabel(step: AppOperationStep): string {
  const key = `apps.stepStatus.${step.status}`
  const text = t(key)
  return text === key ? step.status : text
}

function stepVariant(step: AppOperationStep): 'secondary' | 'success' | 'warning' | 'destructive' | 'outline' {
  switch (step.status) {
    case 'running':
      return 'secondary'
    case 'failed':
      return 'destructive'
    case 'needs_auth':
      return 'warning'
    case 'installed':
    case 'linked':
    case 'removed':
    case 'disconnected':
      return 'success'
    default:
      return 'outline'
  }
}

function stepIcon(step: AppOperationStep) {
  switch (step.status) {
    case 'running':
      return Spinner
    case 'failed':
      return AlertTriangle
    case 'needs_auth':
      return KeyRound
    case 'linked':
      return Link2
    case 'kept':
    case 'skipped':
      return CircleDashed
    default:
      return Check
  }
}

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

async function copyLog() {
  const ok = await copyText(props.lines.map(line => line.data).join('\n'))
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
        <DialogDescription
          class="break-words"
          role="status"
          aria-live="polite"
          aria-atomic="true"
        >
          {{ subtitle }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0 space-y-4">
        <ul
          v-if="steps.length"
          class="space-y-1.5"
        >
          <li
            v-for="step in steps"
            :key="`${step.kind}/${step.id}`"
            class="flex min-w-0 items-center gap-2 text-body"
          >
            <component
              :is="stepIcon(step)"
              class="size-4 shrink-0 text-muted-foreground"
            />
            <span class="min-w-0 flex-1 truncate">{{ stepLabel(step) }}</span>
            <span
              v-if="step.version && step.kind !== 'skills'"
              class="font-mono text-caption text-muted-foreground"
            >{{ step.version }}</span>
            <Badge
              :variant="stepVariant(step)"
              size="sm"
            >
              {{ stepStatusLabel(step) }}
            </Badge>
          </li>
        </ul>

        <div
          ref="scroller"
          role="region"
          :aria-label="t('apps.progress.log')"
          tabindex="0"
          class="max-h-60 min-w-0 overflow-auto rounded-lg border border-border bg-muted-soft p-3 font-mono text-caption leading-relaxed text-foreground"
        >
          <p
            v-if="lines.length === 0"
            class="text-muted-foreground"
          >
            {{ running ? t('apps.progress.preparing') : t('apps.progress.noLog') }}
          </p>
          <div
            v-for="(line, index) in lines"
            :key="line.id ?? index"
            class="min-w-0 whitespace-pre-wrap break-all"
            :class="{ 'text-muted-foreground': line.stream !== 'stdout' }"
          >
            {{ line.data }}
          </div>
        </div>

        <Alert
          v-if="status === 'error' || status === 'unknown'"
          :variant="status === 'error' ? 'destructive' : 'default'"
          class="min-w-0"
        >
          <AlertTitle class="break-words">
            {{ status === 'unknown' ? t('apps.progress.unknownTitle') : error || t('apps.progress.failedTitle') }}
          </AlertTitle>
          <AlertDescription>{{ t(status === 'unknown' ? 'apps.progress.unknownHint' : 'apps.progress.failedHint') }}</AlertDescription>
        </Alert>

        <Alert
          v-else-if="status === 'done' && result === 'partial'"
          variant="default"
          class="min-w-0"
        >
          <AlertTitle>{{ t('apps.progress.partialTitle') }}</AlertTitle>
          <AlertDescription>{{ t('apps.progress.partialHint') }}</AlertDescription>
        </Alert>
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
            {{ t('apps.progress.runInBackground') }}
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
              v-if="canRetry && status === 'error'"
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
