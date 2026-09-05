<script setup lang="ts">
// The blocking dependency check that runs before a direct agent is enabled
// (design §9.3). Mounted once by bot-agents.vue; `run(agent)` resolves true
// only when the declared dependency is installed — the caller writes
// `enabled: true` after that and never before (WD-EXT-002). Every exit that is
// not an installed dependency resolves false: cancel, a stopped workspace the
// user did not start, an unsupported platform, a failed install (WD-EXT-003 —
// the error stays in the progress dialog with the full log), or the install
// sent to the background — the switch stays off and a toast says when the
// dependency is ready to enable, so nothing lights up while the user is away.
// No dependency is pinned, so the only operation here is an install; the
// confirm dialog lets the user name a version, blank meaning the latest.
import { computed, onBeforeUnmount, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
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
  preflightDependencies,
  type DependencyItem,
  type ScriptAction,
  type ScriptResponse,
} from '@/composables/api/useWorkspaceDependencies'
import { useDependencyOperationsStore, type DependencyOperation } from '@/store/dependency-operations'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { dependencyDisplayName } from '@/utils/workspace-dependency'
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
const store = useDependencyOperationsStore()

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

const VIEWER_ID = 'dependency-enable-flow'
const progressOpen = ref(false)
// Kept across close so the dialog fades out with its content intact even
// after the store dropped the record.
const displayed = shallowRef<DependencyOperation | null>(null)

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
  hideProgress()
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
    case 'install': {
      item.value = step.item
      // An install sent to the background earlier is still streaming: reopen
      // its log rather than asking to confirm a second one.
      const running = store.get(props.botId, step.item.id)
      if (running?.status === 'running') {
        showProgress(running)
        return
      }
      confirmOpen.value = true
      return
    }
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
  confirmOpen.value = false
  const current = item.value
  if (!current) return finish(false)
  const result = store.start({
    botId: props.botId,
    targetId: '',
    item: current,
    action: OPERATION,
    version,
    onBackgroundDone: onBackgroundDone,
  })
  switch (result.kind) {
    case 'started':
    case 'running':
      showProgress(result.operation)
      return
    case 'busy':
      toast.error(t('bots.dependencies.busy'))
      return finish(false)
    default:
      return finish(false)
  }
}

// The install finished with the dialog closed: the agent was never enabled
// (WD-EXT-002), so the toast points back at the switch instead of flipping it.
function onBackgroundDone(operation: DependencyOperation) {
  toast.success(t('bots.agent.dependencyInstalledEnableHint', { name: dependencyDisplayName(operation.item) }))
}

function showProgress(operation: DependencyOperation) {
  displayed.value = operation
  progressOpen.value = true
  store.view(operation.key, VIEWER_ID)
}

function hideProgress() {
  if (!progressOpen.value) return
  progressOpen.value = false
  if (displayed.value) store.unview(displayed.value.key, VIEWER_ID)
}

function retryOperation() {
  if (displayed.value) store.retry(displayed.value.key)
}

// Closing a finished dialog is the verdict; closing a running one sends the
// install to the background, which resolves the flow as "not enabled".
function onProgressOpenChange(value: boolean) {
  if (value) return
  finish(displayed.value?.status === 'done')
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

onBeforeUnmount(hideProgress)

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
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ workspaceState === 'missing'
            ? t('bots.dependencies.preflight.workspaceMissingTitle')
            : t('bots.dependencies.preflight.workspaceNotRunningTitle') }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ t('bots.dependencies.preflight.workspaceNotRunningDescription', { name }) }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0">
        <DependencyKvList :rows="workspaceRows" />
      </DialogBody>

      <DialogFooter class="min-w-0 items-center gap-2">
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
    :name="name"
    :action="OPERATION"
    :lines="displayed?.lines ?? []"
    :status="displayed?.status ?? 'running'"
    :error="displayed?.error"
    :result-version="displayed?.resultVersion"
    :entrypoint="displayed?.entrypoint"
    @update:open="onProgressOpenChange"
    @retry="retryOperation"
  />
</template>
