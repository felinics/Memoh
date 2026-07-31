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
        <p class="text-xs text-muted-foreground">
          {{ $t(isRefresh ? 'models.refreshConfirmHint' : 'models.importConfirmHint') }}
        </p>
      </div>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation } from '@pinia/colada'
import { FormDialogShell, toast } from '@felinic/ui'
import { FileInput, RefreshCw } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import type { ButtonVariants } from '@felinic/ui'
import { useDialogMutation } from '@/composables/useDialogMutation'
import { useProviderModelCatalog } from '@/composables/useProviderModelCatalog'

const props = withDefaults(defineProps<{
  providerId: string
  size?: ButtonVariants['size']
  mode?: 'import' | 'refresh'
}>(), {
  size: 'default',
  mode: 'import',
})

const open = ref(false)
const { t } = useI18n()
const { run } = useDialogMutation()
const { syncProviderModelCatalog } = useProviderModelCatalog()
const isRefresh = computed(() => props.mode === 'refresh')

const { mutateAsync: importModelsMutation, isLoading } = useMutation({
  mutation: () => syncProviderModelCatalog(props.providerId),
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
