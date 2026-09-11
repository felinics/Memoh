<script setup lang="ts">
// Picks what to update on one App before anything runs: the release
// (which replaces its Skills) and each dependency with a newer version.
// Everything starts selected; the choice is emitted, the panel streams it.
import { computed, ref, watch } from 'vue'
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
} from '@/composables/api/useApps'
import { dependencyDisplayName, formatDependencyVersion } from '@/utils/workspace-dependency'

export interface AppUpdateChoice {
  release: boolean
  dependencies: string[]
}

interface Candidate {
  key: string
  label: string
  from: string
  to: string
}

const props = defineProps<{
  open: boolean
  item: AppItem | null
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  confirm: [choice: AppUpdateChoice]
}>()

const { t, locale } = useI18n()
const RELEASE_KEY = 'release'
const selected = ref(new Set<string>())

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

watch(() => props.open, (open) => {
  if (open) selected.value = new Set(candidates.value.map(candidate => candidate.key))
})

function toggle(key: string, value: boolean | 'indeterminate') {
  const next = new Set(selected.value)
  if (value === true) next.add(key)
  else next.delete(key)
  selected.value = next
}

const selectedCount = computed(() => candidates.value.filter(candidate => selected.value.has(candidate.key)).length)

function confirm() {
  emit('confirm', {
    release: selected.value.has(RELEASE_KEY),
    dependencies: candidates.value
      .filter(candidate => candidate.key !== RELEASE_KEY && selected.value.has(candidate.key))
      .map(candidate => candidate.key.slice('dep:'.length)),
  })
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
          {{ t('apps.update.title', { name }) }}
        </DialogTitle>
        <DialogDescription class="break-words">
          {{ t('apps.update.description') }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody class="min-w-0">
        <p
          v-if="!candidates.length"
          class="text-body text-muted-foreground"
        >
          {{ t('apps.update.none') }}
        </p>
        <ul
          v-else
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
      </DialogBody>

      <DialogFooter>
        <Button
          variant="outline"
          @click="emit('update:open', false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          :disabled="!selectedCount"
          @click="confirm"
        >
          {{ t('apps.update.confirm', { count: selectedCount }, selectedCount) }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
