<script setup lang="ts">
// The blocking dependency check that runs before a direct agent is enabled
// (design §9.3). Mounted once by bot-agents.vue; `run(agent)` resolves true
// only when the declared dependency is installed — the caller writes
// `enabled: true` after that and never before (WD-EXT-002). Every exit that is
// not an installed dependency resolves false: cancel, a stopped workspace the
// user did not start, an unsupported platform, or a failed install
// (WD-EXT-003 — the error stays in the progress dialog with the full log).
// No dependency is pinned, so the only operation here is an install; the
// confirm dialog lets the user name a version, blank meaning the latest.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useQueryCache } from '@pinia/colada'
import {
  Button,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  toast,
} from '@felinic/ui'
import { postBotsByBotIdContainerStart, type BotagentsBotAgent } from '@memohai/sdk'
import {
  fetchDependencyScript,
  invalidateBotDependencies,
  preflightDependencies,
  type DependencyItem,
  type ScriptAction,
  type ScriptResponse,
} from '@/composables/api/useWorkspaceDependencies'
import { streamDependencyOperation } from '@/composables/api/useWorkspaceDependencyStream'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  dependencyDisplayName,
  formatDependencyVersion,
  type DependencyLogLine,
  type DependencyProgressStatus,
} from '@/utils/workspace-dependency'
import DependencyConfirmDialog from './dependency-confirm-dialog.vue'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'
import DependencyProgressDialog from './dependency-progress-dialog.vue'
import DependencyScriptDialog from './dependency-script-dialog.vue'
import {
  agentDependencyRequirement,
  dependencyItemFromPreflight,
  resolveEnableFlowStep,
  type EnableFlowRequirement,
} from './dependency-enable-flow'

const props = defineProps<{ botId: string }>()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const queryCache = useQueryCache()

// One run at a time: the resolver of the pending `run()` promise. Every path
// out of the flow goes through `finish()` so a promise can never be left
// dangling with a dialog closed underneath it.
let settle: ((ok: boolean) => void) | null = null
const requirement = ref<EnableFlowRequirement | null>(null)
const item = ref<DependencyItem | null>(null)
const name = computed(() => (item.value ? dependencyDisplayName(item.value) : ''))

/** True while the preflight request is in flight (the row shows "Checking…"). */
const checking = ref(false)

const workspaceOpen = ref(false)
const workspaceState = ref<'not_running' | 'missing'>('not_running')
const starting = ref(false)

const confirmOpen = ref(false)
const OPERATION = 'install'
/** Version the user confirmed; empty means the latest. Replayed by Retry. */
let requestedVersion = ''

const progressOpen = ref(false)
const progressStatus = ref<DependencyProgressStatus>('running')
const progressError = ref('')
const lines = ref<DependencyLogLine[]>([])
const resultVersion = ref('')
const entrypoint = ref('')
const progressTitle = computed(() => t('bots.dependencies.progress.installing', { name: name.value }))

const scriptOpen = ref(false)
const scriptLoading = ref(false)
const scriptError = ref('')
const script = ref<ScriptResponse | null>(null)

const workspaceRows = computed<DependencyKvRow[]>(() => [
  { label: t('bots.dependencies.confirm.dependency'), value: item.value?.id, mono: true },
])

async function run(agent: BotagentsBotAgent): Promise<boolean> {
  const declared = agentDependencyRequirement(agent)
  if (!declared) return true
  if (settle) return false
  return new Promise<boolean>((resolve) => {
    settle = resolve
    requirement.value = declared
    item.value = dependencyItemFromPreflight(declared)
    void preflight()
  })
}

function finish(ok: boolean) {
  const resolve = settle
  settle = null
  workspaceOpen.value = false
  confirmOpen.value = false
  progressOpen.value = false
  scriptOpen.value = false
  resolve?.(ok)
}

async function preflight() {
  const declared = requirement.value
  if (!declared) return finish(false)
  checking.value = true
  let step
  try {
    // Empty target = the bot's current one; the Server never starts it here.
    const response = await preflightDependencies(props.botId, '', [declared.dependencyId])
    step = resolveEnableFlowStep(declared, response)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.dependencies.preflight.failed'), { prefixFallback: true }))
    return finish(false)
  } finally {
    checking.value = false
  }
  switch (step.kind) {
    case 'satisfied':
      return finish(true)
    case 'workspace':
      workspaceState.value = step.state
      workspaceOpen.value = true
      return
    case 'remote_offline':
      toast.error(t('bots.agent.dependencyRemoteOffline'))
      return finish(false)
    case 'platform_unsupported':
      item.value = step.item
      toast.error(t('bots.dependencies.preflight.platformUnsupported', { name: name.value }))
      return finish(false)
    case 'install':
      item.value = step.item
      confirmOpen.value = true
      return
    default:
      toast.error(t('bots.dependencies.preflight.failed'))
      return finish(false)
  }
}

function onWorkspaceOpenChange(value: boolean) {
  if (value || starting.value) return
  finish(false)
}

function goToContainer() {
  finish(false)
  void router.replace({ query: { ...route.query, tab: 'container' } }).catch(() => {})
}

// The user asked for the start explicitly, so this is not the silent start
// WD-EXT-004 forbids; the preflight is simply repeated once it is up.
async function startAndContinue() {
  if (starting.value) return
  starting.value = true
  try {
    await postBotsByBotIdContainerStart({ path: { bot_id: props.botId }, throwOnError: true })
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.container.startFailed')))
    return
  } finally {
    starting.value = false
  }
  workspaceOpen.value = false
  await preflight()
}

function onConfirmOpenChange(value: boolean) {
  if (!value) finish(false)
}

function onConfirmed(version: string) {
  requestedVersion = version
  startOperation()
}

function startOperation() {
  confirmOpen.value = false
  lines.value = []
  progressStatus.value = 'running'
  progressError.value = ''
  resultVersion.value = ''
  entrypoint.value = ''
  progressOpen.value = true
  void consumeOperation()
}

async function consumeOperation() {
  const depId = item.value?.id ?? ''
  let sequence = 0
  try {
    for await (const event of streamDependencyOperation(props.botId, depId, OPERATION, undefined, { version: requestedVersion })) {
      switch (event.type) {
        case 'log':
          lines.value.push({ id: sequence++, stream: event.stream, data: event.data })
          break
        case 'done':
          resultVersion.value = formatDependencyVersion(event.version)
          entrypoint.value = Object.values(event.entrypoints ?? {})[0] ?? ''
          progressStatus.value = 'done'
          break
        case 'error':
          progressError.value = event.message
          progressStatus.value = 'error'
          break
      }
    }
    // A stream that closes without a verdict is a failure the user must see.
    if (progressStatus.value === 'running') {
      progressError.value = t('bots.dependencies.progress.failedTitle')
      progressStatus.value = 'error'
    }
  } catch (error) {
    progressError.value = resolveApiErrorMessage(error, t('bots.dependencies.progress.failedTitle'))
    progressStatus.value = 'error'
  } finally {
    void invalidateBotDependencies(queryCache, props.botId)
  }
}

function onProgressOpenChange(value: boolean) {
  if (value) return
  // The dialog refuses to close while running, so a close is a verdict.
  finish(progressStatus.value === 'done')
}

async function openScript() {
  const depId = item.value?.id ?? ''
  const action: ScriptAction = OPERATION
  scriptOpen.value = true
  scriptLoading.value = true
  scriptError.value = ''
  script.value = null
  try {
    script.value = await fetchDependencyScript(props.botId, '', depId, action)
  } catch (error) {
    scriptError.value = resolveApiErrorMessage(error, t('common.loadFailed'))
  } finally {
    scriptLoading.value = false
  }
}

defineExpose({ run, checking })
</script>

<template>
  <Dialog
    :open="workspaceOpen"
    @update:open="onWorkspaceOpenChange"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader>
        <DialogTitle>
          {{ workspaceState === 'missing'
            ? t('bots.dependencies.preflight.workspaceMissingTitle')
            : t('bots.dependencies.preflight.workspaceNotRunningTitle') }}
        </DialogTitle>
        <DialogDescription>
          {{ t('bots.dependencies.preflight.workspaceNotRunningDescription', { name }) }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <DependencyKvList :rows="workspaceRows" />
      </DialogBody>

      <DialogFooter class="items-center gap-2">
        <Button
          variant="outline"
          :disabled="starting"
          @click="finish(false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          variant="outline"
          :disabled="starting"
          @click="goToContainer"
        >
          {{ t('bots.dependencies.workspace.goToContainer') }}
        </Button>
        <Button
          v-if="workspaceState === 'not_running'"
          :loading="starting"
          @click="startAndContinue"
        >
          {{ t('bots.dependencies.preflight.startAndContinue') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <DependencyConfirmDialog
    :open="confirmOpen"
    mode="install"
    :item="item"
    target-kind="native"
    :confirm-label="t('bots.dependencies.confirm.installAndEnable')"
    @update:open="onConfirmOpenChange"
    @confirm="onConfirmed"
    @view-script="openScript"
  />

  <DependencyScriptDialog
    v-model:open="scriptOpen"
    :script="script"
    :loading="scriptLoading"
    :error="scriptError"
    :dependency-name="name"
    :action="OPERATION"
  />

  <DependencyProgressDialog
    :open="progressOpen"
    :title="progressTitle"
    :lines="lines"
    :status="progressStatus"
    :error="progressError"
    :result-version="resultVersion"
    :entrypoint="entrypoint"
    @update:open="onProgressOpenChange"
    @retry="startOperation"
  />
</template>
