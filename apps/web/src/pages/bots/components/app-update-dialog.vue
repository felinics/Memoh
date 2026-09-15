<script setup lang="ts">
// Picks what to update on one App before anything runs: the release
// (which replaces its Skills) and each dependency with a newer version.
// Everything starts selected; the choice is emitted, the panel streams it.
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  Checkbox,
  Dialog,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from '@felinic/ui'
import {
  appDependencyUpdates,
  appDisplayName,
  appUpdateAvailable,
  type AppItem,
  type AppDependencyConfirmation,
} from '@/composables/api/useApps'
import { dependencyDisplayName, formatDependencyVersion } from '@/utils/workspace-dependency'
import { useAppPreparation } from '../composables/useAppPreparation'
import AppDependencyConfirmations from './app-dependency-confirmations.vue'
import DependencyKvList from './dependency-kv-list.vue'

export interface AppUpdateChoice {
  action: 'update' | 'resume'
  release: boolean
  dependencies: string[]
  releaseRevision: string
  dependencyConfirmations: AppDependencyConfirmation[]
}

interface Candidate {
  key: string
  label: string
  from: string
  to: string
}

const props = withDefaults(defineProps<{
  open: boolean
  botId: string
  item: AppItem | null
  action?: 'update' | 'resume'
}>(), { action: 'update' })

const emit = defineEmits<{
  'update:open': [value: boolean]
  confirm: [choice: AppUpdateChoice]
}>()

const { t, locale } = useI18n()
const RELEASE_KEY = 'release'
const selected = ref(new Set<string>())
const submitting = ref(false)

const name = computed(() => (props.item ? appDisplayName(props.item, locale.value) : ''))

const candidates = computed<Candidate[]>(() => {
  const item = props.item
  if (!item) return []
  const list: Candidate[] = []
  if (appUpdateAvailable(item)) {
    list.push({
      key: RELEASE_KEY,
      label: t('apps.update.release'),
      from: item.version || '',
      to: item.available_version || item.available_revision?.slice(0, 8) || '',
    })
  }
  for (const dep of appDependencyUpdates(item)) {
    if (!dep.id || !dep.dependency) continue
    list.push({
      key: `dep:${dep.id}`,
      label: dependencyDisplayName(dep.dependency, locale.value),
      from: formatDependencyVersion(dep.dependency.installed_version),
      to: formatDependencyVersion(dep.dependency.latest_version),
    })
  }
  return list
})

watch([() => props.open, () => props.botId, () => props.item, () => props.action], ([open]) => {
  if (open) selected.value = new Set(candidates.value.map(candidate => candidate.key))
  submitting.value = false
}, { immediate: true })

function toggle(key: string, value: boolean | 'indeterminate') {
  const next = new Set(selected.value)
  if (value === true) next.add(key)
  else next.delete(key)
  selected.value = next
}

const selectedCount = computed(() => candidates.value.filter(candidate => selected.value.has(candidate.key)).length)

const { prepared, preparing, error: prepareError, prepare } = useAppPreparation(() => {
  const item = props.item
  if (!props.open || !props.botId || !item) return null
  if (props.action === 'resume') {
    return item.installation_id
      ? { botId: props.botId, request: { action: 'resume', installation_id: item.installation_id } }
      : null
  }
  if (!item.registry_id || !item.app_id || !selectedCount.value) return null
  return {
    botId: props.botId,
    request: {
      action: 'update',
      registry_id: item.registry_id,
      app_id: item.app_id,
      release: selected.value.has(RELEASE_KEY),
      dependencies: candidates.value
        .filter(candidate => candidate.key !== RELEASE_KEY && selected.value.has(candidate.key))
        .map(candidate => candidate.key.slice('dep:'.length)),
    },
  }
})

function confirm() {
  const confirmation = prepared.value
  if (submitting.value || !confirmation?.result.revision || confirmation.request.action === 'install') return
  submitting.value = true
  emit('confirm', {
    action: confirmation.request.action,
    release: confirmation.request.action === 'update' && confirmation.request.release,
    dependencies: confirmation.request.action === 'update' ? confirmation.request.dependencies : [],
    releaseRevision: confirmation.result.revision,
    dependencyConfirmations: confirmation.result.dependencies ?? [],
  })
  void nextTick(() => { submitting.value = false })
}
</script>

<template>
  <Dialog
    :open="open"
    @update:open="(value) => emit('update:open', value)"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader class="min-w-0">
        <DialogTitle class="break-words">
          {{ action === 'resume' ? t('apps.prepare.resumeTitle', { name }) : t('apps.update.title', { name }) }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ action === 'resume' ? t('apps.prepare.resumeDescription') : t('apps.update.description') }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0 space-y-4">
        <p
          v-if="action === 'update' && !candidates.length"
          class="text-body text-muted-foreground"
        >
          {{ t('apps.update.none') }}
        </p>
        <ul
          v-else-if="action === 'update'"
          class="divide-y divide-border rounded-lg border border-border"
        >
          <li
            v-for="candidate in candidates"
            :key="candidate.key"
          >
            <label class="flex cursor-pointer items-center gap-3 px-3 py-2.5">
              <Checkbox
                :model-value="selected.has(candidate.key)"
                @update:model-value="toggle(candidate.key, $event)"
              />
              <span class="min-w-0 flex-1 truncate text-body font-medium">{{ candidate.label }}</span>
              <span class="shrink-0 font-mono text-caption text-muted-foreground">
                <template v-if="candidate.from">{{ candidate.from }} → </template>{{ candidate.to }}
              </span>
            </label>
          </li>
        </ul>
        <template v-if="prepared">
          <DependencyKvList
            :rows="[{ label: t('apps.prepare.appRevision'), value: prepared.result.revision, mono: true }]"
          />
          <AppDependencyConfirmations :dependencies="prepared.result.dependencies ?? []" />
        </template>
        <p
          v-if="prepareError"
          role="alert"
          class="text-body text-destructive"
        >
          {{ prepareError }}
        </p>
      </DialogBody>

      <DialogFooter>
        <Button
          variant="outline"
          @click="emit('update:open', false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          v-if="!prepared"
          :disabled="action === 'update' && !selectedCount"
          :loading="preparing"
          @click="prepare"
        >
          {{ t('apps.prepare.review') }}
        </Button>
        <Button
          v-else
          :disabled="submitting || !prepared.result.revision"
          @click="confirm"
        >
          {{ action === 'resume' ? t('apps.action.resume') : t('apps.update.confirm', { count: selectedCount }, selectedCount) }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
