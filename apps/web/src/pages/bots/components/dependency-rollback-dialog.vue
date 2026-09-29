<script setup lang="ts">
// Confirms switching a dependency back to the version the workspace kept.
// Rollback is a pure data switch — nothing downloads, nothing
// streams — so unlike the other operations it ends in a toast, not a log. The
// caller owns the request (same contract as ConfirmDeleteDialog) and closes on
// success.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from '@felinic/ui'
import type { DependencyItem } from '@/composables/api/useWorkspaceDependencies'
import { dependencyDisplayName, formatDependencyVersion } from '@/utils/workspace-dependency'
import DependencyPlanPreview from './dependency-plan-preview.vue'
import { useDependencyPlan } from '@/composables/useDependencyPlan'
import DependencyKvList, { type DependencyKvRow } from './dependency-kv-list.vue'

const props = withDefaults(defineProps<{
  botId: string
  open: boolean
  item: DependencyItem | null
  loading?: boolean
}>(), {
  loading: false,
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  confirm: [planId: string]
}>()

const { t } = useI18n()
const preview = useDependencyPlan(() => props.botId, () => props.open && props.item?.id ? { roots: [{ dependency_id: props.item.id, action: 'rollback' }] } : null)

const name = computed(() => (props.item ? dependencyDisplayName(props.item) : ''))
const from = computed(() => formatDependencyVersion(props.item?.installed_version))
const to = computed(() => formatDependencyVersion(props.item?.previous_version))

const rows = computed<DependencyKvRow[]>(() => [
  { label: t('bots.dependencies.rollback.current'), value: from.value, mono: true },
  { label: t('bots.dependencies.rollback.target'), value: to.value, mono: true },
])

function onOpenChange(value: boolean) {
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
        <DialogTitle>{{ t('bots.dependencies.rollback.title', { name }) }}</DialogTitle>
        <DialogDescription>
          {{ t('bots.dependencies.rollback.description', { from, to }) }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <DependencyKvList :rows="rows" />
        <DependencyPlanPreview
          :plan="preview.plan.value"
          :loading="preview.loading.value"
          :error="preview.error.value"
          @retry="preview.retry"
        />
      </DialogBody>

      <DialogFooter>
        <Button
          variant="outline"
          :disabled="loading"
          @click="emit('update:open', false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          :loading="loading"
          :disabled="!preview.ready.value"
          @click="emit('confirm', preview.plan.value?.id ?? '')"
        >
          {{ t('bots.dependencies.rollback.confirm', { to }) }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
