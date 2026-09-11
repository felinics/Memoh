<template>
  <div class="space-y-4">
    <form
      :id="formId"
      ref="formElement"
      @submit.prevent="connect"
    >
      <FormStack>
        <FormField
          v-if="authMethods.length > 1"
          v-slot="{ componentField }"
          name="auth_method"
        >
          <FieldStack :label="t('connectors.authMethod')">
            <FormControl>
              <Select
                v-bind="componentField"
                :disabled="phase !== 'idle'"
              >
                <SelectTrigger class="w-full">
                  <SelectValue :placeholder="t('connectors.selectAuthMethod')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="method in authMethods"
                    :key="method.key"
                    :value="method.key!"
                  >
                    {{ method.label || method.key }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FormControl>
          </FieldStack>
        </FormField>

        <FormField
          v-for="field in credentialFields"
          :key="field.key"
          v-slot="{ componentField }"
          :name="`fields.${field.key}`"
        >
          <FieldStack
            :label="field.label || field.key"
            :help="field.description"
          >
            <FormControl>
              <Select
                v-if="field.input_type === 'select'"
                :disabled="phase !== 'idle'"
                v-bind="componentField"
              >
                <SelectTrigger class="w-full">
                  <SelectValue :placeholder="field.label || field.key" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="option in field.options ?? []"
                    :key="option"
                    :value="option"
                  >
                    {{ option }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <Input
                v-else
                :disabled="phase !== 'idle'"
                v-bind="componentField"
                :type="field.secret ? 'password' : 'text'"
                :placeholder="t('connectors.credentialPlaceholder', { field: field.label || field.key })"
              />
            </FormControl>
          </FieldStack>
        </FormField>
      </FormStack>
    </form>
    <Alert
      v-if="errorMessage"
      variant="destructive"
    >
      <AlertTitle>{{ t('connectors.connectFailed') }}</AlertTitle>
      <AlertDescription>{{ errorMessage }}</AlertDescription>
    </Alert>
  </div>
</template>

<script setup lang="ts">
// Both installation progress and the Apps tab use this authorization form.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import {
  Alert,
  AlertDescription,
  AlertTitle,
  FieldStack,
  FormControl,
  FormField,
  FormStack,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@felinic/ui'
import type { ConnectitAuthMethod, ConnectitConnector } from '@memohai/sdk'
import {
  beginAppConnectorOAuth,
  createAppConnectorCredential,
  type AppConnectorItem,
} from '@/composables/api/useApps'
import {
  connectorOAuthErrorKey,
  isConnectorOAuthCancelled,
  openConnectorOAuthURL,
  prepareConnectorOAuthPopup,
  waitForConnectorOAuth,
} from '@/composables/useConnectorOAuth'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  autoStart?: boolean
  oauthPopup?: Window | null
  botId: string
  installationId: string
  connector: AppConnectorItem | null
  /** Catalog entry for the connector type, undefined when Connect-It lacks it. */
  catalog: ConnectitConnector | undefined
}>()

const emit = defineEmits<{
  phase: [phase: 'idle' | 'submitting' | 'awaiting-oauth']
  authorized: []
}>()

const { t } = useI18n()
const schema = toTypedSchema(z.object({
  auth_method: z.string().min(1, t('connectors.validation.authMethodRequired')),
  fields: z.record(z.string(), z.string().optional()),
}))
const form = useForm({
  validationSchema: schema,
  initialValues: { auth_method: '', fields: {} },
})

const authMethods = computed(() => (props.catalog?.auth_methods ?? []).filter(method => method.key))
const selectedMethod = computed<ConnectitAuthMethod | undefined>(() =>
  authMethods.value.find(method => method.key === form.values.auth_method),
)
function methodCredentialFields(method: ConnectitAuthMethod | undefined) {
  if (method?.type === 'oauth2') return []
  return (method?.credential_fields ?? []).filter(field => field.key)
}
function credentialDefaults(method: ConnectitAuthMethod | undefined): Record<string, string> {
  return Object.fromEntries(methodCredentialFields(method).map(field => [field.key!, field.default_value ?? '']))
}
const credentialFields = computed(() => methodCredentialFields(selectedMethod.value))

watch(() => [props.connector?.type, props.installationId], () => {
  const method = authMethods.value[0]
  form.resetForm({ values: { auth_method: method?.key || '', fields: credentialDefaults(method) } })
}, { immediate: true })

watch(
  () => form.values.auth_method,
  (methodKey, previous) => {
    if (!previous || methodKey === previous) return
    form.setFieldValue('fields', credentialDefaults(authMethods.value.find(item => item.key === methodKey)))
  },
)

const phase = ref<'idle' | 'submitting' | 'awaiting-oauth'>('idle')
let attempt: AbortController | null = null

const errorMessage = ref('')
const formId = useId()
const formElement = ref<HTMLFormElement | null>(null)
let reservedPopup = props.oauthPopup
watch(phase, value => emit('phase', value), { flush: 'sync' })

function cancel() {
  attempt?.abort()
}
onBeforeUnmount(() => {
  cancel()
  reservedPopup?.close()
})
onMounted(async () => {
  await nextTick()
  if (!formElement.value) return
  formElement.value.querySelector<HTMLElement>('input, select')?.focus()
  // A popup must be reserved by the Install click, before the streamed install.
  // Later connectors without a reserved window use the visible Connect button.
  if (props.autoStart && selectedMethod.value?.type === 'oauth2'
    && (reservedPopup || window.api?.desktop?.openExternalUrl)) void connect()
})
defineExpose({ connect, cancel, phase, formId })

async function connect() {
  if (phase.value !== 'idle') return
  const method = selectedMethod.value
  const connectorType = props.connector?.type
  errorMessage.value = ''
  const oauthPopup = method?.type === 'oauth2' ? (reservedPopup ?? prepareConnectorOAuthPopup(t('common.loading'))) : null
  reservedPopup = null
  if (method?.type === 'oauth2' && !oauthPopup && !window.api?.desktop?.openExternalUrl) {
    errorMessage.value = t('connectors.oauthPopupBlocked')
    return
  }
  const flow = new AbortController()
  attempt = flow
  phase.value = 'submitting'
  try {
    const validation = await form.validate()
    if (!validation.valid || !connectorType || !method?.key) {
      oauthPopup?.close()
      return
    }
    let credentialValid = true
    for (const field of credentialFields.value) {
      if (!field.key) continue
      const value = String(form.values.fields?.[field.key] ?? '').trim()
      if (field.required && !value) {
        form.setFieldError(`fields.${field.key}`, t('connectors.validation.fieldRequired', { field: field.label || field.key }))
        credentialValid = false
      }
      if (value && field.pattern && !new RegExp(field.pattern).test(value)) {
        form.setFieldError(`fields.${field.key}`, t('connectors.validation.fieldInvalid', { field: field.label || field.key }))
        credentialValid = false
      }
    }
    if (!credentialValid) {
      oauthPopup?.close()
      return
    }

    if (method.type === 'oauth2') {
      const result = await beginAppConnectorOAuth(props.botId, props.installationId, connectorType, method.key)
      const connectionId = result.connection_id
      if (!result.authorization_url || !connectionId) throw new Error('oauth_failed')
      if (flow.signal.aborted) return
      phase.value = 'awaiting-oauth'
      await openConnectorOAuthURL(result.authorization_url, oauthPopup)
      try {
        await waitForConnectorOAuth(props.botId, connectionId, oauthPopup, flow.signal)
      } catch (error) {
        if (!isConnectorOAuthCancelled(error)) throw error
        return
      }
    } else {
      await createAppConnectorCredential(
        props.botId,
        props.installationId,
        connectorType,
        method.key,
        Object.fromEntries(Object.entries(form.values.fields ?? {}).map(([key, value]) => [key, String(value ?? '').trim()])),
      )
    }
    if (!flow.signal.aborted) emit('authorized')
  } catch (error) {
    oauthPopup?.close()
    if (flow.signal.aborted) return
    const oauthKey = connectorOAuthErrorKey(error)
    errorMessage.value = oauthKey ? t(oauthKey) : resolveApiErrorMessage(error, t('connectors.connectFailed'))
  } finally {
    if (flow.signal.aborted) oauthPopup?.close()
    if (attempt === flow) {
      attempt = null
      phase.value = 'idle'
    }
  }
}
</script>
