<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Button, Dialog, DialogBody, DialogFooter, DialogHeader, DialogPanel, DialogTitle, Input, Label, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, SettingsRow } from '@felinic/ui'
import { ExternalLink, KeyRound } from 'lucide-vue-next'
import { useAgentAuthorization } from '@/composables/useAgentAuthorization'
import CodexAccountPanel from './codex-account-panel.vue'
import AgentCredentialInput from './agent-credential-input.vue'

const props = defineProps<{ runtime: string, storageKey: string }>()
const emit = defineEmits<{ status: [value: { ready: boolean, busy: boolean, id: string, auth: string }] }>()
const mode = ref(props.runtime === 'codex' ? 'chatgpt' : 'claude_account')
const secret = ref('')
const authorizationCode = ref('')
const { session, ready, pending, busy, loading, error, start, exchange, cancel, handoff, resume } = useAgentAuthorization(() => props.runtime, props.storageKey)
const agentAuth = computed(() => mode.value === 'claude_account' ? 'oauth_token' : mode.value)
const authKind = computed(() => mode.value === 'chatgpt' ? 'openai_codex_oauth'
  : agentAuth.value === 'oauth_token' ? 'claude_code_oauth'
    : props.runtime === 'codex' ? 'openai_api_key' : 'anthropic_api_key')
watch(() => session.value?.auth_kind, (kind) => {
  if (kind) mode.value = kind === 'openai_codex_oauth' ? 'chatgpt'
    : kind === 'claude_code_oauth' ? mode.value === 'oauth_token' ? 'oauth_token' : 'claude_account' : 'api_key'
})
watch([ready, busy, agentAuth, () => session.value?.id], () => {
  emit('status', { ready: ready.value, busy: busy.value, id: session.value?.id ?? '', auth: agentAuth.value })
}, { immediate: true })
watch([ready, () => session.value?.id], () => { authorizationCode.value = '' })
function changeMode(value: unknown) {
  if (typeof value !== 'string' || value === mode.value) return
  cancel()
  secret.value = ''
  mode.value = value
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
          v-if="runtime === 'codex'"
          value="chatgpt"
        >
          {{ $t('bots.agent.authChatGPT') }}
        </SelectItem>
        <SelectItem
          v-else
          value="claude_account"
        >
          {{ $t('bots.agent.claudeAccount') }}
        </SelectItem>
        <SelectItem
          v-if="runtime === 'claude-code'"
          value="oauth_token"
        >
          {{ $t('bots.agent.authOAuthToken') }}
        </SelectItem>
        <SelectItem value="api_key">
          {{ $t('bots.agent.apiKey') }}
        </SelectItem>
      </SelectContent>
    </Select>
  </SettingsRow>
  <CodexAccountPanel
    v-if="mode === 'chatgpt'"
    :authorized="ready"
    :authorizing="loading"
    :device-pending="pending"
    :expires-at="session?.expires_at"
    :device-login="session?.user_code && session.verification_url ? { user_code: session.user_code, verification_url: session.verification_url } : null"
    :error="error"
    @connect="start(authKind)"
    @cancel="cancel"
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
