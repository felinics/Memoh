<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Alert, AlertDescription, AlertTitle, Button, Dialog, DialogBody,
  DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogTitle, toast,
} from '@felinic/ui'
import type { ConnectitConnector } from '@memohai/sdk'
import type { AppConnectorItem } from '@/composables/api/useApps'
import AppConnectorAuthForm from './app-connector-auth-form.vue'

const props = defineProps<{
  open: boolean
  botId: string
  installationId: string
  appName: string
  connector: AppConnectorItem | null
  catalog: ConnectitConnector | undefined
}>()
const emit = defineEmits<{
  'update:open': [open: boolean]
  authorized: []
}>()
const { t } = useI18n()
const authForm = ref<InstanceType<typeof AppConnectorAuthForm> | null>(null)
function updateOpen(open: boolean) {
  if (!open && authForm.value?.phase === 'submitting') return
  if (!open) authForm.value?.cancel()
  emit('update:open', open)
}
function authorized() {
  toast.success(t('connectors.connectedSuccess', { name: props.catalog?.name || props.connector?.type }))
  emit('update:open', false)
  emit('authorized')
}
</script>

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
        <DialogTitle>{{ t('connectors.connectTitle', { name: catalog?.name || connector?.type || t('connectors.unknown') }) }}</DialogTitle>
        <DialogDescription>{{ catalog?.description || t('apps.connector.authDescription', { name: appName }) }}</DialogDescription>
      </DialogHeader>
      <DialogBody>
        <AppConnectorAuthForm
          v-if="open && catalog"
          ref="authForm"
          :bot-id="botId"
          :installation-id="installationId"
          :connector="connector"
          :catalog="catalog"
          @authorized="authorized"
        />
        <Alert v-else>
          <AlertTitle>{{ t('apps.connector.unavailableTitle') }}</AlertTitle>
          <AlertDescription>{{ t('apps.connector.unavailableDescription') }}</AlertDescription>
        </Alert>
      </DialogBody>
      <DialogFooter>
        <Button
          variant="outline"
          :disabled="authForm?.phase === 'submitting'"
          @click="updateOpen(false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          v-if="catalog"
          :loading="!!authForm && authForm.phase !== 'idle'"
          @click="authForm?.connect()"
        >
          {{ authForm?.phase === 'awaiting-oauth' ? t('connectors.awaitingAuthorization') : t('connectors.connect') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
