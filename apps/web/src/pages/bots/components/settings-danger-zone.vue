<template>
  <SettingsSection>
    <SettingsRow>
      <!-- #content, not the label prop: the danger-zone label carries the
           destructive signal color, which the default label slot (text-foreground)
           would drop. -->
      <template #content>
        <div class="truncate text-sm font-medium text-destructive">
          {{ $t('bots.settings.dangerZone') }}
        </div>
        <p class="mt-0.5 text-xs text-muted-foreground">
          {{ $t('bots.settings.deleteBotDescription') }}
        </p>
      </template>

      <ConfirmPopover
        :message="$t('bots.deleteConfirm')"
        :loading="deleteLoading"
        :cancel-text="$t('common.cancel')"
        :confirm-text="$t('common.delete')"
        @confirm="$emit('delete')"
      >
        <template #trigger>
          <Button
            variant="destructive"
            size="sm"
            :loading="deleteLoading"
          >
            {{ $t('bots.settings.deleteBot') }}
          </Button>
        </template>
      </ConfirmPopover>
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import { Button, ConfirmPopover, SettingsRow, SettingsSection } from '@felinic/ui'

defineProps<{
  deleteLoading: boolean
}>()

defineEmits<{
  'delete': []
}>()
</script>
