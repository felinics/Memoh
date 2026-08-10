<template>
  <Dialog
    :open="open"
    @update:open="onOpenChange"
  >
    <DialogPanel
      width="lg"
      footer
    >
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
      </DialogHeader>
      <DialogBody>
        <form
          class="space-y-1.5"
          @submit.prevent="submit"
        >
          <Label :for="inputId">{{ label }}</Label>
          <Input
            :id="inputId"
            v-model="value"
            :placeholder="placeholder"
            autocomplete="off"
          />
        </form>
      </DialogBody>
      <DialogFooter>
        <Button
          variant="ghost"
          @click="onOpenChange(false)"
        >
          {{ t('common.cancel') }}
        </Button>
        <Button
          :disabled="!canSubmit"
          @click="submit"
        >
          <Spinner v-if="busy" />
          {{ confirmLabel }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  Dialog,
  DialogBody,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  Input,
  Label,
  Spinner,
} from '@felinic/ui'

// One shared single-field dialog for every name prompt in the Projects panel
// (new project / rename project / new doc / rename doc): the four flows only
// differ in copy and initial value, and four bespoke dialogs would drift.
const props = defineProps<{
  open: boolean
  title: string
  label: string
  placeholder: string
  confirmLabel: string
  initialValue?: string
  busy?: boolean
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  'confirm': [value: string]
}>()

const { t } = useI18n()
const inputId = useId()
const value = ref('')

watch(() => props.open, (open) => {
  if (!open) return
  value.value = props.initialValue ?? ''
  // Focus lands on the field the user came to fill (SKILL §2b): autofocus
  // only fires on first document load, so focus programmatically on open.
  void nextTick(() => {
    document.getElementById(inputId)?.focus()
  })
})

const canSubmit = computed(() => value.value.trim().length > 0 && !props.busy)

function onOpenChange(open: boolean) {
  emit('update:open', open)
}

function submit() {
  if (!canSubmit.value) return
  emit('confirm', value.value.trim())
}
</script>
