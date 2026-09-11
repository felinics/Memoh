<script setup lang="ts">
// Preflight runs before a direct agent can be enabled. Mounted once by
// bot-agents.vue; `run(agent)` resolves true only when its declared dependency
// is installed, and only then does the caller write `enabled: true`.
// A missing dependency is installed through its canonical App (the
// App with the dependency's own ID), so the bot's Apps tab shows it
// afterwards like anything else installed from the Supermarket. Cancellation,
// an unavailable workspace or platform, and failed or backgrounded
// installation all leave the agent disabled.
import { computed, onBeforeUnmount, onDeactivated, ref, shallowRef } from 'vue'
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
import {
  getSupermarketRegistriesByRegistryIdAppsByAppId,
  postBotsByBotIdContainerStart,
  type BotagentsBotAgent,
} from '@memohai/sdk'
import {
  preflightDependencies,
  type DependencyItem,
} from '@/composables/api/useWorkspaceDependencies'
import { appDisplayName } from '@/composables/api/useApps'
import { useAppOperationsStore, type AppOperation } from '@/store/app-operations'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { dependencyDisplayName } from '@/utils/workspace-dependency'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'
import AppProgressDialog from './app-progress-dialog.vue'
import {
  agentDependencyRequirement,
  dependencyItemFromPreflight,
  resolveEnableFlowStep,
  type EnableFlowRequirement,
} from './dependency-enable-flow'

const DEPENDENCY_REGISTRY = 'memoh'

const props = defineProps<{ botId: string }>()

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useAppOperationsStore()

let settle: ((ok: boolean) => void) | null = null
let generation = 0
const requirement = ref<EnableFlowRequirement | null>(null)
const item = ref<DependencyItem | null>(null)
const name = computed(() => (item.value ? dependencyDisplayName(item.value) : ''))

/** True while the preflight request is in flight (the row shows "Checking…"). */
const checking = ref(false)

const workspaceOpen = ref(false)
const workspaceState = ref<'not_running' | 'missing'>('not_running')
const starting = ref(false)

const confirmOpen = ref(false)
const installing = ref(false)

const VIEWER_ID = 'dependency-enable-flow'
const progressOpen = ref(false)
const displayed = shallowRef<AppOperation | null>(null)

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
  generation += 1
  checking.value = false
  const resolve = settle
  settle = null
  workspaceOpen.value = false
  confirmOpen.value = false
  hideProgress()
  resolve?.(ok)
}

async function preflight() {
  const currentGeneration = generation
  const declared = requirement.value
  if (!declared) return finish(false)
  checking.value = true
  let step
  try {
    const response = await preflightDependencies(props.botId, '', [declared.dependencyId])
    if (!settle || currentGeneration !== generation) return
    step = resolveEnableFlowStep(declared, response)
  } catch (error) {
    if (currentGeneration !== generation) return
    toast.error(resolveApiErrorMessage(error, t('bots.dependencies.preflight.failed'), { prefixFallback: true }))
    return finish(false)
  } finally {
    if (currentGeneration === generation) checking.value = false
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
      const running = store.get(props.botId, DEPENDENCY_REGISTRY, step.item.id)
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
  if (!value && !installing.value) finish(false)
}

// The canonical App of the dependency carries it; installing that
// App's current release installs the dependency.
async function onConfirmed() {
  const current = item.value
  const depId = current?.id
  if (!current || !depId) return finish(false)
  installing.value = true
  try {
    const { data } = await getSupermarketRegistriesByRegistryIdAppsByAppId({
      path: { registry_id: DEPENDENCY_REGISTRY, app_id: depId },
      throwOnError: true,
    })
    if (!data.revision) throw new Error('missing revision')
    const result = store.start({
      botId: props.botId,
      targetId: '',
      registryId: DEPENDENCY_REGISTRY,
      appId: depId,
      name: appDisplayName(data, locale.value),
      action: 'install',
      install: { registryId: DEPENDENCY_REGISTRY, appId: depId, revision: data.revision },
      onBackgroundDone,
    })
    confirmOpen.value = false
    switch (result.kind) {
      case 'started':
      case 'running':
        showProgress(result.operation)
        return
      case 'busy':
        toast.error(t('apps.busy'))
        return finish(false)
      default:
        return finish(false)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('supermarket.loadError')))
    return finish(false)
  } finally {
    installing.value = false
  }
}

function onBackgroundDone(operation: AppOperation) {
  toast.success(t('bots.agent.dependencyInstalledEnableHint', { name: operation.name }))
}

function showProgress(operation: AppOperation) {
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

function onProgressOpenChange(value: boolean) {
  if (value) return
  finish(displayed.value?.status === 'done' && displayed.value.result === 'installed')
}

onDeactivated(() => finish(false))
onBeforeUnmount(() => finish(false))

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

  <Dialog
    :open="confirmOpen"
    @update:open="onConfirmOpenChange"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ t('bots.dependencies.confirm.installTitle', { name }) }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ t('apps.enableFlow.installDescription', { name }) }}
        </DialogDescription>
      </DialogHeader>
      <DialogBody class="min-w-0">
        <DependencyKvList :rows="workspaceRows" />
      </DialogBody>
      <DialogFooter class="min-w-0 items-center gap-2">
        <Button
          variant="outline"
          :disabled="installing"
          @click="finish(false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          :loading="installing"
          @click="onConfirmed"
        >
          {{ t('bots.dependencies.confirm.installAndEnable') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <AppProgressDialog
    :open="progressOpen"
    :name="displayed?.name ?? name"
    :action="displayed?.action ?? 'install'"
    :steps="displayed?.steps ?? []"
    :lines="displayed?.lines ?? []"
    :status="displayed?.status ?? 'running'"
    :result="displayed?.result"
    :error="displayed?.error"
    @update:open="onProgressOpenChange"
    @retry="retryOperation"
  />
</template>
