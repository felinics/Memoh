<script setup lang="ts">
// Version selection and exact-target confirmation share one dialog. Preparing
// resolves metadata only; submitting the reviewed target authorizes mutation.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import {
  Button,
  CalloutBanner,
  InlineLoadingRow,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  FieldStack,
  FormField,
  FormControl,
  Input,
} from '@felinic/ui'
import {
  prepareDependencyInstallation,
  prepareDependencyRepair,
  type DependencyItem,
  type DependencyOperationAction,
  type DependencyPreparedInstallation,
} from '@/composables/api/useWorkspaceDependencies'
import { resolveApiErrorMessage } from '@/utils/api-error'
import DependencyKvList from './dependency-kv-list.vue'
import {
  formatDependencyVersion,
  validDependencyVersion,
  type DependencyConfirmMode,
} from '@/utils/workspace-dependency'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'

const props = withDefaults(defineProps<{
  open: boolean
  botId: string
  operation: DependencyOperationAction
  mode: DependencyConfirmMode
  item: DependencyItem | null

  loading?: boolean
  /** Overrides the confirm label (the enable flow says "Install and enable"). */
  confirmLabel?: string
}>(), {
  loading: false,
  confirmLabel: '',
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  /** Exact release and recipe the user reviewed. */
  confirm: [target: DependencyPreparedInstallation]
}>()

const { t } = useI18n()
const { dependencyName } = useWorkspaceDependencyText()

const name = computed(() => (props.item ? dependencyName(props.item) : ''))
const installedVersion = computed(() => formatDependencyVersion(props.item?.installed_version))

const prepared = ref<DependencyPreparedInstallation | null>(null)
const preparing = ref(false)
const prepareError = ref('')
let prepareSequence = 0
onBeforeUnmount(() => { prepareSequence++ })
const busy = computed(() => props.loading || preparing.value)

// Reinstall starts at the authorized or installed release; selecting an older
// release uses the same reviewed installation flow.
const form = useForm({
  validationSchema: computed(() => toTypedSchema(z.object({
    version: z.string().trim().refine(validDependencyVersion, t('bots.dependencies.confirm.versionInvalid')),
  }))),
  initialValues: { version: '' },
})
watch(() => [props.open, props.botId, props.item?.id, props.mode, props.item?.definition_revision], () => {
  prepareSequence++
  preparing.value = false
  prepared.value = null
  prepareError.value = ''
  if (!props.open) return
  const version = props.mode === 'update'
    ? props.item?.latest_version ?? ''
    : props.mode === 'reinstall' || props.mode === 'authorizeRepair'
      ? props.item?.desired?.version || props.item?.installed_version || ''
      : ''
  form.resetForm({ values: { version } })
}, { immediate: true, flush: 'sync' })

watch(() => form.values.version, () => {
  prepareSequence++
  preparing.value = false
  prepared.value = null
  prepareError.value = ''
}, { flush: 'sync' })

const targetRows = computed(() => [
  { label: t('bots.dependencies.confirm.version'), value: prepared.value?.version, mono: true },
  { label: t('bots.dependencies.confirm.source'), value: prepared.value?.source_url },
  { label: t('bots.dependencies.script.revision'), value: prepared.value?.definition_revision, mono: true },
])

const title = computed(() => {
  const args = { name: name.value }
  switch (props.mode) {
    case 'authorizeRepair':
      return t('bots.dependencies.repair.authorizeTitle', args)
    case 'reinstall':
      return t('bots.dependencies.confirm.reinstallTitle', args)
    case 'update':
      return t('bots.dependencies.confirm.updateTitle', args)
    default:
      return t('bots.dependencies.confirm.installTitle', args)
  }
})

const description = computed(() => {
  switch (props.mode) {
    case 'authorizeRepair':
      return t('bots.dependencies.repair.authorizeDescription', { name: name.value })
    case 'reinstall':
      return t('bots.dependencies.confirm.reinstallDescription', { name: name.value })
    case 'update':
      return t('bots.dependencies.confirm.updateDescription', { from: installedVersion.value })
    default:
      return t('bots.dependencies.confirm.installDescription', { name: name.value })
  }
})

const confirmText = computed(() => {
  if (!prepared.value) return t('bots.dependencies.confirm.review')
  if (props.confirmLabel) return props.confirmLabel
  if (props.mode === 'authorizeRepair') return t('bots.dependencies.repair.authorizeConfirm')
  switch (props.mode) {
    case 'reinstall':
      return t('bots.dependencies.action.reinstall')
    case 'update':
      return t('bots.dependencies.confirm.updateConfirm')
    default:
      return t('bots.dependencies.confirm.installConfirm')
  }
})

function onOpenChange(value: boolean) {
  // The request is in flight once confirmed; closing would orphan the stream.
  if (!value && props.loading) return
  emit('update:open', value)
}

const submit = form.handleSubmit(async ({ version }) => {
  if (busy.value || !props.item?.id || !props.botId) return
  if (prepared.value) {
    emit('confirm', prepared.value)
    return
  }
  const sequence = ++prepareSequence
  preparing.value = true
  prepareError.value = ''
  try {
    const target = props.mode === 'authorizeRepair'
      ? await prepareDependencyRepair(props.botId, props.item.id, version, props.item.definition_revision ?? '')
      : await prepareDependencyInstallation(props.botId, props.item.id, props.operation, version, props.item.definition_revision ?? '')
    if (sequence !== prepareSequence || !props.open) return
    if (!target.version || !target.definition_revision) {
      prepareError.value = t('bots.dependencies.confirm.prepareFailed')
      return
    }
    prepared.value = target
  } catch (error) {
    if (sequence === prepareSequence) prepareError.value = resolveApiErrorMessage(error, t('bots.dependencies.confirm.prepareFailed'))
  } finally {
    if (sequence === prepareSequence) preparing.value = false
  }
})
</script>

<template>
  <Dialog
    :open="open"
    @update:open="onOpenChange"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ title }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ description }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0 space-y-4">
        <form
          id="dependency-confirm-form"
          @submit.prevent="submit"
        >
          <FormField
            v-slot="{ componentField }"
            name="version"
          >
            <FieldStack
              :label="t('bots.dependencies.confirm.version')"
              :help="t('bots.dependencies.confirm.versionHelp')"
            >
              <FormControl>
                <Input
                  v-bind="componentField"
                  class="font-mono"
                  :placeholder="t('bots.dependencies.confirm.versionPlaceholder')"
                  autocomplete="off"
                  spellcheck="false"
                  :disabled="busy"
                />
              </FormControl>
            </FieldStack>
          </FormField>
        </form>
        <InlineLoadingRow v-if="preparing">
          {{ t('bots.dependencies.confirm.preparing') }}
        </InlineLoadingRow>
        <CalloutBanner
          v-else-if="prepareError"
          tone="destructive"
          :title="prepareError"
        />
        <template v-else-if="prepared">
          <DependencyKvList :rows="targetRows" />
          <p class="text-body text-muted-foreground">
            {{ t('bots.dependencies.confirm.repairConsent', { version: prepared.version }) }}
          </p>
        </template>
      </DialogBody>

      <DialogFooter class="min-w-0 items-center gap-2">
        <Button
          variant="outline"
          :disabled="loading"
          @click="emit('update:open', false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          form="dependency-confirm-form"
          type="submit"
          :loading="busy"
        >
          {{ confirmText }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
