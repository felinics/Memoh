<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, SettingsRow, SettingsSection } from '@felinic/ui'
import { getBotsByBotIdAgents, getBotsByBotIdAgentsById, postBotsByBotIdAgents, patchBotsByBotIdAgentsById, putBotsByBotIdSettings, type BotagentsBotAgent } from '@memohai/sdk'
import { directBotAgentMetadata, isDirectBotAgentConfigured } from '@/utils/bot-agent'
import { resolveApiErrorMessage } from '@/utils/api-error'
import CodexAccountLogin from './codex-account-login.vue'
import SettingsDirectAgentDetail from './settings-direct-agent-detail.vue'
import DependencyEnableFlow from './dependency-enable-flow.vue'

const props = defineProps<{ botId: string, agentId: string, runtime: string }>()
const emit = defineEmits<{ status: [state: { authorized: boolean, busy: boolean }] }>()
const { t } = useI18n()
const dependencyFlow = ref<InstanceType<typeof DependencyEnableFlow> | null>(null)
const prepared = ref(false)
const preparing = ref(false)
const setupError = ref('')
const login = ref({ authorized: false, busy: false })
const agent = ref<BotagentsBotAgent | null>(null)
const authorized = computed(() => prepared.value && (agent.value?.runtime === 'codex'
  ? login.value.authorized
  : isDirectBotAgentConfigured(agent.value) === true))
const busy = computed(() => preparing.value || login.value.busy)
watch([authorized, busy], () => emit('status', { authorized: authorized.value, busy: busy.value }), { immediate: true })

async function prepare() {
  if (preparing.value || !dependencyFlow.value) return
  if (props.runtime !== 'codex' && props.runtime !== 'claude-code') return
  preparing.value = true
  setupError.value = ''
  try {
    // Re-read before retrying a creation whose response may have been lost.
    if (props.agentId) {
      const { data } = await getBotsByBotIdAgentsById({ path: { bot_id: props.botId, id: props.agentId }, throwOnError: true })
      agent.value = data
    } else {
      const { data } = await getBotsByBotIdAgents({ path: { bot_id: props.botId }, throwOnError: true })
      agent.value = data.items?.find(item => item.runtime === props.runtime) ?? null
      if (!agent.value) {
        const { data: created } = await postBotsByBotIdAgents({
          path: { bot_id: props.botId },
          body: { name: props.runtime, runtime: props.runtime, enabled: false, metadata: directBotAgentMetadata(props.runtime) },
          throwOnError: true,
        })
        agent.value = created
      }
    }
    if (!agent.value.id) throw new Error('Created Agent has no ID')
    if (!await dependencyFlow.value.run(agent.value)) return
    if (agent.value.enabled === false) {
      await patchBotsByBotIdAgentsById({
        path: { bot_id: props.botId, id: agent.value.id },
        body: { enabled: true }, throwOnError: true,
      })
      await refreshAgent()
    }
    await putBotsByBotIdSettings({
      path: { bot_id: props.botId },
      body: { default_bot_agent_id: agent.value!.id }, throwOnError: true,
    })
    prepared.value = true
  } catch (cause) {
    setupError.value = resolveApiErrorMessage(cause, t('common.saveFailed'))
  } finally {
    preparing.value = false
  }
}
async function refreshAgent() {
  const id = agent.value?.id
  if (!id) return
  const { data } = await getBotsByBotIdAgentsById({ path: { bot_id: props.botId, id }, throwOnError: true })
  agent.value = data
}
</script>

<template>
  <SettingsSection v-if="!prepared">
    <SettingsRow
      :label="t('bots.agent.runtimeSetup')"
      :description="setupError || t('bots.agentCreate.prepareDescription')"
    >
      <Button
        :loading="preparing"
        @click="prepare()"
      >
        {{ t('bots.agentCreate.prepare') }}
      </Button>
    </SettingsRow>
  </SettingsSection>
  <CodexAccountLogin
    v-else-if="agent?.runtime === 'codex'"
    :bot-id="botId"
    :agent-id="agent.id!"
    @status="login = $event"
  />
  <SettingsDirectAgentDetail
    v-else-if="agent"
    :bot-id="botId"
    :agent="agent"
    @authorized="refreshAgent()"
  />
  <DependencyEnableFlow
    ref="dependencyFlow"
    :bot-id="botId"
  />
</template>
