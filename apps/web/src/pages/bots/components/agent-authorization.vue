<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue, SettingsRow } from '@felinic/ui'
import { useAgentAuthorization } from '@/composables/useAgentAuthorization'
import {
  AGENT_AUTH_MODE_COPY,
  DIRECT_BOT_AGENT_API_KEY_AUTH_KIND,
  DIRECT_BOT_AGENT_AUTH_MODES,
  isDeviceLoginAuthMode,
  type DirectBotAgentRuntime,
} from '@/utils/bot-agent'
import AgentCredentialInput from './agent-credential-input.vue'

const props = defineProps<{ runtime: DirectBotAgentRuntime, storageKey: string }>()
const emit = defineEmits<{ status: [value: { ready: boolean, busy: boolean, id: string, auth: string }] }>()
const modes = DIRECT_BOT_AGENT_AUTH_MODES[props.runtime]
const mode = ref(modes[0])
const secret = ref('')
const { session, ready, loading, error, start, cancel, handoff, resume } = useAgentAuthorization(() => props.runtime, props.storageKey)
// An account that signs in on the Agent's own process has nothing to stage
// here: the Bot is created first and signs in from its settings.
const deviceLoginAuth = computed(() => isDeviceLoginAuthMode(mode.value) ? mode.value : undefined)
// Only an API key can be staged, so a staged one means its mode.
watch(() => session.value?.auth_kind, (kind) => {
  if (kind) mode.value = 'api_key'
})
watch([ready, loading, mode, deviceLoginAuth, () => session.value?.id], () => {
  emit('status', { ready: !!deviceLoginAuth.value || ready.value, busy: loading.value, id: session.value?.id ?? '', auth: mode.value })
}, { immediate: true })
function changeMode(value: unknown) {
  const next = modes.find(option => option === value)
  if (!next || next === mode.value) return
  cancel()
  secret.value = ''
  mode.value = next
}
async function save() {
  await start(DIRECT_BOT_AGENT_API_KEY_AUTH_KIND[props.runtime], secret.value.trim())
  secret.value = ''
}
defineExpose({ handoff, resume })
</script>

<template>
  <SettingsRow :label="$t('bots.agent.authMode')">
    <Select
      :model-value="mode"
      :disabled="loading"
      @update:model-value="changeMode"
    >
      <SelectTrigger class="w-full sm:w-56">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem
          v-for="option in modes"
          :key="option"
          :value="option"
        >
          {{ $t(AGENT_AUTH_MODE_COPY[option].label) }}
        </SelectItem>
      </SelectContent>
    </Select>
  </SettingsRow>
  <SettingsRow
    v-if="deviceLoginAuth"
    :label="$t(AGENT_AUTH_MODE_COPY[deviceLoginAuth].label)"
    :description="$t('bots.agent.signInAfterCreate')"
  />
  <SettingsRow
    v-else
    :label="$t('bots.agent.apiKey')"
    :description="error || (ready ? $t('bots.settings.agentCredentialSaved') : $t('bots.agent.apiKeyDescription'))"
    stack="always"
  >
    <AgentCredentialInput
      v-model="secret"
      :loading="loading"
      :connected="ready"
      @save="save"
    />
  </SettingsRow>
</template>
