<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader>
        <DialogTitle>{{ $t('supermarket.appInstallTitle') }}</DialogTitle>
        <DialogDescription v-if="pkg">
          {{ appDisplayName(pkg, locale) }}
          <span
            v-if="pkg.version"
            class="font-mono"
          >v{{ pkg.version }}</span>
        </DialogDescription>
      </DialogHeader>
      <DialogBody class="space-y-4">
        <FieldStack
          v-if="!lockBot"
          :label="$t('supermarket.selectBot')"
        >
          <BotSelect
            v-model="selectedBotId"
            trigger-class="w-full"
          />
        </FieldStack>

        <!-- What installing does, before it does it: Skills are files, a
             dependency runs a script, a connector needs the user's authorization. -->
        <div
          v-if="pkg && (!prepared || pkg.skills.length || pkg.connectors.length)"
          class="space-y-2 rounded-md border border-border p-3 text-xs"
        >
          <p class="font-medium">
            {{ $t('supermarket.installPreview') }}
          </p>
          <ul class="space-y-1 text-muted-foreground">
            <li
              v-if="pkg.skills.length"
              class="flex items-center gap-2"
            >
              <BrainCircuit class="size-3.5" />
              {{ $t('apps.section.skills', { count: pkg.skills.length }, pkg.skills.length) }}
            </li>
            <li
              v-for="dep in pkg.dependencies"
              v-show="!prepared"
              :key="dep"
              class="flex items-center gap-2"
            >
              <App class="size-3.5" />
              <span class="font-mono">{{ dep }}</span>
              <span>{{ $t('supermarket.installPreviewDependency') }}</span>
            </li>
            <li
              v-for="connector in pkg.connectors"
              :key="connector.type"
              class="flex items-center gap-2"
            >
              <Plug class="size-3.5" />
              <span class="font-mono">{{ connector.type }}</span>
              <span>{{ connector.required ? $t('supermarket.installPreviewConnector') : $t('supermarket.installPreviewConnectorOptional') }}</span>
            </li>
          </ul>
        </div>
        <AppDependencyConfirmations
          v-if="prepared"
          :dependencies="prepared.result.dependencies ?? []"
        />
        <p
          v-if="prepareError"
          role="alert"
          class="text-body text-destructive"
        >
          {{ prepareError }}
        </p>
      </DialogBody>
      <DialogFooter>
        <DialogClose as-child>
          <Button variant="outline">
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          v-if="!prepared"
          :disabled="!selectedBotId || !pkg?.revision"
          :loading="preparing"
          @click="prepare"
        >
          {{ $t('apps.prepare.review') }}
        </Button>
        <Button
          v-else
          :disabled="submitting || !prepared.result.revision"
          @click="handleInstall"
        >
          {{ $t('supermarket.install') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <AppProgressDialog
    :open="progressOpen"
    :operation="active"
    :oauth-popup="oauthPopup"
    :has-skills="!!pkg?.skills.length"
    :name="active?.name ?? ''"
    :action="active?.action ?? 'install'"
    :steps="active?.steps ?? []"
    :lines="active?.lines ?? []"
    :status="active?.status ?? 'running'"
    :result="active?.result"
    :error="active?.error"
    :done-label="lockBot ? $t('bots.dependencies.done') : $t('apps.viewBotApps')"
    @update:open="setProgressOpen"
    @retry="retry"
    @done="openBotApps"
  />
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useQuery } from '@pinia/colada'
import { BrainCircuit, Package as App, Plug } from 'lucide-vue-next'
import {
  Button,
  Dialog,
  DialogClose,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogPanel,
  FieldStack,
} from '@felinic/ui'
import {
  getConnectorsCatalog,
  type HandlersSupermarketAppDescriptor,
} from '@memohai/sdk'
import BotSelect from '@/components/bot-select/index.vue'
import AppProgressDialog from '@/pages/bots/components/app-progress-dialog.vue'
import { useAppOperation } from '@/pages/bots/composables/useAppOperation'
import { prepareConnectorOAuthPopup } from '@/composables/useConnectorOAuth'
import { appDisplayName } from '@/composables/api/useApps'
import { useAppPreparation } from '@/pages/bots/composables/useAppPreparation'
import AppDependencyConfirmations from '@/pages/bots/components/app-dependency-confirmations.vue'

const props = defineProps<{
  open: boolean
  pkg: HandlersSupermarketAppDescriptor | null
  defaultBotId?: string
  /** Keep sidebar installations bound to their originating bot and finish in chat. */
  lockBot?: boolean
}>()
const emit = defineEmits<{
  'update:open': [open: boolean]
  'installed': [botId: string]
}>()
const { t, locale } = useI18n()
const router = useRouter()
const selectedBotId = ref('')
const submitting = ref(false)
watch([() => props.open, () => props.defaultBotId], ([open]) => {
  if (open) {
    selectedBotId.value = props.defaultBotId || ''
    submitting.value = false
  }
}, { immediate: true })

const { prepared, preparing, error: prepareError, prepare } = useAppPreparation(() => {
  const pkg = props.pkg
  if (!props.open || !selectedBotId.value || !pkg?.registry_id || !pkg.app_id || !pkg.revision) return null
  return {
    botId: selectedBotId.value,
    request: { action: 'install', registry_id: pkg.registry_id, app_id: pkg.app_id, revision: pkg.revision },
  }
})

const { active, progressOpen, start, retry, setProgressOpen } = useAppOperation(selectedBotId, 'supermarket-install')

const oauthPopup = shallowRef<Window | null>(null)
const catalogQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => (await getConnectorsCatalog({ throwOnError: true })).data,
  enabled: () => props.open && !!props.pkg?.connectors.length,
})
function closePopup() {
  oauthPopup.value?.close()
  oauthPopup.value = null
}
watch(progressOpen, open => { if (!open) closePopup() })
onBeforeUnmount(closePopup)

function handleInstall() {
  const pkg = props.pkg
  const confirmation = prepared.value
  if (submitting.value || !pkg || !confirmation?.result.revision || confirmation.request.action !== 'install') return
  submitting.value = true
  closePopup()
  const firstConnector = pkg.connectors[0]
  const method = catalogQuery.data.value?.find(item => item.type === firstConnector?.type)?.auth_methods?.[0]
  if (method?.type === 'oauth2') oauthPopup.value = prepareConnectorOAuthPopup(t('common.loading'))
  const started = start({
    registryId: confirmation.request.registry_id,
    appId: confirmation.request.app_id,
    name: appDisplayName(pkg, locale.value),
    action: 'install',
    install: {
      registryId: confirmation.request.registry_id,
      appId: confirmation.request.app_id,
      revision: confirmation.result.revision,
    },
    dependencyConfirmations: confirmation.result.dependencies ?? [],
  })
  if (started) emit('update:open', false)
  else closePopup()
  submitting.value = false
}

function openBotApps() {
  const botId = active.value?.botId
  if (!botId) return
  emit('installed', botId)
  if (props.lockBot) return
  void router.push({ name: 'bot-detail', params: { botName: botId }, query: { tab: 'apps' } }).catch(() => {})
}
</script>
