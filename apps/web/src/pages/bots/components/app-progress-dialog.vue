<script setup lang="ts">
// Live progress of one streamed App operation: the component steps the
// Server walks through (dependencies, Skills, connectors) above the raw script
// log. Closing while running means "run in background"; the store keeps the
// stream and the verdict lands as a toast.
import { computed, nextTick, ref, toRef, watch } from 'vue'
import { useQuery } from '@pinia/colada'
import { getConnectorsCatalog } from '@memohai/sdk'
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
  InlineLoadingRow,
  Spinner,
  TextButton,
  toast,
  useClipboard,
} from '@felinic/ui'
import { AlertTriangle, Check, CircleDashed, Link2, KeyRound } from 'lucide-vue-next'
import type { AppOperationAction } from '@/composables/api/useAppStream'
import AppConnectorAuthForm from './app-connector-auth-form.vue'
import { useAppInstallAuthorization } from '../composables/useAppInstallAuthorization'
import type { AppOperation, AppOperationStep } from '@/store/app-operations'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'

const props = withDefaults(defineProps<{
  open: boolean
  operation?: AppOperation | null
  oauthPopup?: Window | null
  hasSkills?: boolean
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
  operation: null,
  oauthPopup: null,
  hasSkills: true,
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

const operation = computed(() => props.operation ?? null)
const authorization = useAppInstallAuthorization(operation, toRef(props, 'open'))
const { installation, loading: authorizationLoading, error: authorizationError, current: connector, needsSetup } = authorization
const authForm = ref<InstanceType<typeof AppConnectorAuthForm> | null>(null)
const authPhase = ref<'idle' | 'submitting' | 'awaiting-oauth'>('idle')
const catalogQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => (await getConnectorsCatalog({ throwOnError: true })).data,
  enabled: () => props.open && !!props.operation && props.steps.some(step => step.kind === 'connector'),
})
const catalog = computed(() => catalogQuery.data.value?.find(item => item.type === connector.value?.type))
const visibleSteps = computed(() => props.steps.filter(step => step.kind !== 'skills' || props.hasSkills))
const catalogLoading = computed(() => !catalog.value && catalogQuery.asyncStatus.value === 'loading')
const setupLoading = computed(() => authorizationLoading.value || catalogLoading.value)
const showAuthForm = computed(() => props.open && needsSetup.value && !setupLoading.value
  && !authorizationError.value && !!connector.value && !!catalog.value)
watch(showAuthForm, shown => { if (!shown) authPhase.value = 'idle' })
watch([() => props.status, needsSetup, authorizationLoading, () => props.open], () => {
  if (!props.open || props.status === 'error' || props.status === 'unknown'
    || (props.status === 'done' && !needsSetup.value && !authorizationLoading.value)) props.oauthPopup?.close()
})
function updateOpen(open: boolean) {
  if (!open && authPhase.value === 'submitting') return
  if (!open) authForm.value?.cancel()
  emit('update:open', open)
}
async function retryAuthorization() {
  await Promise.all([authorization.refresh(), catalogQuery.refetch()])
}

const subtitle = computed(() => {
  if (needsSetup.value) {
    if (setupLoading.value) return t('apps.progress.preparingAuthorization')
    if (authPhase.value === 'awaiting-oauth') return t('apps.progress.awaitingAuthorization')
    return t('apps.progress.authorize', { name: catalog.value?.name || connector.value?.type || props.name })
  }
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
      return catalogQuery.data.value?.find(item => item.type === step.id)?.name || step.id
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

function stepIcon(step: AppOperationStep) {
  if (step.kind === 'connector' && step.id === connector.value?.type && authPhase.value !== 'idle') return Spinner
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
    @update:open="updateOpen"
  >
    <DialogPanel
      width="lg"
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
          v-if="visibleSteps.length"
          class="space-y-1.5"
        >
          <li
            v-for="step in visibleSteps"
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
            <span :class="['installed', 'linked', 'needs_auth'].includes(step.status) ? 'sr-only' : 'text-caption text-muted-foreground'">
              {{ stepStatusLabel(step) }}
            </span>
          </li>
        </ul>

        <div
          v-if="lines.length"
          ref="scroller"
          role="region"
          :aria-label="t('apps.progress.log')"
          tabindex="0"
          class="max-h-60 min-w-0 overflow-auto rounded-lg border border-border bg-muted-soft p-3 font-mono text-caption leading-relaxed text-foreground"
        >
          <div
            v-for="(line, index) in lines"
            :key="line.id ?? index"
            class="min-w-0 whitespace-pre-wrap break-all"
            :class="{ 'text-muted-foreground': line.stream !== 'stdout' }"
          >
            {{ line.data }}
          </div>
        </div>

        <InlineLoadingRow v-if="needsSetup && setupLoading">
          {{ t('apps.progress.preparingAuthorization') }}
        </InlineLoadingRow>
        <AppConnectorAuthForm
          v-else-if="showAuthForm && operation && installation"
          :key="`${installation.installation_id}/${connector?.type}`"
          ref="authForm"
          :bot-id="operation.botId"
          :installation-id="installation.installation_id!"
          :connector="connector ?? null"
          :catalog="catalog"
          :oauth-popup="oauthPopup?.closed ? null : oauthPopup"
          auto-start
          @phase="authPhase = $event"
          @authorized="authorization.authorized"
        />
        <Alert
          v-else-if="needsSetup"
          variant="destructive"
        >
          <AlertTitle>{{ t('apps.progress.authorizationLoadFailed') }}</AlertTitle>
          <AlertDescription>{{ authorizationError || t('apps.connector.unavailableDescription') }}</AlertDescription>
        </Alert>

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
          v-else-if="status === 'done' && result === 'partial' && !needsSetup"
          variant="default"
          class="min-w-0"
        >
          <AlertTitle>{{ t('apps.progress.partialTitle') }}</AlertTitle>
          <AlertDescription>{{ t('apps.progress.partialHint') }}</AlertDescription>
        </Alert>
      </DialogBody>

      <DialogFooter class="min-w-0 items-center gap-2 sm:justify-between">
        <TextButton
          v-if="lines.length"
          @click="copyLog"
        >
          {{ t('common.copy') }}
        </TextButton>
        <div class="ml-auto flex items-center gap-2">
          <template v-if="needsSetup">
            <Button
              variant="outline"
              :disabled="authPhase === 'submitting'"
              @click="updateOpen(false)"
            >
              {{ t('bots.dependencies.close') }}
            </Button>
            <Button
              v-if="showAuthForm && authPhase === 'awaiting-oauth'"
              @click="authForm?.cancel()"
            >
              {{ t('common.cancel') }}
            </Button>
            <Button
              v-else-if="showAuthForm"
              :loading="authPhase === 'submitting'"
              @click="authForm?.connect()"
            >
              {{ t('connectors.connect') }}
            </Button>
            <Button
              v-else-if="!setupLoading"
              @click="retryAuthorization"
            >
              {{ t('common.retry') }}
            </Button>
          </template>
          <Button
            v-else-if="running"
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
