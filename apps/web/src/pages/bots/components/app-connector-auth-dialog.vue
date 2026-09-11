<template>
  <Dialog
    :open="open"
    @update:open="updateOpen"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader>
        <DialogTitle>
          {{ t('connectors.connectTitle', { name: catalog?.name || connector?.type || t('connectors.unknown') }) }}
        </DialogTitle>
        <DialogDescription>
          {{ catalog?.description || t('apps.connector.authDescription', { name: appName }) }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <Alert
          v-if="!catalog"
          variant="default"
        >
          <AlertTitle>{{ t('apps.connector.unavailableTitle') }}</AlertTitle>
          <AlertDescription>{{ t('apps.connector.unavailableDescription') }}</AlertDescription>
        </Alert>
        <form
          v-else
          id="app-connector-auth-form"
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
                  <Select v-bind="componentField">
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
                    v-bind="componentField"
                    :type="field.secret ? 'password' : 'text'"
                    :placeholder="t('connectors.credentialPlaceholder', { field: field.label || field.key })"
                  />
                </FormControl>
              </FieldStack>
            </FormField>
          </FormStack>
        </form>
      </DialogBody>

      <DialogFooter>
        <DialogClose as-child>
          <Button
            variant="outline"
            :disabled="phase === 'submitting'"
          >
            {{ t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          v-if="catalog"
          form="app-connector-auth-form"
          type="submit"
          :loading="phase !== 'idle'"
        >
          {{ phase === 'awaiting-oauth' ? t('connectors.awaitingAuthorization') : t('connectors.connect') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
// Authorizes one connector an App references. The connection is created
// through the App endpoints so the Server links it to the installation;
// the OAuth popup handling is the same as before.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Button,
  Dialog,
  DialogBody,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
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
  toast,
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
  open: boolean
  botId: string
  installationId: string
  appName: string
  connector: AppConnectorItem | null
  /** Catalog entry for the connector type, undefined when Connect-It lacks it. */
  catalog: ConnectitConnector | undefined
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
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

watch(
  () => [props.open, props.connector, props.catalog] as const,
  ([open]) => {
    if (!open) return
    const method = authMethods.value[0]
    form.resetForm({ values: { auth_method: method?.key || '', fields: credentialDefaults(method) } })
  },
  { immediate: true },
)

watch(
  () => form.values.auth_method,
  (methodKey, previous) => {
    if (!props.open || !previous || methodKey === previous) return
    form.setFieldValue('fields', credentialDefaults(authMethods.value.find(item => item.key === methodKey)))
  },
)

const phase = ref<'idle' | 'submitting' | 'awaiting-oauth'>('idle')
let attempt: AbortController | null = null

function updateOpen(open: boolean) {
  if (!open) {
    if (phase.value === 'submitting') return
    attempt?.abort()
  }
  emit('update:open', open)
}

async function connect() {
  if (phase.value !== 'idle') return
  const method = selectedMethod.value
  const connectorType = props.connector?.type
  const oauthPopup = method?.type === 'oauth2' ? prepareConnectorOAuthPopup(t('common.loading')) : null
  if (method?.type === 'oauth2' && !oauthPopup && !window.api?.desktop?.openExternalUrl) {
    toast.error(t('connectors.oauthPopupBlocked'))
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
    toast.success(t('connectors.connectedSuccess', { name: props.catalog?.name || connectorType }))
    emit('update:open', false)
    emit('authorized')
  } catch (error) {
    oauthPopup?.close()
    if (flow.signal.aborted) return
    const oauthKey = connectorOAuthErrorKey(error)
    toast.error(oauthKey ? t(oauthKey) : resolveApiErrorMessage(error, t('connectors.connectFailed')))
  } finally {
    if (flow.signal.aborted) oauthPopup?.close()
    if (attempt === flow) {
      attempt = null
      phase.value = 'idle'
    }
  }
}
</script>
