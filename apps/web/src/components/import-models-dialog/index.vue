<template>
  <FormDialogShell
    v-model:open="open"
    :title="$t(isRefresh ? 'models.refreshModels' : 'models.importModels')"
    :cancel-text="$t('common.cancel')"
    :submit-text="$t(isRefresh ? 'models.refreshModels' : 'common.import')"
    :submit-disabled="false"
    :loading="isLoading"
    @submit="handleImport"
  >
    <template #trigger>
      <Button
        variant="outline"
        :size="size"
        class="flex items-center gap-2"
      >
        <RefreshCw v-if="isRefresh" />
        <FileInput v-else />
        {{ $t(isRefresh ? 'models.refreshModels' : 'models.importModels') }}
      </Button>
    </template>
    <template #body>
      <div class="flex flex-col gap-3 mt-4">
        <!-- A provider from a models.dev source imports its catalog by default;
             the endpoint stays available for models the catalog lacks. -->
        <RadioGroup
          v-if="source"
          v-model="origin"
          class="gap-2"
        >
          <Label class="flex items-center gap-2 text-label">
            <RadioGroupItem value="catalog" />
            {{ $t('models.importFromCatalog') }}
          </Label>
          <Label class="flex items-center gap-2 text-label">
            <RadioGroupItem value="endpoint" />
            {{ $t('models.importFromEndpoint') }}
          </Label>
        </RadioGroup>
        <p class="text-xs text-muted-foreground">
          {{ $t(origin === 'catalog' && source
            ? 'models.importFromCatalogHint'
            : isRefresh ? 'models.refreshConfirmHint' : 'models.importConfirmHint') }}
        </p>
      </div>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation } from '@pinia/colada'
import { FormDialogShell, Label, RadioGroup, RadioGroupItem, toast } from '@felinic/ui'
import { FileInput, RefreshCw } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import type { ButtonVariants } from '@felinic/ui'
import { useDialogMutation } from '@/composables/useDialogMutation'
import { useProviderModelCatalog } from '@/composables/useProviderModelCatalog'

const props = withDefaults(defineProps<{
  providerId: string
  size?: ButtonVariants['size']
  mode?: 'import' | 'refresh'
  clientType?: string
  // models.dev provider the catalog import reads.
  source?: string
}>(), {
  size: 'default',
  mode: 'import',
})

const open = ref(false)
const { t } = useI18n()
const { run } = useDialogMutation()
const { importProviderModels } = useProviderModelCatalog()
const origin = ref<'catalog' | 'endpoint'>('catalog')
const isRefresh = computed(() => props.mode === 'refresh')

const { mutateAsync: importModelsMutation, isLoading } = useMutation({
  mutation: () => importProviderModels(
    {
      id: props.providerId,
      client_type: props.clientType,
      metadata: props.source ? { source: props.source } : {},
    },
    { fromEndpoint: origin.value === 'endpoint' },
  ),
})

async function handleImport() {
  await run(
    () => importModelsMutation(),
    {
      fallbackMessage: t(isRefresh.value ? 'models.refreshFailed' : 'models.importFailed'),
      onSuccess: (data) => {
        if (data) {
          toast.success(t(
            isRefresh.value ? 'models.refreshSuccess' : 'models.importSuccess',
            isRefresh.value
              ? { created: data.created, updated: data.updated }
              : { created: data.created, skipped: data.skipped },
          ))
        }
        open.value = false
      },
    },
  )
}
</script>
