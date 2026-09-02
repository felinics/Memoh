<script setup lang="ts">
// Confirmation before a streamed dependency operation starts. Four modes share
// one shell; what changes is the copy: an align must name where the version
// requirement comes from (the Server pin, design §10.1), a tool update states
// only the version pair and the rollback guarantee (WD-UPD-006), and a remote
// target always gets the explicit "runs on your computer" warning (WD-PLAT-001).
// Download size is not shown: the API does not report it, and an estimate
// would be invented.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
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
  TextButton,
} from '@felinic/ui'
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import {
  dependencyDisplayName,
  formatDependencyVersion,
  type DependencyConfirmMode,
} from '@/utils/workspace-dependency'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'

const props = withDefaults(defineProps<{
  open: boolean
  mode: DependencyConfirmMode
  item: DependencyItem | null
  targetKind: 'native' | 'remote'
  /** Display name of a remote target, for the WD-PLAT-001 warning. */
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
  confirm: []
  viewScript: []
}>()

const { t } = useI18n()

const name = computed(() => (props.item ? dependencyDisplayName(props.item) : ''))
const installedVersion = computed(() => formatDependencyVersion(props.item?.installed_version))
const requiredVersion = computed(() => formatDependencyVersion(props.item?.required_version))
const latestVersion = computed(() => formatDependencyVersion(props.item?.latest_version))

// The version the operation lands on: agents follow the Server pin, tools the
// last upstream check; an install without either resolves to "latest".
const targetVersion = computed(() => {
  switch (props.mode) {
    case 'align':
      return requiredVersion.value
    case 'update':
      return latestVersion.value || requiredVersion.value
    default:
      return requiredVersion.value || latestVersion.value
  }
})

const title = computed(() => {
  const args = { name: name.value }
  switch (props.mode) {
    case 'reinstall':
      return t('bots.dependencies.confirm.reinstallTitle', args)
    case 'update':
      return t('bots.dependencies.confirm.updateTitle', args)
    case 'align':
      return t('bots.dependencies.confirm.alignTitle', args)
    default:
      return t('bots.dependencies.confirm.installTitle', args)
  }
})

const description = computed(() => {
  switch (props.mode) {
    case 'reinstall':
      return t('bots.dependencies.confirm.reinstallDescription', { name: name.value })
    case 'update':
      return t('bots.dependencies.confirm.updateDescription', {
        from: installedVersion.value,
        to: targetVersion.value,
      })
    case 'align':
      return t('bots.dependencies.confirm.alignDescription', {
        name: name.value,
        version: targetVersion.value,
      })
    default:
      return targetVersion.value
        ? t('bots.dependencies.confirm.installDescription', { name: name.value, version: targetVersion.value })
        : t('bots.dependencies.confirm.installLatestDescription', { name: name.value })
  }
})

const rows = computed<DependencyKvRow[]>(() => [
  { label: t('bots.dependencies.confirm.dependency'), value: props.item?.id, mono: true },
  { label: t('bots.dependencies.confirm.targetVersion'), value: targetVersion.value, mono: true },
  { label: t('bots.dependencies.confirm.installPath'), value: props.item?.install_path, mono: true },
])

const confirmText = computed(() => {
  if (props.confirmLabel) return props.confirmLabel
  switch (props.mode) {
    case 'reinstall':
      return t('bots.dependencies.action.reinstall')
    case 'update':
      return t('bots.dependencies.confirm.updateConfirm')
    case 'align':
      return t('bots.dependencies.confirm.alignConfirm', { version: targetVersion.value })
    default:
      return t('bots.dependencies.confirm.installConfirm')
  }
})

function onOpenChange(value: boolean) {
  // The request is in flight once confirmed; closing would orphan the stream.
  if (!value && props.loading) return
  emit('update:open', value)
}
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
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription>{{ description }}</DialogDescription>
      </DialogHeader>

      <DialogBody class="space-y-4">
        <DependencyKvList :rows="rows" />

        <CalloutBanner
          v-if="targetKind === 'remote'"
          tone="warning"
          :title="t('bots.dependencies.confirm.remoteWarningTitle', { name: targetName })"
          :description="t('bots.dependencies.confirm.remoteWarningDescription')"
        />
      </DialogBody>

      <DialogFooter class="items-center gap-2 sm:justify-between">
        <TextButton
          :disabled="loading"
          @click="emit('viewScript')"
        >
          {{ t('bots.dependencies.action.viewScript') }}
        </TextButton>
        <div class="flex items-center gap-2">
          <Button
            variant="outline"
            :disabled="loading"
            @click="emit('update:open', false)"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            :loading="loading"
            @click="emit('confirm')"
          >
            {{ confirmText }}
          </Button>
        </div>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
