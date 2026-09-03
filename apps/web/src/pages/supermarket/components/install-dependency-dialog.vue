<template>
  <!-- Step 1: where it goes. Bot, then the workspace target only when the bot
       has more than one, then an optional version (blank = latest). -->
  <Dialog
    :open="open && !progressOpen"
    @update:open="onFormOpenChange"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader>
        <DialogTitle>{{ t('supermarket.dependencyInstallTitle', { name }) }}</DialogTitle>
        <DialogDescription v-if="description">
          {{ description }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <form
          id="install-dependency-form"
          @submit.prevent="startInstall"
        >
          <FormStack>
            <FieldStack :label="t('supermarket.selectBot')">
              <BotSelect
                v-model="botId"
                trigger-class="w-full"
              />
            </FieldStack>

            <FieldStack
              v-if="targets.length > 1"
              :label="t('supermarket.selectWorkspaceTarget')"
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
                    <span
                      v-if="!workspaceTargetAvailable(target)"
                      class="text-caption text-muted-foreground"
                    >
                      {{ workspaceTargetStatusLabel(target, t) }}
                    </span>
                  </SelectItem>
                </SelectContent>
              </Select>
            </FieldStack>

            <FieldStack
              :label="t('bots.dependencies.confirm.version')"
              :help="t('bots.dependencies.confirm.versionHelp')"
            >
              <Input
                v-model="version"
                class="font-mono"
                :placeholder="t('bots.dependencies.confirm.versionPlaceholder')"
                autocomplete="off"
                spellcheck="false"
              />
            </FieldStack>
          </FormStack>
        </form>
      </DialogBody>

      <DialogFooter>
        <DialogClose as-child>
          <Button variant="outline">
            {{ t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          form="install-dependency-form"
          type="submit"
          :disabled="!botId"
        >
          {{ t('supermarket.install') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <!-- Step 2: the streamed install, same log as the bot's Dependencies tab.
       "Done" leads to that tab so the new row is the next thing seen. -->
  <DependencyProgressDialog
    :open="progressOpen"
    :title="t('bots.dependencies.progress.installing', { name })"
    :lines="lines"
    :status="progressStatus"
    :error="progressError"
    :result-version="resultVersion"
    :entrypoint="entrypoint"
    :done-label="t('supermarket.viewBotDependencies')"
    @update:open="onProgressOpenChange"
    @retry="consume"
    @done="onDone"
  />
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
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
  getBotsByBotIdWorkspaceTargets,
  type HandlersWorkspaceDependencyCatalogItem,
  type WorkspaceWorkspaceTarget,
} from '@memohai/sdk'
import BotSelect from '@/components/bot-select/index.vue'
import { invalidateBotDependencies } from '@/composables/api/useWorkspaceDependencies'
import { streamDependencyOperation } from '@/composables/api/useWorkspaceDependencyStream'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import DependencyProgressDialog from '@/pages/bots/components/dependency-progress-dialog.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  formatDependencyVersion,
  type DependencyLogLine,
  type DependencyProgressStatus,
} from '@/utils/workspace-dependency'
import {
  workspaceTargetAvailable,
  workspaceTargetName,
  workspaceTargetStatusLabel,
} from '@/utils/workspace-target'

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & { target_id: string }

const props = defineProps<{
  open: boolean
  item: HandlersWorkspaceDependencyCatalogItem | null
  defaultBotId?: string
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  /** The install finished and the user asked to see the bot's dependencies. */
  installed: [botId: string]
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const { dependencyName, dependencyDescription } = useWorkspaceDependencyText()

const name = computed(() => (props.item ? dependencyName(props.item) : ''))
const description = computed(() => (props.item ? dependencyDescription(props.item) : ''))

// ---- Form ---------------------------------------------------------------------

const botId = ref('')
const version = ref('')

// '' means the bot's current target: the Server resolves it, the same way the
// bot's Dependencies tab does when nothing is picked.
const selectedTargetId = ref('')

const { data: targetsResponse } = useQuery({
  key: () => ['bot-workspace-targets', botId.value],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: botId.value },
      throwOnError: true,
    })
    return data
  },
  enabled: () => props.open && !!botId.value,
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

watch(botId, () => {
  selectedTargetId.value = ''
})

watch(() => props.open, (open) => {
  if (!open) return
  botId.value = props.defaultBotId || ''
  selectedTargetId.value = ''
  version.value = ''
}, { immediate: true })

function onFormOpenChange(open: boolean) {
  emit('update:open', open)
}

// ---- Streamed install -----------------------------------------------------------

const progressOpen = ref(false)
const progressStatus = ref<DependencyProgressStatus>('running')
const progressError = ref('')
const lines = ref<DependencyLogLine[]>([])
const resultVersion = ref('')
const entrypoint = ref('')
// Frozen at start so a bot picked for the next install cannot redirect a
// running stream's invalidation or the "view dependencies" link.
let request = { botId: '', targetId: '', depId: '', version: '' }

function startInstall() {
  const depId = props.item?.id ?? ''
  if (!botId.value || !depId) return
  request = { botId: botId.value, targetId: selectedTargetId.value, depId, version: version.value.trim() }
  void consume()
}

async function consume() {
  lines.value = []
  progressStatus.value = 'running'
  progressError.value = ''
  resultVersion.value = ''
  entrypoint.value = ''
  progressOpen.value = true
  let sequence = 0
  try {
    const stream = streamDependencyOperation(request.botId, request.depId, 'install', request.targetId || undefined, { version: request.version })
    for await (const event of stream) {
      switch (event.type) {
        case 'log':
          lines.value.push({ id: sequence++, stream: event.stream, data: event.data })
          break
        case 'done':
          resultVersion.value = formatDependencyVersion(event.version)
          entrypoint.value = Object.values(event.entrypoints ?? {})[0] ?? ''
          progressStatus.value = 'done'
          break
        case 'error':
          progressError.value = event.message
          progressStatus.value = 'error'
          break
      }
    }
    // A stream that closes without a verdict is a failure the user must see.
    if (progressStatus.value === 'running') {
      progressError.value = t('bots.dependencies.progress.failedTitle')
      progressStatus.value = 'error'
    }
  } catch (error) {
    progressError.value = resolveApiErrorMessage(error, t('bots.dependencies.progress.failedTitle'))
    progressStatus.value = 'error'
  } finally {
    void invalidateBotDependencies(queryCache, request.botId)
    if (props.item?.category === 'agent') {
      void queryCache.invalidateQueries({ key: ['bot-agents', request.botId] })
    }
    if (progressStatus.value === 'done') {
      toast.success(t('supermarket.dependencyInstalled', { name: name.value }))
    }
  }
}

// The progress dialog refuses to close while running, so a close is a verdict;
// either way the whole flow is over and the form does not come back.
function onProgressOpenChange(open: boolean) {
  if (open) return
  progressOpen.value = false
  emit('update:open', false)
}

function onDone() {
  emit('installed', request.botId)
}
</script>
