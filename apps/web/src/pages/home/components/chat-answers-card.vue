<template>
  <!-- Completed ask_user, rendered as a quiet bordered card IN the text flow.
       The Q&A record is content the conversation refers back to — unlike the
       muted process capsule, which holds transient tool machinery — so it
       breaks out of the tool-call group (see renderNodes in message-item)
       and takes a bordered card, never the capsule. bg-card lifts the card
       off the page in dark mode (0.21 vs 0.152 surface); in light both are
       effectively white, so the card stays quiet there. Question muted,
       answer foreground: the Q→A reading without extra chrome.
       Width hugs the Q&A content (w-fit) instead of spanning the column —
       a full-width frame around two short lines reads as scaffolding. -->
  <div class="w-fit max-w-full rounded-lg border border-border bg-card px-4 py-3">
    <div class="space-y-2.5">
      <div
        v-for="entry in entries"
        :key="entry.id"
      >
        <p class="text-sm text-muted-foreground">
          {{ entry.question }}
        </p>
        <p
          class="mt-0.5 text-sm"
          :class="entry.unanswered ? 'text-muted-foreground' : 'text-foreground'"
        >
          {{ entry.answer }}
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { structuredToolResult } from '@/store/chat-list.normalize'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()

// Both the canonical user-input projection and the legacy tool result use this
// display-safe shape, so the card never touches the raw input JSON (#868).
type ResultAnswer = {
  question?: string
  selected?: { label?: string }[]
  custom_text?: string
  text?: string
  skipped?: boolean
}

const entries = computed(() => {
  const result = structuredToolResult(props.block.result) as { status?: string; answers?: ResultAnswer[] }
  const answers = props.block.userInput?.answers?.length
    ? props.block.userInput.answers
    : result?.answers
  if (Array.isArray(answers) && answers.length > 0) {
    return answers.map((a, i) => ({
      id: `a${i}`,
      question: a.question ?? '',
      answer: answerText(a),
      unanswered: a.skipped === true,
    }))
  }
  // Terminal requests without an answer payload are legacy or non-submitted
  // outcomes. Show their actual state instead of incorrectly calling them
  // unanswered while a canonical answer projection is still arriving.
  const questions = props.block.userInput?.questions?.map(q => q.text)
    ?? legacyQuestions(props.block.input)
  const status = props.block.userInput?.status ?? result?.status
  const note = terminalNote(status)
  return questions.map((q, i) => ({ id: `q${i}`, question: q, answer: note, unanswered: true }))
})

function terminalNote(status?: string): string {
  if (status === 'canceled') return t('chat.answers.cancelled')
  if (status === 'expired') return t('chat.tools.userInputExpired')
  if (status === 'failed') return t('chat.tools.userInputFailed')
  return t('chat.tools.userInputSubmitted')
}

function answerText(a: ResultAnswer): string {
  if (a.skipped) return t('chat.answers.skipped')
  const parts: string[] = []
  if (a.selected?.length) parts.push(a.selected.map(s => s.label ?? '').filter(Boolean).join(', '))
  if (a.custom_text) parts.push(a.custom_text)
  if (a.text) parts.push(a.text)
  return parts.filter(Boolean).join(', ')
}

function legacyQuestions(input: unknown): string[] {
  const obj = input as { questions?: { text?: string }[]; question?: string } | null
  if (Array.isArray(obj?.questions)) return obj.questions.map(q => q.text ?? '').filter(Boolean)
  if (typeof obj?.question === 'string') return [obj.question]
  return []
}
</script>
