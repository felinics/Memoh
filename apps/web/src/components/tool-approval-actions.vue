<template>
  <div>
    <div
      v-if="rejecting"
      class="flex flex-col gap-2"
    >
      <Textarea
        v-model="reason"
        autofocus
        :disabled="responding"
        :placeholder="$t('chat.approval.rejectReasonPlaceholder')"
        :aria-label="$t('chat.approval.rejectReasonLabel')"
      />
      <div class="flex gap-1.5">
        <Button
          type="button"
          variant="ghost"
          class="flex-1"
          :disabled="responding"
          @click="emit('cancel-reject')"
        >
          {{ $t('common.cancel') }}
        </Button>
        <Button
          type="button"
          variant="destructive"
          class="flex-1"
          :disabled="responding"
          @click="emit('confirm-reject')"
        >
          {{ $t('chat.approval.confirmReject') }}
        </Button>
      </div>
    </div>
    <div
      v-else
      class="flex gap-1.5"
      :class="agentOptions.length ? 'flex-col' : ''"
    >
      <Button
        v-for="action in actions"
        :key="action.key"
        type="button"
        :class="agentOptions.length ? '' : 'flex-1'"
        :variant="action.approves ? 'default' : 'secondary'"
        :disabled="responding"
        @click="action.approves ? emit('approve', action.optionId) : emit('begin-reject', action.optionId)"
      >
        {{ action.name }}
        <template v-if="action.agentName">
          <span aria-hidden="true"> — </span>
          <span>{{ $t('chat.approval.agentOption') }}: <bdi>{{ action.agentName }}</bdi></span>
        </template>
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Button, Textarea } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import type { UIToolApprovalOption } from '@/composables/api/useChat'

const props = withDefaults(defineProps<{
  options?: UIToolApprovalOption[]
  responding: boolean
  rejecting: boolean
}>(), {
  options: () => [],
})

const reason = defineModel<string>('reason', { default: '' })
const { t } = useI18n()
const emit = defineEmits<{
  approve: [optionId?: string]
  'begin-reject': [optionId?: string]
  'cancel-reject': []
  'confirm-reject': []
}>()

type ApprovalAction = {
  key: string
  name: string
  agentName?: string
  optionId?: string
  approves: boolean
}

function optionKindLabel(kind: string | undefined) {
  switch (kind) {
    case 'allow_once': return t('chat.approval.option.allowOnce')
    case 'allow_always': return t('chat.approval.option.allowAlways')
    case 'reject_once': return t('chat.approval.option.rejectOnce')
    case 'reject_always': return t('chat.approval.option.rejectAlways')
    default: return t('chat.tools.reject')
  }
}

const agentOptions = computed(() => props.options
  .filter(option => option.id?.trim())
  .map(option => {
    const id = option.id!
    const kind = option.kind?.trim().toLowerCase()
    return {
      id,
      key: `option:${id}`,
      name: optionKindLabel(kind),
      agentName: option.name?.trim() || id,
      optionId: id,
      approves: kind === 'allow_once' || kind === 'allow_always',
    }
  }))
const actions = computed<ApprovalAction[]>(() => {
  if (!agentOptions.value.length) {
    return [
      { key: 'binary:approve', name: t('chat.tools.approve'), approves: true },
      { key: 'binary:reject', name: t('chat.tools.reject'), approves: false },
    ]
  }
  if (agentOptions.value.some(option => !option.approves)) return agentOptions.value
  return [
    ...agentOptions.value,
    { key: 'binary:reject', name: t('chat.tools.reject'), approves: false },
  ]
})
</script>
