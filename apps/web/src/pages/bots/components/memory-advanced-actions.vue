<template>
  <!-- Memory "Index & sync": one ActionCard row on the Memories tab opening a
       dialog. The dialog body is a single row in the owner rhythm — backend
       name + health on the left, the sync action on the right — same shape as
       every other action row in the app. Index counts stay on Overview; path
       details were dropped (copy discipline: the user already knows them). -->
  <section class="mb-6">
    <ActionCard
      :title="$t('bots.memory.advanced.entryTitle')"
      @click="dialogOpen = true"
    >
      <template #icon>
        <SlidersHorizontal />
      </template>
    </ActionCard>

    <Dialog v-model:open="dialogOpen">
      <DialogPanel>
        <DialogHeader>
          <DialogTitle>{{ $t('bots.memory.advanced.entryTitle') }}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div class="flex min-h-[2.25rem] items-center justify-between gap-4">
            <div class="min-w-0">
              <p class="text-sm font-medium text-foreground">
                {{ $t('memory.builtinTitle') }}
              </p>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ syncDescription }}
              </p>
            </div>
            <Button
              variant="outline"
              size="sm"
              class="shrink-0"
              :disabled="!memoryStatus?.can_manual_sync"
              :loading="syncLoading"
              @click="handleSync"
            >
              {{ $t('bots.memory.advanced.syncAction') }}
            </Button>
          </div>
        </DialogBody>
      </DialogPanel>
    </Dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { SlidersHorizontal } from 'lucide-vue-next'
import {
  ActionCard,
  Button,
  Dialog,
  DialogBody,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  toast,
} from '@felinic/ui'
import { postBotsByBotIdMemoryRebuild } from '@memohai/sdk'
import type { AdaptersMemoryStatusResponse } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
  memoryStatus: AdaptersMemoryStatusResponse | null
  statusLoading?: boolean
}>()

const emit = defineEmits<{
  synced: []
}>()

const { t } = useI18n()
const dialogOpen = ref(false)
const syncLoading = ref(false)

const syncDescription = computed(() => {
  if (props.statusLoading) return t('common.loading')
  if (!props.memoryStatus) return t('bots.memory.advanced.healthUnavailable')
  if (props.memoryStatus.degraded) return t('bots.memory.degradedDesc')
  return t('bots.memory.advanced.healthOk')
})

async function handleSync() {
  const botId = props.botId.trim()
  if (!botId) return

  syncLoading.value = true
  try {
    const { data } = await postBotsByBotIdMemoryRebuild({
      path: { bot_id: botId },
      throwOnError: true,
    })
    toast.success(t('bots.memory.advanced.syncSuccess', {
      fsCount: data?.fs_count ?? 0,
      restoredCount: data?.restored_count ?? 0,
      storageCount: data?.storage_count ?? 0,
    }))
    emit('synced')
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.memory.advanced.syncFailed')))
  } finally {
    syncLoading.value = false
  }
}
</script>
