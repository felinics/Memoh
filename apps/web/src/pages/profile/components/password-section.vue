<template>
  <Dialog
    :open="open"
    @update:open="$emit('update:open', $event)"
  >
    <DialogTrigger as-child>
      <Button
        variant="outline"
        size="sm"
      >
        {{ $t('settings.changePassword') }}
      </Button>
    </DialogTrigger>
    <DialogContent :show-close-button="false">
      <DialogHeader>
        <DialogTitle>{{ $t('settings.changePassword') }}</DialogTitle>
      </DialogHeader>

      <FormStack>
        <FieldStack
          :label="$t('settings.currentPassword')"
          for="pw-current"
        >
          <PasswordInput
            id="pw-current"
            v-model="currentPassword"
            autocomplete="current-password"
          />
        </FieldStack>
        <FieldStack
          :label="$t('settings.newPassword')"
          for="pw-new"
        >
          <PasswordInput
            id="pw-new"
            v-model="newPassword"
            autocomplete="new-password"
            :aria-invalid="isMismatch || undefined"
          />
        </FieldStack>
        <FieldStack
          :label="$t('settings.confirmPassword')"
          for="pw-confirm"
        >
          <PasswordInput
            id="pw-confirm"
            v-model="confirmPassword"
            autocomplete="new-password"
            :aria-invalid="isMismatch || undefined"
          />
          <!-- Validation error rides in the control column, shown only on submit-time
               mismatch — inline field feedback, not FieldStack's static muted help. -->
          <p
            v-if="isMismatch"
            class="text-xs text-destructive"
          >
            {{ $t('settings.passwordNotMatch') }}
          </p>
        </FieldStack>
      </FormStack>

      <DialogFooter>
        <DialogClose as-child>
          <Button variant="outline">
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          :disabled="!hasInput || isMismatch"
          :loading="saving"
          @click="onSubmit"
        >
          {{ $t('settings.updatePassword') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@felinic/ui'
import { FieldStack, FormStack } from '@felinic/ui'
import PasswordInput from '@/components/password-input/index.vue'

const props = defineProps<{
  open: boolean
  saving: boolean
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  submit: [payload: { currentPassword: string, newPassword: string }]
}>()

const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')

const hasInput = computed(() =>
  currentPassword.value.length > 0
  && newPassword.value.length > 0
  && confirmPassword.value.length > 0,
)

const isMismatch = computed(() =>
  newPassword.value.length > 0
  && confirmPassword.value.length > 0
  && newPassword.value !== confirmPassword.value,
)

watch(() => props.open, (open) => {
  if (!open) {
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  }
})

function onSubmit() {
  if (!hasInput.value || isMismatch.value) return
  emit('submit', {
    currentPassword: currentPassword.value,
    newPassword: newPassword.value,
  })
}
</script>
