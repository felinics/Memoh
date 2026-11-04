<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Button, Dialog, DialogBody, DialogFooter, DialogHeader, DialogPanel, DialogTitle, Input, Label, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, SettingsRow } from '@felinic/ui'
import { ExternalLink, KeyRound } from 'lucide-vue-next'
import { useAgentAuthorization } from '@/composables/useAgentAuthorization'
import {
  AGENT_AUTH_MODE_COPY,
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
  DIRECT_BOT_AGENT_API_KEY_AUTH_KIND,
  DIRECT_BOT_AGENT_AUTH_MODES,
  agentAuthForCredentialKind,
  isDeviceLoginAuthMode,
  type AgentAuthMode,
  type DirectBotAgentRuntime,
} from '@/utils/bot-agent'
import AgentCredentialInput from './agent-credential-input.vue'

type Mode = AgentAuthMode | 'claude_account'

const props = defineProps<{ runtime: DirectBotAgentRuntime, storageKey: string }>()
const emit = defineEmits<{ status: [value: { ready: boolean, busy: boolean, id: string, auth: string }] }>()
// Before the Bot exists there is no workspace login to reuse, so Claude Code
// signs in through the browser instead (`claude_account`, stored as an
// `oauth_token` credential). The other runtimes offer what an Agent does.
const modes: readonly [Mode, ...Mode[]] = props.runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE
  ? ['claude_account', 'oauth_token', 'api_key']
  : DIRECT_BOT_AGENT_AUTH_MODES[props.runtime]
const mode = ref(modes[0])
const secret = ref('')
const authorizationCode = ref('')
const { session, ready, pending, busy, loading, error, start, exchange, cancel, handoff, resume } = useAgentAuthorization(() => props.runtime, props.storageKey)
const agentAuth = computed(() => mode.value === 'claude_account' ? 'oauth_token' : mode.value)
// An account that signs in on the Agent's own process has nothing to stage
// here: the Bot is created first and signs in from its settings.
const deviceLoginAuth = computed(() => isDeviceLoginAuthMode(mode.value) ? mode.value : undefined)
const authKind = computed(() => agentAuth.value === 'oauth_token' ? 'claude_code_oauth' : DIRECT_BOT_AGENT_API_KEY_AUTH_KIND[props.runtime])
function modeLabel(value: Mode): string {
  return value === 'claude_account' ? 'bots.agent.claudeAccount' : AGENT_AUTH_MODE_COPY[value].label
}
watch(() => session.value?.auth_kind, (kind) => {
  if (!kind) return
  const auth = agentAuthForCredentialKind(kind)
  // Both Claude choices store the same credential kind; keep the one in use.
  mode.value = auth === 'oauth_token' && mode.value !== 'oauth_token' ? 'claude_account' : auth
})
watch([ready, busy, agentAuth, deviceLoginAuth, () => session.value?.id], () => {
  emit('status', { ready: !!deviceLoginAuth.value || ready.value, busy: busy.value, id: session.value?.id ?? '', auth: agentAuth.value })
}, { immediate: true })
watch([ready, () => session.value?.id], () => { authorizationCode.value = '' })
function changeMode(value: unknown) {
  const next = modes.find(option => option === value)
  if (!next || next === mode.value) return
  cancel()
  secret.value = ''
  mode.value = next
}
async function save() {
  await start(authKind.value, secret.value.trim())
  secret.value = ''
}
function openAuthorizationPage() {
  const url = session.value?.authorization_url
  if (url) window.open(url, '_blank', 'noopener,noreferrer')
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
          {{ $t(modeLabel(option)) }}
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
    v-else-if="mode === 'claude_account'"
    :label="$t('bots.agent.claudeAccount')"
    :description="ready ? $t('bots.settings.agentCredentialSaved') : $t('bots.agent.claudeAccountDescription')"
  >
    <Button
      type="button"
      variant="outline"
      size="sm"
      :loading="loading"
      :disabled="loading"
      @click="pending ? cancel() : start(authKind)"
    >
      <KeyRound v-if="!pending" />
      {{ pending ? $t('common.cancel') : ready ? $t('bots.agent.reconnect') : $t('provider.oauth.connect') }}
    </Button>
  </SettingsRow>
  <SettingsRow
    v-else
    :label="mode === 'oauth_token' ? $t('bots.agent.oauthToken') : $t('bots.agent.apiKey')"
    :description="error || (ready ? $t('bots.settings.agentCredentialSaved') : mode === 'oauth_token' ? $t('bots.agent.oauthTokenDescription') : $t('bots.agent.apiKeyDescription'))"
    stack="always"
  >
    <AgentCredentialInput
      v-model="secret"
      :loading="loading"
      :connected="ready"
      :placeholder="mode === 'oauth_token' ? $t('bots.agent.oauthToken') : undefined"
      @save="save"
    />
  </SettingsRow>
  <Dialog
    :open="pending && !!session?.authorization_url"
    @update:open="!$event && cancel()"
  >
    <DialogPanel
      width="lg"
      footer
      :aria-describedby="undefined"
    >
      <DialogHeader>
        <DialogTitle>{{ $t('bots.agent.claudeAccount') }}</DialogTitle>
      </DialogHeader>
      <DialogBody class="space-y-4">
        <p class="text-sm text-muted-foreground">
          {{ $t('bots.agent.claudeAuthorizationHint') }}
        </p>
        <Button
          type="button"
          variant="outline"
          @click="openAuthorizationPage"
        >
          <ExternalLink />
          {{ $t('bots.agent.openAuthorizationPage') }}
        </Button>
        <form
          id="claude-authorization-code-form"
          class="space-y-2"
          @submit.prevent="exchange(authorizationCode)"
        >
          <Label for="claude-authorization-code">{{ $t('bots.agent.authorizationCode') }}</Label>
          <Input
            id="claude-authorization-code"
            v-model="authorizationCode"
            :placeholder="$t('bots.agent.authorizationCodePlaceholder')"
            autocomplete="off"
            :spellcheck="false"
            :disabled="loading"
          />
          <p
            v-if="error"
            role="alert"
            class="text-sm text-destructive"
          >
            {{ error }}
          </p>
        </form>
      </DialogBody>
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          @click="cancel()"
        >
          {{ $t('common.cancel') }}
        </Button>
        <Button
          type="submit"
          form="claude-authorization-code-form"
          :disabled="!authorizationCode.trim() || loading"
          :loading="loading"
        >
          {{ $t('bots.agent.completeAuthorization') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
  <p
    v-if="mode === 'claude_account' && error && !pending"
    role="alert"
    class="px-4 text-sm text-destructive"
  >
    {{ error }}
  </p>
</template>
