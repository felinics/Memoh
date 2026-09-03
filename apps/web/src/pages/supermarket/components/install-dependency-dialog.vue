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
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ t('supermarket.dependencyInstallTitle', { name }) }}
        </DialogTitle>
        <DialogDescription
          v-if="description"
          class="break-words"
        >
          {{ description }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0">
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
                :disabled="resumable"
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
                :disabled="resumable"
              />
            </FieldStack>
          </FormStack>
        </form>
      </DialogBody>

      <DialogFooter class="min-w-0">
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
          {{ resumable ? t('bots.dependencies.action.viewProgress') : t('supermarket.install') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <!-- Step 2: the streamed install, same log as the bot's Dependencies tab.
       "Done" leads to that tab so the new row is the next thing seen. -->
  <DependencyProgressDialog
    :open="progressOpen"
    :name="name"
    action="install"
    :lines="displayed?.lines ?? []"
    :status="displayed?.status ?? 'running'"
    :error="displayed?.error"
    :result-version="displayed?.resultVersion"
    :entrypoint="displayed?.entrypoint"
    :done-label="t('supermarket.viewBotDependencies')"
    @update:open="onProgressOpenChange"
    @retry="retry"
    @done="onDone"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
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
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import DependencyProgressDialog from '@/pages/bots/components/dependency-progress-dialog.vue'
import { useDependencyOperationsStore, type DependencyOperation } from '@/store/dependency-operations'
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
const store = useDependencyOperationsStore()
const { dependencyName, dependencyDescription } = useWorkspaceDependencyText()

const name = computed(() => (props.item ? dependencyName(props.item) : ''))
const description = computed(() => (props.item ? dependencyDescription(props.item) : ''))
const depId = computed(() => props.item?.id ?? '')

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

// The picked bot is already installing this dependency (the dialog was sent
// to the background earlier): the submit button reopens that log instead of
// sending a second install the Server would refuse as busy.
const resumable = computed(() => store.get(botId.value, depId.value)?.status === 'running')

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
  // Coming back for a dependency whose install is still streaming for the
  // preselected bot skips the form: there is nothing left to choose.
  const running = store.get(botId.value, depId.value)
  if (running?.status === 'running') show(running)
}, { immediate: true })

function onFormOpenChange(open: boolean) {
  emit('update:open', open)
}

// ---- Streamed install -----------------------------------------------------------

const VIEWER_ID = 'supermarket-install-dependency'
const progressOpen = ref(false)
// Kept across close so the dialog fades out with its content intact even
// after the store dropped the record.
const displayed = shallowRef<DependencyOperation | null>(null)

function show(operation: DependencyOperation) {
  displayed.value = operation
  progressOpen.value = true
  store.view(operation.key, VIEWER_ID)
}

function startInstall() {
  const item = props.item
  if (!item || !botId.value || !depId.value) return
  const result = store.start({
    botId: botId.value,
    targetId: selectedTargetId.value,
    item,
    action: 'install',
    version: version.value,
  })
  switch (result.kind) {
    case 'started':
    case 'running':
      show(result.operation)
      return
    case 'busy':
      toast.error(t('bots.dependencies.busy'))
      return
    default:
      return
  }
}

function retry() {
  if (displayed.value) store.retry(displayed.value.key)
}

// Closing while running sends the install to the background (the store keeps
// the stream and toasts the outcome); closing afterwards forgets it. Either
// way the whole flow is over and the form does not come back.
function onProgressOpenChange(open: boolean) {
  if (open) return
  progressOpen.value = false
  if (displayed.value) store.unview(displayed.value.key, VIEWER_ID)
  emit('update:open', false)
}

function onDone() {
  if (displayed.value) emit('installed', displayed.value.botId)
}

onBeforeUnmount(() => {
  if (progressOpen.value && displayed.value) store.unview(displayed.value.key, VIEWER_ID)
})
</script>
