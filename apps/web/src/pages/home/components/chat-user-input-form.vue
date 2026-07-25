<template>
  <ComposerCapsule>
    <div
      class="max-h-[40vh] overflow-y-auto overscroll-contain"
      style="scrollbar-gutter: stable;"
    >
      <div
        v-for="(question, questionIndex) in questions"
        :key="question.id"
        :class="questionIndex > 0 ? 'mt-3 border-t border-border-soft pt-3' : ''"
      >
        <p class="whitespace-pre-wrap break-words px-3 py-1.5 text-label font-medium text-foreground">
          {{ question.text }}
        </p>
        <div
          v-if="question.kind !== 'text' && question.options?.length"
          class="mt-0.5 flex flex-col gap-1"
        >
          <Button
            v-for="option in question.options"
            :key="option.id"
            type="button"
            variant="ghost"
            class="h-auto min-h-9 w-full justify-start whitespace-normal rounded-lg px-3 py-2 text-left text-control font-normal"
            :title="option.description || option.label"
            :role="question.kind === 'multi_select' ? 'checkbox' : 'radio'"
            :aria-checked="isOptionSelected(question.id, option.id)"
            @click="toggleOption(question, option.id)"
          >
            <component
              :is="optionIcon(question, isOptionSelected(question.id, option.id))"
              class="size-4 shrink-0"
              :class="isOptionSelected(question.id, option.id) ? 'text-foreground' : 'text-muted-foreground'"
            />
            <span class="min-w-0 flex-1 break-words">{{ option.label }}</span>
          </Button>
          <Button
            v-if="question.allow_custom && !isSingle"
            type="button"
            variant="ghost"
            class="h-auto min-h-9 w-full justify-start whitespace-normal rounded-lg px-3 py-2 text-left text-control font-normal"
            :role="question.kind === 'multi_select' ? 'checkbox' : 'radio'"
            :aria-checked="isCustomSelected(question.id)"
            @click="toggleCustom(question)"
          >
            <component
              :is="optionIcon(question, isCustomSelected(question.id))"
              class="size-4 shrink-0"
              :class="isCustomSelected(question.id) ? 'text-foreground' : 'text-muted-foreground'"
            />
            <span class="min-w-0 flex-1 break-words">{{ $t('chat.tools.userInputCustomOption') }}</span>
          </Button>
        </div>
        <Input
          v-if="!isSingle && (question.kind === 'text' || isCustomSelected(question.id))"
          class="mt-1"
          :model-value="draftText(question)"
          :placeholder="question.placeholder || $t('chat.tools.userInputPlaceholder')"
          @update:model-value="setDraftText(question, String($event))"
          @keydown.enter.prevent="handleSubmit"
        />
      </div>
    </div>
    <div class="mt-2 border-t border-border-soft pt-2">
      <Input
        v-if="footerInputVisible"
        class="mb-2"
        :model-value="footerText"
        :placeholder="footerPlaceholder"
        @update:model-value="setFooterText(String($event))"
        @keydown.enter.prevent="handleSubmit"
      />
      <div class="flex gap-1.5">
        <Button
          type="button"
          variant="default"
          class="h-9 flex-1 rounded-lg"
          :disabled="!canSubmit"
          @click="handleSubmit"
        >
          {{ $t('chat.tools.submitUserInput') }}
        </Button>
        <Button
          type="button"
          variant="secondary"
          class="h-9 flex-1 rounded-lg"
          @click="handleCancel"
        >
          {{ $t('chat.tools.cancelUserInput') }}
        </Button>
      </div>
    </div>
  </ComposerCapsule>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Button, Input } from '@felinic/ui'
import { Circle, CircleDot, Square, SquareCheck } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useChatStore } from '@/store/chat-list'
import type { UIUserInput, UIUserInputQuestion, WSUserInputAnswer } from '@/composables/api/useChat'
import { useChatViewTarget } from '../composables/useChatViewContext'
import ComposerCapsule from './composer-capsule.vue'

// Inline Q&A selector for the ask_user tool. It REPLACES the chat composer
// while a request is pending (the parent hides the textarea and shows this
// capsule, styled like the composer, in its place) and is fully
// self-contained: option rows on top, then a divider, then the free-text
// input ("tell the bot more") and the Submit / Cancel pair. The normal
// composer stays hidden until the request is submitted or canceled.
// Multi-question requests keep per-question inline inputs, because one footer
// input cannot answer several text questions at once.
const props = defineProps<{
  userInput: UIUserInput
}>()

const emit = defineEmits<{
  (e: 'reveal-composer', opts: { focus?: boolean }): void
}>()

interface PendingUserInputDraft {
  optionIds: string[]
  customSelected: boolean
  customText: string
  text: string
}

const { t } = useI18n()
const chatStore = useChatStore()
const chatViewTarget = useChatViewTarget()
const drafts = ref<Record<string, PendingUserInputDraft>>({})

const questions = computed(() => props.userInput.questions ?? [])
const isSingle = computed(() => questions.value.length === 1)
const singleQuestion = computed(() => (isSingle.value ? questions.value[0] ?? null : null))

// The footer free-text entry: the answer box for a lone text question, or the
// custom ("Other") entry when the lone select question allows one.
const footerInputVisible = computed(() => {
  const question = singleQuestion.value
  if (!question) return false
  return question.kind === 'text' || question.allow_custom === true
})
const footerText = computed(() => {
  const question = singleQuestion.value
  if (!question) return ''
  return draftText(question)
})
const footerPlaceholder = computed(() => {
  const question = singleQuestion.value
  if (!question) return ''
  if (question.placeholder) return question.placeholder
  return question.kind === 'text'
    ? t('chat.tools.userInputPlaceholder')
    : t('chat.tools.userInputTellMore')
})

// A new request invalidates any half-filled answers from the previous one.
watch(
  () => props.userInput.user_input_id,
  () => {
    drafts.value = {}
  },
)

function ensureDraft(questionId: string): PendingUserInputDraft {
  let draft = drafts.value[questionId]
  if (!draft) {
    draft = { optionIds: [], customSelected: false, customText: '', text: '' }
    drafts.value[questionId] = draft
  }
  return draft
}

function isOptionSelected(questionId: string, optionId: string) {
  return drafts.value[questionId]?.optionIds.includes(optionId) ?? false
}

function isCustomSelected(questionId: string) {
  return drafts.value[questionId]?.customSelected ?? false
}

function optionIcon(question: UIUserInputQuestion, selected: boolean) {
  if (question.kind === 'multi_select') return selected ? SquareCheck : Square
  return selected ? CircleDot : Circle
}

function toggleOption(question: UIUserInputQuestion, optionId: string) {
  const draft = ensureDraft(question.id)
  if (question.kind === 'multi_select') {
    draft.optionIds = draft.optionIds.includes(optionId)
      ? draft.optionIds.filter(id => id !== optionId)
      : [...draft.optionIds, optionId]
  } else {
    draft.optionIds = [optionId]
    // An option and the custom entry are mutually exclusive for single_select.
    draft.customSelected = false
    draft.customText = ''
  }
}

function toggleCustom(question: UIUserInputQuestion) {
  const draft = ensureDraft(question.id)
  if (question.kind === 'multi_select') {
    draft.customSelected = !draft.customSelected
  } else {
    draft.customSelected = true
    draft.optionIds = []
  }
  if (!draft.customSelected) {
    draft.customText = ''
  }
}

function draftText(question: UIUserInputQuestion) {
  const draft = drafts.value[question.id]
  if (!draft) return ''
  return question.kind === 'text' ? draft.text : draft.customText
}

function setDraftText(question: UIUserInputQuestion, value: string) {
  const draft = ensureDraft(question.id)
  if (question.kind === 'text') {
    draft.text = value
    return
  }
  draft.customText = value
}

function setFooterText(value: string) {
  const question = singleQuestion.value
  if (!question) return
  const draft = ensureDraft(question.id)
  setDraftText(question, value)
  // Typing a custom answer displaces the picked option for single_select —
  // the backend accepts exactly one of the two.
  if (question.kind === 'single_select' && value.trim()) {
    draft.optionIds = []
  }
}

function answerFor(question: UIUserInputQuestion): WSUserInputAnswer | null {
  const draft = drafts.value[question.id]
  if (question.kind === 'text') {
    const text = draft?.text.trim() ?? ''
    return text ? { question_id: question.id, text } : null
  }
  const optionIds = draft?.optionIds ?? []
  let customText = ''
  if (isSingle.value) {
    // The footer input needs no explicit "Other" selection — text present
    // means the custom answer is in play.
    customText = question.allow_custom === true ? (draft?.customText.trim() ?? '') : ''
  } else {
    const customSelected = draft?.customSelected ?? false
    customText = customSelected ? (draft?.customText.trim() ?? '') : ''
    if (customSelected && !customText) return null
  }
  if (question.kind === 'single_select' && optionIds.length + (customText ? 1 : 0) !== 1) return null
  if (optionIds.length === 0 && !customText) return null
  const answer: WSUserInputAnswer = { question_id: question.id }
  if (optionIds.length > 0) answer.option_ids = [...optionIds]
  if (customText) answer.custom_text = customText
  return answer
}

// All questions must be answered per kind before submit; null means incomplete.
const answers = computed<WSUserInputAnswer[] | null>(() => {
  if (!questions.value.length) return null
  const out: WSUserInputAnswer[] = []
  for (const question of questions.value) {
    const answer = answerFor(question)
    if (!answer) return null
    out.push(answer)
  }
  return out
})

const canSubmit = computed(() => answers.value !== null)

function handleSubmit() {
  if (!answers.value) return
  void chatStore.respondUserInput(props.userInput, { answers: answers.value }, chatViewTarget.value)
}

function handleCancel() {
  // Hand the composer back (focused) before the canceled state unmounts us.
  emit('reveal-composer', { focus: true })
  void chatStore.respondUserInput(props.userInput, {
    canceled: true,
    reason: 'user_canceled',
  }, chatViewTarget.value)
}
</script>
