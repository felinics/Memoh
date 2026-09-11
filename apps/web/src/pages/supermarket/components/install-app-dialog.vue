<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogContent class="sm:max-w-lg">
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
      <div class="space-y-4 py-2">
        <FieldStack :label="$t('supermarket.selectBot')">
          <BotSelect
            v-model="selectedBotId"
            trigger-class="w-full"
          />
        </FieldStack>
        <FieldStack
          v-if="targets.length > 1"
          :label="$t('supermarket.selectWorkspaceTarget')"
        >
          <Select
            :model-value="displayTargetId"
            @update:model-value="onTargetChange"
          >
            <SelectTrigger class="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="target in targets"
                :key="target.target_id"
                :value="target.target_id"
                :disabled="!workspaceTargetAvailable(target)"
              >
                {{ workspaceTargetName(target, t) }}
              </SelectItem>
            </SelectContent>
          </Select>
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
              v-for="dep in pkg.dependencies"
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
      </div>
      <DialogFooter>
        <DialogClose as-child>
          <Button variant="outline">
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          :disabled="!selectedBotId || !pkg?.revision"
          @click="handleInstall"
        >
          {{ $t('supermarket.install') }}
        </Button>
      </DialogFooter>
    </DialogContent>
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
    :done-label="$t('apps.viewBotApps')"
    @update:open="setProgressOpen"
    @retry="retry"
    @done="openBotApps"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useQuery } from '@pinia/colada'
import { BrainCircuit, Package as App, Plug } from 'lucide-vue-next'
import {
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  FieldStack,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@felinic/ui'
import {
  getBotsByBotIdWorkspaceTargets,
  getConnectorsCatalog,
  type HandlersSupermarketAppDescriptor,
  type WorkspaceWorkspaceTarget,
} from '@memohai/sdk'
import BotSelect from '@/components/bot-select/index.vue'
import AppProgressDialog from '@/pages/bots/components/app-progress-dialog.vue'
import { useAppOperation } from '@/pages/bots/composables/useAppOperation'
import { prepareConnectorOAuthPopup } from '@/composables/useConnectorOAuth'
import { appDisplayName } from '@/composables/api/useApps'
import { workspaceTargetAvailable, workspaceTargetName } from '@/utils/workspace-target'

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & { target_id: string }

const props = defineProps<{
  open: boolean
  pkg: HandlersSupermarketAppDescriptor | null
  defaultBotId?: string
}>()
const emit = defineEmits<{
  'update:open': [open: boolean]
  'installed': [botId: string]
}>()
const { t, locale } = useI18n()
const router = useRouter()
const selectedBotId = ref('')
const selectedTargetId = ref('')

const { data: targetsResponse } = useQuery({
  key: () => ['bot-workspace-targets', selectedBotId.value],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({ path: { bot_id: selectedBotId.value }, throwOnError: true })
    return data
  },
  enabled: () => !!selectedBotId.value,
})
const targets = computed<ValidWorkspaceTarget[]>(() => (
  (targetsResponse.value?.targets ?? []).filter(
    (target): target is ValidWorkspaceTarget => typeof target.target_id === 'string' && target.target_id.length > 0,
  )
))
const primaryTargetId = computed(() => targets.value.find(target => target.primary)?.target_id ?? targets.value[0]?.target_id ?? '')
const displayTargetId = computed(() => selectedTargetId.value || primaryTargetId.value)

function onTargetChange(value: unknown) {
  const next = typeof value === 'string' ? value : ''
  selectedTargetId.value = next === primaryTargetId.value ? '' : next
}

watch(() => props.open, (open) => {
  if (open) {
    selectedBotId.value = props.defaultBotId || ''
    selectedTargetId.value = ''
  }
})
watch(selectedBotId, () => {
  selectedTargetId.value = ''
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
  if (!selectedBotId.value || !pkg?.registry_id || !pkg.app_id || !pkg.revision) return
  closePopup()
  const firstConnector = pkg.connectors[0]
  const method = catalogQuery.data.value?.find(item => item.type === firstConnector?.type)?.auth_methods?.[0]
  if (method?.type === 'oauth2') oauthPopup.value = prepareConnectorOAuthPopup(t('common.loading'))
  const started = start({
    targetId: selectedTargetId.value,
    registryId: pkg.registry_id,
    appId: pkg.app_id,
    name: appDisplayName(pkg, locale.value),
    action: 'install',
    install: {
      registryId: pkg.registry_id,
      appId: pkg.app_id,
      revision: pkg.revision,
      workspaceTargetId: selectedTargetId.value || undefined,
    },
  })
  if (started) emit('update:open', false)
  else closePopup()
}

function openBotApps() {
  const botId = selectedBotId.value
  if (!botId) return
  emit('installed', botId)
  void router.push({ name: 'bot-detail', params: { botName: botId }, query: { tab: 'apps' } }).catch(() => {})
}
</script>
