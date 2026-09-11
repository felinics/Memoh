<script setup lang="ts">
import { Button } from '@felinic/ui'
import PasswordInput from '@/components/password-input/index.vue'
const value = defineModel<string>({ default: '' })
defineProps<{ loading: boolean, connected?: boolean, placeholder?: string }>()
const emit = defineEmits<{ save: [] }>()
</script>

<template>
  <div class="flex w-full flex-col gap-2 sm:flex-row">
    <PasswordInput
      v-model="value"
      autocomplete="new-password"
      class="min-w-0 flex-1"
      :placeholder="placeholder || $t('bots.settings.agentCredentialSecretPlaceholder')"
      @keydown.enter.prevent="emit('save')"
    />
    <Button
      type="button"
      size="sm"
      :loading="loading"
      :disabled="!value.trim()"
      @click="emit('save')"
    >
      {{ connected ? $t('bots.settings.agentCredentialReplace') : $t('bots.settings.agentCredentialSave') }}
    </Button>
    <slot />
  </div>
</template>
