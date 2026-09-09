<script setup lang="ts">
// Confirmation before a streamed dependency operation starts. Three modes share
// one shell — install, update, reinstall — and every one takes an optional
// version: blank means the latest the catalog resolves, a typed one pins the
// run. A remote target always gets the explicit "runs on your computer"
// warning. Download size is not shown: the API does not report
// it, and an estimate would be invented.
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import {
  Button,
  CalloutBanner,
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
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import {
  formatDependencyVersion,
  validDependencyVersion,
  type DependencyConfirmMode,
} from '@/utils/workspace-dependency'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'

const props = withDefaults(defineProps<{
  open: boolean
  mode: DependencyConfirmMode
  item: DependencyItem | null
  targetKind: 'native' | 'remote'
  /** Display name identifying the computer where the script will run. */
  targetName?: string
  loading?: boolean
  /** Overrides the confirm label (the enable flow says "Install and enable"). */
  confirmLabel?: string
}>(), {
  targetName: '',
  loading: false,
  confirmLabel: '',
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  /** The trimmed version the user typed; empty means the latest. */
  confirm: [version: string]
}>()

const { t } = useI18n()
const { dependencyName } = useWorkspaceDependencyText()

const name = computed(() => (props.item ? dependencyName(props.item) : ''))
const installedVersion = computed(() => formatDependencyVersion(props.item?.installed_version))

// Each opening starts blank: a version typed for one row must not leak into
// the next confirmation.
const form = useForm({
  validationSchema: computed(() => toTypedSchema(z.object({
    version: z.string().trim().refine(validDependencyVersion, t('bots.dependencies.confirm.versionInvalid')),
  }))),
  initialValues: { version: '' },
})
watch(() => props.open, (open) => {
  if (open) form.resetForm()
})

const title = computed(() => {
  const args = { name: name.value }
  switch (props.mode) {
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
    case 'reinstall':
      return t('bots.dependencies.confirm.reinstallDescription', { name: name.value })
    case 'update':
      return t('bots.dependencies.confirm.updateDescription', { from: installedVersion.value })
    default:
      return t('bots.dependencies.confirm.installDescription', { name: name.value })
  }
})

const confirmText = computed(() => {
  if (props.confirmLabel) return props.confirmLabel
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

const submit = form.handleSubmit(({ version }) => {
  if (props.loading) return
  emit('confirm', version)
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
                  :disabled="loading"
                />
              </FormControl>
            </FieldStack>
          </FormField>
        </form>

        <CalloutBanner
          v-if="targetKind === 'remote'"
          tone="warning"
          :title="t('bots.dependencies.confirm.remoteWarningTitle', { name: targetName })"
          :description="t('bots.dependencies.confirm.remoteWarningDescription')"
        />
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
          :loading="loading"
        >
          {{ confirmText }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
