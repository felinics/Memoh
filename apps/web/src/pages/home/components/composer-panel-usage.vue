<template>
  <div
    class="flex items-center gap-2 px-1.5 py-1"
    :class="exhausted ? 'text-destructive' : 'text-muted-foreground'"
  >
    <CircleAlert
      v-if="exhausted"
      class="size-3.5 shrink-0"
    />
    <span class="min-w-0 flex-1 break-words text-body">{{ message }}</span>
    <Button
      v-if="!exhausted"
      type="button"
      variant="ghost"
      size="icon-sm"
      :aria-label="$t('chat.codexUsage.dismiss')"
      @click="emit('dismiss')"
    >
      <X class="size-3.5" />
    </Button>
  </div>
</template>

<script setup lang="ts">
// The account usage line inside the composer panel. A nearing-limit warning
// can be dismissed; a reached limit cannot, since every send fails until the
// window resets.
import { Button } from '@felinic/ui'
import { CircleAlert, X } from 'lucide-vue-next'

defineProps<{
  exhausted: boolean
  message: string
}>()
const emit = defineEmits<{ (e: 'dismiss'): void }>()
</script>
