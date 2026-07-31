<template>
  <!-- A flush list row, not a nested card: name on the left, actions on the
       right, separated from the next model by an inset hairline (the last row
       drops it). Same row language as the Configuration card above. -->
  <div
    class="mx-4 flex min-h-[3.25rem] items-center justify-between gap-3 border-b border-border py-2.5 last:border-b-0"
    :class="{ 'opacity-60': !enabled }"
  >
    <!-- A hair of left padding so the name doesn't sit dead-flush on the row's
         inset edge when the search box is above it — just breathing room, not a
         full align to the placeholder text. -->
    <div
      class="min-w-0 flex-1"
      :class="{ 'pl-1': searchAligned }"
    >
      <div class="flex min-w-0 items-center gap-2">
        <ModelDescriptionTooltip :description="modelDescription">
          <span class="truncate text-[13px] font-medium text-foreground">
            {{ model.name || model.model_id }}
          </span>
        </ModelDescriptionTooltip>
        <Badge
          v-if="model.type === 'embedding'"
          variant="outline"
          size="sm"
          class="inline-flex shrink-0 items-center gap-1"
        >
          <Binary class="size-3" />
          {{ model.type }}
        </Badge>
        <span
          v-if="testResult"
          class="inline-flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
        >
          <span
            class="inline-block size-2 rounded-full"
            :class="statusDotClass"
          />
          <span v-if="testResult.latency_ms">{{ testResult.latency_ms }}ms</span>
        </span>
        <Spinner
          v-if="testLoading"
          class="size-3.5 shrink-0"
        />
      </div>
      <!-- Second line only when it carries new info: the real id (when a custom
           display name is hiding it) or a test error. When the name already is
           the id, repeating it is pure noise, so the line is dropped. -->
      <div
        v-if="showModelId || showError"
        class="mt-0.5 flex flex-wrap items-center gap-2"
      >
        <span
          v-if="showModelId"
          class="truncate text-xs text-muted-foreground"
        >
          {{ model.model_id }}
        </span>
        <span
          v-if="showError"
          class="text-xs text-destructive"
        >
          {{ testResult?.message }}
        </span>
      </div>
      <p
        v-if="modelDescription"
        data-model-description
        class="mt-1 line-clamp-2 whitespace-pre-wrap break-words text-body text-muted-foreground"
      >
        {{ modelDescription }}
      </p>
    </div>

    <div
      v-if="!preview"
      class="flex shrink-0 items-center gap-0.5"
    >
      <Switch
        class="mr-1"
        :model-value="enabled"
        :disabled="!model.id || enableLoading"
        :aria-label="$t('models.enable')"
        @update:model-value="handleToggleEnable"
      />

      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        :disabled="testLoading"
        :aria-label="$t('models.testModel')"
        @click="runTest"
      >
        <Zap class="size-4" />
      </Button>

      <Button
        v-if="!managed"
        type="button"
        variant="ghost"
        size="icon-sm"
        :aria-label="$t('common.edit')"
        @click="$emit('edit', model)"
      >
        <Settings class="size-4" />
      </Button>

      <ConfirmPopover
        v-if="!managed"
        :message="$t('models.deleteModelConfirm')"
        :cancel-text="$t('common.cancel')"
        :confirm-text="$t('common.confirm')"
        :loading="deleteLoading"
        @confirm="$emit('delete', model.id ?? '')"
      >
        <template #trigger>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            :aria-label="$t('common.delete')"
          >
            <Trash2 class="size-4" />
          </Button>
        </template>
      </ConfirmPopover>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  Badge,
  Button,
  Spinner,
  Switch,
  toast,
} from '@felinic/ui'
import { Zap, Settings, Trash2, Binary } from 'lucide-vue-next'
import { ConfirmPopover } from '@felinic/ui'
import ModelDescriptionTooltip from '@/components/model-description-tooltip/index.vue'
import { postModelsByIdTest, putModelsById } from '@memohai/sdk'
import type { ModelsGetResponse, ModelsTestResponse, ModelsUpdateRequest } from '@memohai/sdk'
import { useQueryCache } from '@pinia/colada'
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { getModelDescription } from '@/utils/model-description'

const props = withDefaults(defineProps<{
  model: ModelsGetResponse
  deleteLoading: boolean
  searchAligned?: boolean
  managed?: boolean
  preview?: boolean
}>(), {
  searchAligned: false,
  managed: false,
  preview: false,
})

defineEmits<{
  edit: [model: ModelsGetResponse]
  delete: [id: string]
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const testLoading = ref(false)
const testResult = ref<ModelsTestResponse | null>(null)
const enableLoading = ref(false)
const enableOverride = ref<boolean | null>(null)

const enabled = computed(() => enableOverride.value ?? props.model.enable ?? true)
const modelDescription = computed(() => getModelDescription(props.model.config))

// Show the id as a second line only when a real custom name is hiding it.
const showModelId = computed(() => {
  const name = props.model.name?.trim()
  return !!name && !!props.model.model_id && name !== props.model.model_id
})

const showError = computed(
  () => !!(testResult.value && testResult.value.status !== 'ok' && testResult.value.message),
)

const statusDotClass = computed(() => {
  switch (testResult.value?.status) {
    case 'ok': return 'bg-success'
    case 'auth_error': return 'bg-warning'
    case 'error': return 'bg-destructive'
    default: return 'bg-muted-foreground'
  }
})

async function runTest() {
  if (!props.model.id) return
  testLoading.value = true
  testResult.value = null
  try {
    const { data } = await postModelsByIdTest({
      path: { id: props.model.id },
      throwOnError: true,
    })
    testResult.value = data ?? null
  } catch {
    testResult.value = { status: 'error' }
  } finally {
    testLoading.value = false
  }
}

async function handleToggleEnable(value: boolean) {
  if (!props.model.id) return
  const prev = enabled.value
  enableOverride.value = value
  enableLoading.value = true
  try {
    const body: ModelsUpdateRequest = {
      model_id: props.model.model_id,
      name: props.model.name,
      provider_id: props.model.provider_id,
      type: props.model.type,
      config: props.model.config,
      enable: value,
    }
    await putModelsById({ path: { id: props.model.id }, body, throwOnError: true })
    queryCache.invalidateQueries({ key: ['provider-models'] })
    queryCache.invalidateQueries({ key: ['models'] })
    // bot-settings caches the full model list under ['all-models']; without
    // this, the bot picker keeps a disabled model visible until the route
    // remounts because the tab is kept alive.
    queryCache.invalidateQueries({ key: ['all-models'] })
  } catch {
    enableOverride.value = prev
    toast.error(t('models.toggleFailed'))
  } finally {
    enableLoading.value = false
  }
}
</script>
