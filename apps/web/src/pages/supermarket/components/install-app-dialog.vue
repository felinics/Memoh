<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogPanel
      width="xl"
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
        <div class="space-y-4 py-2">
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
            v-if="pkg"
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
        </div>
        <DependencyPlanPreview
          :plan="preview.plan.value"
          :loading="preview.loading.value"
          :error="preview.error.value"
          @retry="preview.retry"
        />
      </DialogBody>
      <DialogFooter>
        <DialogClose as-child>
          <Button variant="outline">
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          :disabled="!selectedBotId || !pkg?.revision || !preview.ready.value"
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
import { BrainCircuit, Plug } from 'lucide-vue-next'
import {
  Button,
  Dialog,
  DialogClose,
  DialogPanel,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  FieldStack,
} from '@felinic/ui'
import {
  getConnectorsCatalog,
  type HandlersSupermarketAppDescriptor,
} from '@memohai/sdk'
import DependencyPlanPreview from '@/pages/bots/components/dependency-plan-preview.vue'
import { useDependencyPlan } from '@/composables/useDependencyPlan'
import BotSelect from '@/components/bot-select/index.vue'
import AppProgressDialog from '@/pages/bots/components/app-progress-dialog.vue'
import { useAppOperation } from '@/pages/bots/composables/useAppOperation'
import { prepareConnectorOAuthPopup } from '@/composables/useConnectorOAuth'
import { appDisplayName } from '@/composables/api/useApps'

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
const preview = useDependencyPlan(selectedBotId, () => props.open && props.pkg ? {
  app: { action: 'install', registry_id: props.pkg.registry_id, app_id: props.pkg.app_id, revision: props.pkg.revision },
} : null)
watch(() => props.open, (open) => {
  if (open) {
    selectedBotId.value = props.defaultBotId || ''
  }
})

const { active, progressOpen, start, retry: retryCurrent, setProgressOpen } = useAppOperation(selectedBotId, 'supermarket-install')

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
  if (!preview.ready.value || !selectedBotId.value || !pkg?.registry_id || !pkg.app_id || !pkg.revision) return
  closePopup()
  const firstConnector = pkg.connectors[0]
  const method = catalogQuery.data.value?.find(item => item.type === firstConnector?.type)?.auth_methods?.[0]
  if (method?.type === 'oauth2') oauthPopup.value = prepareConnectorOAuthPopup(t('common.loading'))
  const started = start({
    registryId: pkg.registry_id,
    appId: pkg.app_id,
    name: appDisplayName(pkg, locale.value),
    action: 'install',
    planId: preview.plan.value?.id,
    install: {
      registryId: pkg.registry_id,
      appId: pkg.app_id,
      revision: pkg.revision,
    },
  })
  if (started) emit('update:open', false)
  else closePopup()
}

function retry() {
  if (active.value?.needsReview) { setProgressOpen(false); emit('update:open', true) }
  else retryCurrent()
}

function openBotApps() {
  const botId = selectedBotId.value
  if (!botId) return
  emit('installed', botId)
  if (props.lockBot) return
  void router.push({ name: 'bot-detail', params: { botName: botId }, query: { tab: 'apps' } }).catch(() => {})
}
</script>
