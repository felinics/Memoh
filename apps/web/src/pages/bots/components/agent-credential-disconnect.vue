<script setup lang="ts">
import { Button, ConfirmPopover } from '@felinic/ui'

defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ confirm: [] }>()
</script>

<template>
  <ConfirmPopover
    :title="$t('bots.settings.agentCredentialDisconnectConfirm')"
    :cancel-text="$t('common.cancel')"
    :confirm-text="$t('bots.settings.agentCredentialDisconnect')"
    variant="destructive"
    @confirm="emit('confirm')"
  >
    <template #trigger>
      <Button
        type="button"
        variant="outline"
        size="sm"
        :disabled="disabled"
      >
        {{ $t('bots.settings.agentCredentialDisconnect') }}
      </Button>
    </template>
  </ConfirmPopover>
</template>
