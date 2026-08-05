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
          {{ t('connectors.connectTitle', { name: connector?.name || t('connectors.unknown') }) }}
        </DialogTitle>
        <DialogDescription>
          {{ connector?.description }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <form
          id="connector-connect-form"
          @submit.prevent="connect"
        >
          <FormStack>
            <FormField
              v-slot="{ value, handleChange }"
              name="bot_id"
            >
              <FieldStack :label="t('supermarket.selectBot')">
                <FormControl>
                  <BotSelect
                    :model-value="String(value ?? '')"
                    trigger-class="w-full"
                    @update:model-value="handleChange"
                  />
                </FormControl>
              </FieldStack>
            </FormField>

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
            :disabled="connecting"
          >
            {{ t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          form="connector-connect-form"
          type="submit"
          :loading="connecting"
        >
          {{ t('connectors.connect') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useMutation } from '@pinia/colada'
import {
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
import {
  postBotsByBotIdConnectorsApiKey,
  postBotsByBotIdConnectorsOauth,
  type ConnectitAuthMethod,
  type ConnectitConnector,
} from '@memohai/sdk'
import BotSelect from '@/components/bot-select/index.vue'
import {
  connectorOAuthErrorKey,
  openConnectorOAuthURL,
  prepareConnectorOAuthPopup,
  waitForConnectorOAuth,
} from '@/composables/useConnectorOAuth'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  open: boolean
  connector: ConnectitConnector | null
  defaultBotId?: string
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  connected: [botId: string]
}>()

const { t } = useI18n()
const schema = toTypedSchema(z.object({
  bot_id: z.string().min(1, t('connectors.validation.botRequired')),
  auth_method: z.string().min(1, t('connectors.validation.authMethodRequired')),
  // Tolerate untouched inputs (vee-validate registers them as undefined); the
  // localized required/pattern loop in connect() is the validation that fires.
  fields: z.record(z.string(), z.string().optional()),
}))
const form = useForm({
  validationSchema: schema,
  initialValues: {
    bot_id: '',
    auth_method: '',
    fields: {},
  },
})

const authMethods = computed(() =>
  (props.connector?.auth_methods ?? []).filter(method => method.key),
)
const selectedMethod = computed<ConnectitAuthMethod | undefined>(() =>
  authMethods.value.find(method => method.key === form.values.auth_method),
)
// OAuth methods never transmit credential fields (the OAuth request body
// carries none), so don't render or collect fields that would be dropped.
function methodCredentialFields(method: ConnectitAuthMethod | undefined) {
  if (method?.type === 'oauth2') return []
  return (method?.credential_fields ?? []).filter(field => field.key)
}
// Seed every rendered field key (default value or '') so untouched inputs stay
// strings instead of undefined and never trip the schema before the loop.
function credentialDefaults(method: ConnectitAuthMethod | undefined): Record<string, string> {
  return Object.fromEntries(
    methodCredentialFields(method).map(field => [field.key!, field.default_value ?? '']),
  )
}
const credentialFields = computed(() => methodCredentialFields(selectedMethod.value))

watch(
  () => [props.open, props.connector, props.defaultBotId] as const,
  ([open]) => {
    if (!open) return
    const method = authMethods.value[0]
    form.resetForm({
      values: {
        bot_id: props.defaultBotId || '',
        auth_method: method?.key || '',
        fields: credentialDefaults(method),
      },
    })
  },
  { immediate: true },
)

watch(
  () => form.values.auth_method,
  (methodKey, previous) => {
    if (!props.open || !previous || methodKey === previous) return
    const method = authMethods.value.find(item => item.key === methodKey)
    form.setFieldValue('fields', credentialDefaults(method))
  },
)

const createOAuthMutation = useMutation({
  mutation: async (value: { botId: string, connectorType: string, authMethod: string }) => {
    const { data } = await postBotsByBotIdConnectorsOauth({
      path: { bot_id: value.botId },
      body: {
        connector_type: value.connectorType,
        auth_method: value.authMethod,
      },
      throwOnError: true,
    })
    return data
  },
})

const createCredentialMutation = useMutation({
  mutation: async (value: {
    botId: string
    connectorType: string
    authMethod: string
    fields: Record<string, string>
  }) => {
    const { data } = await postBotsByBotIdConnectorsApiKey({
      path: { bot_id: value.botId },
      body: {
        connector_type: value.connectorType,
        auth_method: value.authMethod,
        fields: value.fields,
      },
      throwOnError: true,
    })
    return data
  },
})

const connecting = ref(false)

function updateOpen(open: boolean) {
  if (!open && connecting.value) return
  emit('update:open', open)
}

async function connect() {
  if (connecting.value) return
  const method = selectedMethod.value
  const connectorType = props.connector?.type
  const oauthPopup = method?.type === 'oauth2'
    ? prepareConnectorOAuthPopup(t('common.loading'))
    : null
  if (method?.type === 'oauth2' && !oauthPopup && !window.api?.desktop?.openExternalUrl) {
    // window.open returned null: the browser blocked the popup. Bail before
    // the create POST so no pending remote connection is orphaned. (Desktop
    // returns null by design — the URL opens in the system browser instead.)
    toast.error(t('connectors.oauthPopupBlocked'))
    return
  }
  connecting.value = true
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
        form.setFieldError(`fields.${field.key}`, t('connectors.validation.fieldRequired', {
          field: field.label || field.key,
        }))
        credentialValid = false
      }
      if (value && field.pattern && !new RegExp(field.pattern).test(value)) {
        form.setFieldError(`fields.${field.key}`, t('connectors.validation.fieldInvalid', {
          field: field.label || field.key,
        }))
        credentialValid = false
      }
    }
    if (!credentialValid) {
      oauthPopup?.close()
      return
    }

    const botId = String(form.values.bot_id ?? '')
    if (method.type === 'oauth2') {
      const result = await createOAuthMutation.mutateAsync({
        botId,
        connectorType,
        authMethod: method.key,
      })
      const connectionId = result.connection_id
      if (!result.authorization_url || !connectionId) throw new Error('oauth_failed')
      await openConnectorOAuthURL(result.authorization_url, oauthPopup)
      await waitForConnectorOAuth(botId, connectionId, oauthPopup)
    } else {
      await createCredentialMutation.mutateAsync({
        botId,
        connectorType,
        authMethod: method.key,
        fields: Object.fromEntries(
          Object.entries(form.values.fields ?? {}).map(([key, value]) => [key, String(value ?? '').trim()]),
        ),
      })
    }
    toast.success(t('connectors.connectedSuccess', { name: props.connector.name }))
    emit('update:open', false)
    emit('connected', botId)
  } catch (error) {
    oauthPopup?.close()
    const oauthKey = connectorOAuthErrorKey(error)
    toast.error(oauthKey ? t(oauthKey) : resolveApiErrorMessage(error, t('connectors.connectFailed')))
  } finally {
    connecting.value = false
  }
}
</script>
