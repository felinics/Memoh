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

      <Button
        variant="destructive"
        size="sm"
        :loading="deleteLoading"
        @click="dialogOpen = true"
      >
        {{ $t('bots.settings.deleteBot') }}
      </Button>
    </SettingsRow>
  </SettingsSection>

  <!-- Deleting a bot is irreversible, so a one-click popover is too easy to
       hit by accident: the user must type the bot's URL name (the
       `/bots/<name>` segment) before the delete button arms. -->
  <Dialog v-model:open="dialogOpen">
    <DialogPanel
      width="lg"
      footer
    >
      <form
        class="contents"
        @submit.prevent="submit"
      >
        <DialogHeader>
          <DialogTitle>{{ $t('bots.settings.deleteBot') }}</DialogTitle>
          <DialogDescription>
            {{ $t('bots.settings.deleteBotDescription') }}
          </DialogDescription>
        </DialogHeader>

        <FormStack>
          <FieldStack
            :label="$t('bots.settings.deleteBotConfirmLabel', { name: botName })"
            for="delete-bot-confirm"
          >
            <Input
              id="delete-bot-confirm"
              v-model="confirmText"
              autofocus
              autocomplete="off"
              spellcheck="false"
              :placeholder="botName"
            />
          </FieldStack>
        </FormStack>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            :disabled="deleteLoading"
            @click="dialogOpen = false"
          >
            {{ $t('common.cancel') }}
          </Button>
          <Button
            type="submit"
            variant="destructive"
            :disabled="!canDelete"
            :loading="deleteLoading"
          >
            {{ $t('common.delete') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  Button,
  Dialog,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  FieldStack,
  FormStack,
  Input,
  SettingsRow,
  SettingsSection,
} from '@felinic/ui'

const props = defineProps<{
  deleteLoading: boolean
  /** The bot's URL name — what the user must type to confirm. */
  botName: string
}>()

const emit = defineEmits<{
  'delete': []
}>()

const dialogOpen = ref(false)
const confirmText = ref('')

// Exact match: the point is to make the user read which bot they are about
// to delete, so no trimming or case folding.
const canDelete = computed(() =>
  props.botName.length > 0 && confirmText.value === props.botName,
)

watch(dialogOpen, (open) => {
  if (!open) confirmText.value = ''
})

function submit() {
  if (!canDelete.value || props.deleteLoading) return
  emit('delete')
}
</script>
