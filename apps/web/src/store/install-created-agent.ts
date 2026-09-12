import { watch } from 'vue'
import { getSupermarketRegistriesByRegistryIdAppsByAppId, postBotsByBotIdContainerStart, type BotagentsBotAgent } from '@memohai/sdk'
import i18n from '@/i18n'
import { preflightDependencies } from '@/composables/api/useWorkspaceDependencies'
import { agentDependencyRequirement, resolveEnableFlowStep } from '@/pages/bots/components/dependency-enable-flow'
import { externalAgentDisplayName } from '@/utils/external-agent'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAppOperationsStore } from './app-operations'

// Creating this Agent already authorizes installing its runtime. Use the same
// App operation and stream recovery as manual installation, without its dialogs.
export async function installCreatedAgent(botId: string, agent: BotagentsBotAgent): Promise<void> {
  const t = i18n.global.t
  const requirement = agentDependencyRequirement(agent)
  if (!requirement) throw new Error(t('bots.dependencies.preflight.failed'))
  const check = () => preflightDependencies(botId, '', [requirement.dependencyId])
  let response = await check()
  if (response.workspace_state === 'not_running') {
    await postBotsByBotIdContainerStart({ path: { bot_id: botId }, throwOnError: true })
    response = await check()
  }
  const step = resolveEnableFlowStep(requirement, response)
  if (step.kind === 'satisfied') return
  if (step.kind === 'remote_offline') throw new Error(t('bots.agent.dependencyRemoteOffline'))
  if (step.kind === 'platform_unsupported') throw new Error(t('bots.dependencies.preflight.platformUnsupported', { name: step.item.name }))
  if (step.kind !== 'install') throw new Error(t('bots.dependencies.preflight.failed'))

  const { data } = await getSupermarketRegistriesByRegistryIdAppsByAppId({
    path: { registry_id: 'memoh', app_id: requirement.dependencyId }, throwOnError: true,
  }).catch((error: unknown) => {
    throw new Error(resolveApiErrorMessage(error, t('supermarket.loadError')))
  })
  if (!data.revision) throw new Error(t('supermarket.loadError'))
  const operations = useAppOperationsStore()
  const result = operations.start({
    botId, targetId: '', registryId: 'memoh', appId: requirement.dependencyId,
    name: externalAgentDisplayName(agent.runtime ?? '', agent.name ?? ''), action: 'install',
    install: { registryId: 'memoh', appId: requirement.dependencyId, revision: data.revision },
  })
  if (result.kind === 'busy') throw new Error(t('apps.busy'))
  if (result.kind === 'invalid') throw new Error(t('bots.dependencies.preflight.failed'))
  const operation = result.operation
  const viewer = 'bot-create-progress'
  operations.view(operation.key, viewer)
  let stop = () => {}
  try {
    await new Promise<void>((resolve) => {
      stop = watch(() => operation.status, status => { if (status !== 'running') resolve() }, { immediate: true })
    })
    if (operation.status !== 'done' || operation.result !== 'installed') {
      throw new Error(operation.error || t('apps.progress.failedTitle'))
    }
    if (resolveEnableFlowStep(requirement, await check()).kind !== 'satisfied') {
      throw new Error(t('bots.dependencies.preflight.failed'))
    }
  } finally {
    stop()
    operations.unview(operation.key, viewer)
  }
}
