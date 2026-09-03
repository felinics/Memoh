<script setup lang="ts">
// Confirmation before a streamed dependency operation starts. Three modes share
// one shell — install, update, reinstall — and every one takes an optional
// version: blank means the latest the catalog resolves, a typed one pins the
// run. A remote target always gets the explicit "runs on your computer"
// warning (WD-PLAT-001). Download size is not shown: the API does not report
// it, and an estimate would be invented.
import { computed, ref, watch } from 'vue'
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
  FieldStack,
  Input,
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
  /** The trimmed version the user typed; empty means the latest. */
  confirm: [version: string]
  viewScript: []
}>()

const { t } = useI18n()

const name = computed(() => (props.item ? dependencyDisplayName(props.item) : ''))
const installedVersion = computed(() => formatDependencyVersion(props.item?.installed_version))

// Each opening starts blank: a version typed for one row must not leak into
// the next confirmation.
const version = ref('')
watch(() => props.open, (open) => {
  if (open) version.value = ''
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

const rows = computed<DependencyKvRow[]>(() => [
  { label: t('bots.dependencies.confirm.dependency'), value: props.item?.id, mono: true },
  { label: t('bots.dependencies.confirm.installPath'), value: props.item?.install_path, mono: true },
])

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

function submit() {
  emit('confirm', version.value.trim())
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
              :disabled="loading"
            />
          </FieldStack>
        </form>

        <DependencyKvList :rows="rows" />

        <CalloutBanner
          v-if="targetKind === 'remote'"
          tone="warning"
          :title="t('bots.dependencies.confirm.remoteWarningTitle', { name: targetName })"
          :description="t('bots.dependencies.confirm.remoteWarningDescription')"
        />
      </DialogBody>

      <DialogFooter class="min-w-0 items-center gap-2 sm:justify-between">
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
            form="dependency-confirm-form"
            type="submit"
            :loading="loading"
          >
            {{ confirmText }}
          </Button>
        </div>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
