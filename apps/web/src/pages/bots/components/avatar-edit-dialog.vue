<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('bots.editAvatar') }}</DialogTitle>
        <DialogDescription>
          {{ $t('bots.editAvatarDescription') }}
        </DialogDescription>
      </DialogHeader>
      <div class="mt-4 flex flex-col items-center gap-4">
        <Avatar class="size-20 shrink-0 rounded-full">
          <AvatarImage
            v-if="draft.trim()"
            :src="draft.trim()"
            :alt="fallbackText"
          />
          <AvatarFallback class="text-xl">
            {{ fallbackText }}
          </AvatarFallback>
        </Avatar>
        <Input
          v-model="draft"
          type="url"
          class="w-full"
          :placeholder="$t('bots.avatarUrlPlaceholder')"
        />
      </div>
      <DialogFooter class="mt-6">
        <DialogClose as-child>
          <Button variant="outline">
            {{ $t('common.cancel') }}
          </Button>
        </DialogClose>
        <Button
          :disabled="!canConfirm"
          :loading="validating"
          @click="handleConfirm"
        >
          {{ $t('common.confirm') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import {
  Avatar,
  AvatarImage,
  AvatarFallback,
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  toast,
} from '@felinic/ui'
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'

withDefaults(defineProps<{
  fallbackText?: string
}>(), {
  fallbackText: '',
})

const { t } = useI18n()

const open = defineModel<boolean>('open', { default: false })
const avatarUrl = defineModel<string>('avatarUrl', { default: '' })

const draft = ref('')
const validating = ref(false)

function isValidUrl(str: string): boolean {
  try {
    const url = new URL(str)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

function isImageUrl(url: string): Promise<boolean> {
  return new Promise((resolve) => {
    const img = new Image()
    img.onload = () => resolve(true)
    img.onerror = () => resolve(false)
    img.src = url
  })
}

const canConfirm = computed(() => {
  const next = draft.value.trim()
  const current = (avatarUrl.value || '').trim()
  return next !== current && !validating.value
})

watch(open, (val) => {
  if (val) {
    draft.value = avatarUrl.value || ''
    validating.value = false
  }
})

async function handleConfirm() {
  if (!canConfirm.value) return
  const url = draft.value.trim()

  if (!isValidUrl(url)) {
    toast.error(t('bots.avatarUrlInvalid'))
    return
  }

  validating.value = true
  const valid = await isImageUrl(url)
  validating.value = false

  if (!valid) {
    toast.error(t('bots.avatarUrlNotImage'))
    return
  }

  avatarUrl.value = url
  open.value = false
}
</script>
