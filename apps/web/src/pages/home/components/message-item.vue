<template>
  <div
    v-if="shouldRenderMessage"
    ref="messageItem"
    class="flex gap-3 items-start"
    :class="message.role === 'user' && isSelf && !channelThread ? 'justify-end' : ''"
  >
    <!-- Sender avatar. Local chat shows it only for remote users; a synced
         channel thread shows it for every participant (self / bot included). -->
    <div
      v-if="showAvatar"
      class="relative shrink-0"
    >
      <Avatar class="size-8">
        <AvatarImage
          v-if="avatarSrc"
          :src="avatarSrc"
          :alt="avatarName"
        />
        <AvatarFallback class="text-xs">
          {{ avatarFallback }}
        </AvatarFallback>
      </Avatar>
      <ChannelBadge
        v-if="avatarPlatform"
        :platform="avatarPlatform"
      />
    </div>

    <!-- Content -->
    <div
      class="min-w-0 group/msg"
      :class="contentClass"
      data-chat-content
    >
      <!-- Sender name (channel threads only): names every participant so the
           group conversation is legible without relying on bubble side. -->
      <p
        v-if="showSenderName && avatarName"
        class="text-xs font-medium text-muted-foreground mb-1 px-0.5"
      >
        {{ avatarName }}
      </p>

      <!-- Background task status -->
      <div
        v-if="message.role === 'system' && message.kind === 'background_task'"
        class="space-y-1"
      >
        <BackgroundTaskBlock :task="message.backgroundTask" />
        <p
          class="text-caption text-muted-foreground/80 mt-1"
          :title="fullTimestamp"
        >
          {{ relativeTimestamp }}
        </p>
      </div>

      <!-- Default user message (chat bubble) -->
      <div
        v-else-if="message.role === 'user'"
        class="flex flex-col gap-2"
        :class="bubbleSelf ? 'items-end' : 'items-start'"
      >
        <AttachmentBlock
          v-if="userAttachmentBlock"
          :block="userAttachmentBlock"
          :on-open-media="onOpenMedia"
        />
        <div
          v-if="isEditingUserMessage"
          class="w-[min(100%,42rem)] bg-muted px-4 py-3 text-foreground"
          :class="userBubbleRadiusClass"
        >
          <Textarea
            ref="editTextarea"
            v-model="editDraft"
            size="lg"
            class="max-h-52 min-h-20 resize-none rounded-none border-0 bg-transparent p-0 text-foreground shadow-none placeholder:text-muted-foreground focus-visible:ring-0"
            :aria-label="t('chat.actions.edit')"
            @keydown.enter.meta.prevent="submitEdit"
            @keydown.enter.ctrl.prevent="submitEdit"
            @keydown.escape.stop.prevent="cancelEdit"
          />
          <div class="mt-2 flex justify-end gap-1.5">
            <Button
              type="button"
              variant="outline"
              size="text"
              class="px-1"
              :disabled="editSubmitting"
              @click="cancelEdit"
            >
              {{ t('common.cancel') }}
            </Button>
            <Button
              type="button"
              variant="primary"
              size="text"
              class="px-1"
              :loading="editSubmitting"
              :disabled="!canSubmitEdit"
              @click="submitEdit"
            >
              {{ t('chat.send') }}
            </Button>
          </div>
        </div>
        <div
          v-else-if="isSkillActivationMessage || userBubbleText || message.forward || message.reply"
          :lang="contentLang(userBubbleText || skillActivationNames || skillActivationTitle)"
          class="chat-user-bubble w-fit max-w-full bg-chat-user-bubble px-4 py-3 text-chat-user-bubble-fg whitespace-pre-wrap break-words"
          :class="userBubbleRadiusClass"
        >
          <div
            v-if="isSkillActivationMessage"
            class="flex min-w-0 items-start gap-2"
          >
            <Sparkles
              aria-hidden="true"
              class="mt-0.5 size-3.5 shrink-0"
            />
            <div class="min-w-0 space-y-0.5">
              <p class="text-caption font-medium leading-snug">
                {{ skillActivationTitle }}
              </p>
              <p
                v-if="skillActivationNames"
                class="text-body leading-snug break-words"
              >
                {{ skillActivationNames }}
              </p>
            </div>
          </div>
          <div
            v-if="message.forward"
            class="text-[11px] font-medium leading-snug text-muted-foreground"
            :class="isSkillActivationMessage ? 'mt-2 mb-1' : 'mb-1'"
          >
            {{ t('chat.forwardedFrom', { sender: forwardSenderLabel }) }}
          </div>
          <button
            v-if="message.reply"
            type="button"
            class="relative mb-1 min-w-0 overflow-hidden rounded-sm py-1 pl-3 pr-2 leading-snug break-normal"
            :class="[
              'bg-background/55 dark:bg-background/20',
              canJumpReply ? 'block w-full text-left cursor-pointer hover:bg-background/70 dark:hover:bg-background/30 focus:outline-none focus:ring-1 focus:ring-primary/40' : 'block w-full text-left cursor-default',
            ]"
            :disabled="!canJumpReply"
            @click.stop="handleReplyClick"
          >
            <span
              class="absolute inset-y-0 left-0 w-[3px]"
              :class="bubbleSelf ? 'bg-border' : 'bg-primary/70'"
            />
            <div class="flex min-w-0 items-start gap-2">
              <div class="min-w-0 flex-1">
                <div
                  class="truncate text-[11px] font-semibold"
                  :class="bubbleSelf ? 'text-foreground' : 'text-primary'"
                >
                  {{ replySenderLabel }}
                </div>
                <div
                  v-if="replyPreviewLabel"
                  class="mt-0.5 line-clamp-2 text-[11px] whitespace-pre-wrap break-words text-muted-foreground"
                >
                  {{ replyPreviewLabel }}
                </div>
              </div>
              <img
                v-if="replyThumbnailSrc"
                :src="replyThumbnailSrc"
                :alt="replyPreviewLabel || replySenderLabel"
                class="size-9 shrink-0 rounded-sm object-cover"
                loading="lazy"
              >
            </div>
          </button>
          <CollapsibleUserText
            v-if="userBubbleText"
            :class="isSkillActivationMessage ? 'mt-2' : ''"
            :text="userBubbleText"
          />
        </div>
        <MessageActions
          v-if="!isEditingUserMessage"
          class="-mt-1"
          role="user"
          :copy-text="userCopyText"
          :align="bubbleSelf ? 'end' : 'start'"
          :on-edit="canEditUserMessage ? handleEdit : undefined"
        />
      </div>

      <!-- Assistant message blocks. The vertical gap between a process/"Thought
           for Ns" row and the body is ~15% tighter than the body↔rule gap
           (1.36rem vs the 1.6rem --ms-flow-hr-y): close to the unified rhythm,
           but the channel-process line was sitting a touch too far from the
           answer at full parity. -->
      <div v-else>
        <div class="space-y-[0.85rem]">
          <template
            v-for="node in renderNodes"
            :key="node.key"
          >
            <!-- Process segment: consecutive tool + reasoning blocks. A single
               item renders as a bare row; multiple collapse into one group. -->
            <ToolCallGroup
              v-if="node.kind === 'process'"
              :items="node.items"
              :active="message.streaming && node.lastIndex === message.messages.length - 1"
            />

            <!-- Completed ask_user: the Q&A card, broken out of the process
                 group so it reads as conversation content, not tool machinery. -->
            <ChatAnswersCard
              v-else-if="node.kind === 'answers'"
              :block="node.block"
            />

            <template v-else>
              <!-- Text block -->
              <!-- Headings split into two spacing groups, not one flat ramp:
                 h1–h3 (true section breaks) get more air above and a clear gap
                 below (so an h3 immediately followed by an h4 isn't cramped);
                 h4–h6 (sub-labels close to their text) get less. Reads as two
                 tiers rather than six evenly-spaced rungs. -->
              <div
                v-if="node.block.type === 'text' && node.block.content"
                :lang="contentLang(node.block.content)"
                class="prose prose-sm dark:prose-invert max-w-none [&_p]:my-0! [&_p+p]:mt-2! [&_ul]:my-1.5! [&_ol]:my-1.5! [&_li]:my-0.5! [&_:is(h1,h2,h3)]:mt-5! [&_:is(h1,h2,h3)]:mb-2! [&_:is(h4,h5,h6)]:mt-3! [&_:is(h4,h5,h6)]:mb-1! [&>*:first-child]:mt-0! [&>*:last-child]:mb-0!"
              >
                <MarkdownRender
                  :content="node.block.content"
                  :is-dark="isDark"
                  :smooth-streaming="isAssistantBlockStreaming(node.index)"
                  :typewriter="isAssistantBlockStreaming(node.index)"
                  :fade="isAssistantBlockStreaming(node.index)"
                  :show-tooltips="false"
                  :mermaid-props="{ showTooltips: false }"
                  :theme="codeBlockTheme"
                  custom-id="chat-msg"
                />
              </div>

              <!-- Error block -->
              <div
                v-else-if="node.block.type === 'error' && node.block.content"
                class="flex items-start gap-2 rounded-md border border-destructive/25 bg-destructive/10 px-3 py-2 text-xs text-destructive"
              >
                <CircleAlert class="mt-0.5 size-3.5 shrink-0" />
                <span class="min-w-0 whitespace-pre-wrap break-words">{{ node.block.content }}</span>
              </div>

              <!-- Attachment block. An assistant turn posts images as reply
                   content, so they render inline at natural aspect ratio rather
                   than as square upload chips. -->
              <AttachmentBlock
                v-else-if="node.block.type === 'attachments'"
                :block="(node.block as AttachmentBlockType)"
                :on-open-media="onOpenMedia"
                variant="content"
              />
            </template>
          </template>

          <!-- Local "the turn is running" indicator: shown only before the first
               block streams in. Same scale/weight as the process headers, and the
               same shimmer the Thinking/running states use (running = shimmer,
               done = solid), so it reads as the first link of the chain the
               Thinking block continues — not a separate loading widget. The phrase
               also types in (a stepped clip-path wipe) on entry; keyed by the hint
               so it replays once per turn. -->
          <div
            v-if="message.streaming && !hasVisibleAssistantBlocks"
            class="font-[400] text-[0.90625rem]"
          >
            <div class="flex items-center gap-1.5 py-px text-cop-title select-none">
              <span
                :key="thinkingHint"
                class="inline-block whitespace-nowrap tracking-[0.01em] tool-shimmer-text cop-typewriter"
              >{{ thinkingHint }}…</span>
            </div>
          </div>
        </div>
        <!-- Action bar hugs the answer (~9px), tighter than the inter-block
             rhythm above, so it reads as belonging to this turn.
             Visibility: only the LATEST turn keeps its bar always on (it is
             the one being acted on); history turns reveal on hover within the
             turn's hover scope, same as the user bubble's bar. Streaming
             hides it entirely (handled inside MessageActions). -->
        <MessageActions
          class="mt-2"
          role="assistant"
          :copy-text="assistantPlainText"
          :menu-time="calendarTimestamp"
          :full-time="fullTimestamp"
          align="start"
          :persistent="isLastMessage"
          :streaming="message.streaming"
          :on-retry="canRetryLatestAssistant ? handleRetry : undefined"
          :on-fork="canForkAssistantMessage && canForkAssistant ? handleFork : undefined"
        />
      </div>
    </div>
  </div>
</template>

<script lang="ts">
import { setCustomComponents } from 'markstream-vue'
import ChatCodeBlock from './chat-code-block.vue'
import { registerSharedMarkdownComponents } from '@/components/markdown'
import ThemedMermaidBlock from '@/components/themed-mermaid-block/index.vue'

// Scope the chat renderer ("chat-msg"): replace markstream's heavy Monaco code
// block (and its font-size/expand/preview toolbar that surfaced raw i18n keys)
// with a clean integral code block — both `code_block` and `shell` map to it so
// shell/bash blocks render identically — and register the shared design-system
// node components (library Checkbox task markers, link-language footnotes).
// Runs once at module load.
registerSharedMarkdownComponents('chat-msg', { code_block: ChatCodeBlock, shell: ChatCodeBlock })
// Mermaid is registered globally so the appearance preference wins over the
// markstream default (which only follows the host renderer's isDark flag). One
// registration covers chat + file preview + any future MarkdownRender call site.
setCustomComponents({ mermaid: ThemedMermaidBlock })
</script>

<script setup lang="ts">
import { computed, nextTick, ref, toRef, useTemplateRef, watch } from 'vue'
import { CircleAlert, Sparkles } from 'lucide-vue-next'
import { formatRelativeTime, formatDateTime, formatCalendarTime } from '@/utils/date-time'
import { Avatar, AvatarImage, AvatarFallback, Button, Textarea } from '@felinic/ui'
import MarkdownRender, { enableKatex, enableMermaid } from 'markstream-vue'
import { useSettingsStore } from '@/store/settings'
import ToolCallGroup from './tool-call-group.vue'
import ChatAnswersCard from './chat-answers-card.vue'
import { toolSegmentCategoryForBlock } from './tool-call-registry'
import type { ToolSegmentCategory } from './tool-call-registry'
import { finalizeReasoning, markReasoningSeen } from './reasoning-timing'
import AttachmentBlock from './attachment-block.vue'
import CollapsibleUserText from './collapsible-user-text.vue'
import MessageActions from './message-actions.vue'
import BackgroundTaskBlock from './background-task-block.vue'
import ChannelBadge from '@/components/chat-list/channel-badge/index.vue'
import { useUserStore } from '@/store/user'
import { useI18n } from 'vue-i18n'
import type {
  AttachmentItem,
  ChatMessage,
  ContentBlock,
  ToolCallBlock as ToolCallBlockType,
  ThinkingBlock as ThinkingBlockType,
  AttachmentBlock as AttachmentBlockType,
} from '@/store/chat-list'
import { structuredToolResult } from '@/store/chat-list.normalize'

import { resolveUrl } from '../composables/useMediaGallery'
import { useElementVisibility } from '@vueuse/core'


enableKatex()
enableMermaid()


const settingsStore = useSettingsStore()
const isDark = computed(() => settingsStore.isDark)
const codeBlockTheme = computed(() => ({
  light: settingsStore.shikiThemeLight,
  dark: settingsStore.shikiThemeDark,
}))

const messageEl = useTemplateRef('messageItem')
const emit = defineEmits<{
  active: [isActive: boolean, { id: string, top: number,  }]
  editMessage: [messageId: string, text: string, done: (started: boolean) => void]
  forkMessage: [messageId: string]
}>()

const props = defineProps<{
  message: ChatMessage
  botId?: string
  // Group layout for third-party synced threads: every turn left-aligned with
  // an avatar + sender name + channel badge (including the bot's own replies).
  channelThread?: boolean
  channelPlatform?: string
  botName?: string
  botAvatarUrl?: string
  onOpenMedia?: (src: string) => void
  onReplyClick?: (messageId: string) => void
  onRetryMessage?: (messageId: string) => void
  canRetryLatestAssistant?: boolean
  canEditLatestUser?: boolean
  canForkAssistant?: boolean
  isScrolling: boolean
  isLastMessage?: boolean
}>()

const userStore = useUserStore()

const isVisible = useElementVisibility(messageEl, {
  threshold: 0.1
})

watch([isVisible, toRef(props, 'isScrolling')], () => { 
  emit('active', isVisible.value, { id: props.message.id, top: ((messageEl.value?.getBoundingClientRect().top ?? 0) - 48) })
}, {
  immediate: true,
  deep:true
})

const isSelf = computed(() =>
  props.message.role !== 'user' || props.message.isSelf !== false,
)


const { t, tm, rt, locale } = useI18n()
const editTextarea = ref<InstanceType<typeof Textarea> | null>(null)
const isEditingUserMessage = ref(false)
const editDraft = ref('')
const editSubmitting = ref(false)

function handleRetry() {
  const messageId = (props.message.serverId ?? props.message.id).trim()
  if (messageId) props.onRetryMessage?.(messageId)
}

function handleFork() {
  const messageId = (props.message.serverId ?? props.message.id).trim()
  if (messageId) emit('forkMessage', messageId)
}

// The pre-stream "running" line picks one phrase and holds it for the turn:
// seeded by the message id so it stays put across re-renders/refetches instead
// of flickering between phrases on every reactive update.
const thinkingHint = computed(() => {
  const hints = tm('chat.process.thinkingHints') as unknown[]
  if (!Array.isArray(hints) || hints.length === 0) return t('chat.thinking')
  let seed = 0
  for (const ch of props.message.id) seed = (seed + ch.charCodeAt(0)) % 100000
  return rt(hints[seed % hints.length] as Parameters<typeof rt>[0])
})


const replySenderLabel = computed(() => {
  if (props.message.role !== 'user') return ''
  return props.message.reply?.sender || props.message.reply?.message_id || t('chat.unknownMessage')
})

const forwardSenderLabel = computed(() => {
  if (props.message.role !== 'user') return ''
  return props.message.forward?.sender
    || props.message.forward?.from_conversation_id
    || props.message.forward?.from_user_id
    || t('chat.unknownMessage')
})

const canJumpReply = computed(() =>
  props.message.role === 'user'
  && !!props.message.reply?.message_id?.trim()
  && typeof props.onReplyClick === 'function',
)

const replyThumbnail = computed<AttachmentItem | null>(() => {
  if (props.message.role !== 'user') return null
  return (props.message.reply?.attachments ?? []).find((att) => isImageAttachment(att) && resolveUrl(att)) ?? null
})

const replyThumbnailSrc = computed(() => replyThumbnail.value ? resolveUrl(replyThumbnail.value) : '')

const replyPreviewLabel = computed(() => {
  if (props.message.role !== 'user') return ''
  const preview = props.message.reply?.preview?.trim()
  if (preview) return preview
  return replyThumbnailSrc.value ? t('chat.replyPhoto') : ''
})

const skillActivation = computed(() => {
  if (props.message.role !== 'user') return null
  if (props.message.userMessageKind !== 'skill_activation' && !props.message.skillActivation) return null
  return props.message.skillActivation ?? null
})

const isSkillActivationMessage = computed(() => skillActivation.value !== null)

const skillActivationSkills = computed(() => skillActivation.value?.skills ?? [])

const skillActivationDisplayNames = computed(() =>
  skillActivationSkills.value
    .map(skill => (skill.display_name || skill.name || '').trim())
    .filter(Boolean),
)

const skillActivationTitle = computed(() =>
  skillActivationDisplayNames.value.length > 1
    ? t('chat.skillActivation.titleMany')
    : t('chat.skillActivation.title'),
)

const skillActivationNames = computed(() => {
  return skillActivationDisplayNames.value.join(', ')
})

const skillActivationPrompt = computed(() => skillActivation.value?.prompt?.trim() ?? '')

const userBubbleText = computed(() => {
  if (props.message.role !== 'user') return ''
  const text = cleanUserText(props.message.text)
  if (!isSkillActivationMessage.value) return text
  if (skillActivationPrompt.value) return skillActivationPrompt.value
  if (text.startsWith('/') || text.startsWith('The user activated the following skill for this turn without an additional prompt:')) {
    return ''
  }
  return text
})

const skillActivationCopyText = computed(() => {
  if (!skillActivation.value) return ''
  const prompt = userBubbleText.value
  if (prompt) return prompt
  return skillActivationNames.value || skillActivationTitle.value
})

function isImageAttachment(att: AttachmentItem): boolean {
  const type = String(att.type ?? '').toLowerCase()
  if (type === 'image' || type === 'gif') return true
  const mime = String(att.mime ?? '').toLowerCase()
  return mime.startsWith('image/')
}

function handleReplyClick() {
  if (props.message.role !== 'user') return
  const messageId = props.message.reply?.message_id?.trim()
  if (!messageId || !props.onReplyClick) return
  props.onReplyClick(messageId)
}

function cleanUserText(content?: string): string {
  if (!content) return ''
  return content
    .split('\n')
    .filter((line) => !/^\[attachment:\w+\]\s/.test(line.trim()))
    .join('\n')
    .trim()
}

const cleanCurrentUserText = computed(() =>
  props.message.role === 'user' ? cleanUserText(props.message.text) : '',
)

const canEditUserMessage = computed(() =>
  props.message.role === 'user'
  && !props.message.streaming
  && props.message.__optimistic !== true
  && props.canEditLatestUser === true
  && props.message.attachments.length === 0
  && cleanCurrentUserText.value.length > 0
  && bubbleSelf.value,
)

const canForkAssistantMessage = computed(() =>
  props.message.role === 'assistant'
  && !props.message.streaming
  && props.message.__optimistic !== true,
)

const canSubmitEdit = computed(() =>
  props.message.role === 'user'
  && props.canEditLatestUser === true
  && editDraft.value.trim().length > 0
  && editDraft.value.trim() !== cleanCurrentUserText.value.trim()
  && !editSubmitting.value,
)

function handleEdit() {
  if (!canEditUserMessage.value || props.message.role !== 'user') return
  editDraft.value = cleanCurrentUserText.value
  isEditingUserMessage.value = true
  void nextTick(() => {
    const el = editTextarea.value?.$el
    const textarea = el instanceof HTMLTextAreaElement
      ? el
      : el?.querySelector?.('textarea')
    if (!textarea) return
    textarea.focus()
    textarea.setSelectionRange(textarea.value.length, textarea.value.length)
  })
}

function cancelEdit() {
  if (editSubmitting.value) return
  isEditingUserMessage.value = false
  editDraft.value = ''
}

async function submitEdit() {
  if (!canSubmitEdit.value || props.message.role !== 'user') return
  editSubmitting.value = true
  const messageId = (props.message.serverId ?? props.message.id).trim()
  emit('editMessage', messageId, editDraft.value.trim(), (started) => {
    editSubmitting.value = false
    if (started) {
      isEditingUserMessage.value = false
      editDraft.value = ''
    }
  })
}

// Element-level script tag for BLOCK typography only — leading (CJK packs
// tighter, so it loosens) and the ~3% Latin size shave. Per-glyph WEIGHT in mixed
// runs is NOT done here (one font-weight can't split a run); that lives in the
// .chat-cjk / .chat-latin spans emitted by md-text and the user bubble. A block
// is 'zh' if it contains any CJK (its leading should breathe for the CJK lines).
const CJK_RE = /[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\uff66-\uff9f]/
function contentLang(content?: string): 'zh' | 'en' {
  return content && CJK_RE.test(content) ? 'zh' : 'en'
}

const contentClass = computed(() => {
  // The user bubble caps a little tighter than the assistant column so a long
  // prompt doesn't sprawl most of the width before wrapping. `w-full` makes the
  // wrapper actually OCCUPY that capped column instead of shrinking to the
  // bubble, so the hover scope (group/msg) that reveals the action row reaches
  // left into the empty space beside a short bubble — wide, but capped at 70%
  // so it never extends to the far left edge. The bubble itself stays
  // right-aligned via the inner `items-end`.
  if (props.message.role === 'user') return 'w-full max-w-[70%]'
  return 'flex-1 max-w-full'
})

// In a synced channel thread the bot is just another participant, so its own
// replies lose the right-aligned "self bubble" treatment and read like everyone
// else's (left-aligned, plain surface).
const bubbleSelf = computed(() => isSelf.value && !props.channelThread)

// Resolve the avatar/name for whoever sent this turn: the bot for assistant
// replies, the signed-in user for own messages, the remote sender otherwise.
const avatarSrc = computed(() => {
  if (props.message.role === 'assistant') return props.botAvatarUrl ?? ''
  if (props.message.role === 'user') {
    return isSelf.value ? (userStore.userInfo.avatarUrl ?? '') : (props.message.senderAvatarUrl ?? '')
  }
  return ''
})

const avatarName = computed(() => {
  if (props.message.role === 'assistant') return props.botName ?? ''
  if (props.message.role === 'user') {
    return isSelf.value
      ? (userStore.userInfo.displayName || userStore.userInfo.username || '')
      : (props.message.senderDisplayName ?? '')
  }
  return ''
})

const avatarFallback = computed(() => (avatarName.value || '?').slice(0, 2).toUpperCase())

// The bot's own turns carry no platform tag; fall back to the thread's channel
// so its avatar still gets the same channel badge as the human participants.
const avatarPlatform = computed(() => {
  const fromMessage = props.message.role === 'user' ? (props.message.platform ?? '') : ''
  return fromMessage || props.channelPlatform || ''
})

// Channel threads show an avatar for every participant; the local chat only
// shows one for remote users (self/bot stay avatar-less to keep it compact).
const showAvatar = computed(() => {
  if (props.channelThread) {
    return props.message.role === 'user' || props.message.role === 'assistant'
  }
  return props.message.role === 'user' && !isSelf.value
})

const showSenderName = computed(() =>
  Boolean(props.channelThread)
  && (props.message.role === 'user' || props.message.role === 'assistant'),
)

const userAttachmentBlock = computed<AttachmentBlockType | null>(() => {
  if (props.message.role !== 'user' || props.message.attachments.length === 0) return null
  return {
    id: -1,
    type: 'attachments',
    attachments: props.message.attachments,
  }
})

// With attachments stacked above the bubble, the corner that meets them tightens
// to a small radius so it tucks into the attachment stack and reads as one
// connected unit — noticeably sharper than the attachment card's own corner; the
// other three corners keep the full bubble radius. 7px (radius token − 3px) sits
// just under the card's corner so the fold is clearly tighter without going razor
// sharp. The folded corner follows the bubble's alignment side — top-right for own
// messages, top-left for left-aligned (channel) messages.
const userBubbleRadiusClass = computed(() => {
  if (!userAttachmentBlock.value) return 'rounded-2xl'
  return bubbleSelf.value
    ? 'rounded-2xl rounded-tr-[calc(var(--radius)-3px)]'
    : 'rounded-2xl rounded-tl-[calc(var(--radius)-3px)]'
})

function hasLaterAssistantMessage(index: number): boolean {
  return props.message.role === 'assistant' && props.message.messages.slice(index + 1).length > 0
}

function isAssistantBlockStreaming(index: number): boolean {
  return props.message.role === 'assistant' && props.message.streaming && !hasLaterAssistantMessage(index)
}

const hasVisibleAssistantBlocks = computed(() =>
  props.message.role === 'assistant'
  && props.message.messages.some(isVisibleAssistantBlock),
)

const shouldRenderMessage = computed(() =>
  props.message.role !== 'assistant' || hasVisibleAssistantBlocks.value || props.message.streaming,
)

function isVisibleAssistantBlock(block: ContentBlock): boolean {
  if (block.type === 'tool') return true
  if (block.type === 'text' || block.type === 'error') return Boolean(block.content)
  if (block.type === 'attachments') return block.attachments.length > 0
  return true
}

// Project the flat assistant block list into render nodes.
//  - A "process" node is a run of consecutive tool + reasoning blocks. It splits
//    by tool category (read-only "explore" vs side-effecting "action" vs "gui")
//    so reads and edits don't merge into one bucket, while browser/computer
//    observe+action steps stay together as one browsing activity; reasoning
//    rides along with whichever segment it sits next to (never standalone).
//  - Every other block type (text / error / attachments) keeps its place.
// Keyed by stable block id.
type ProcessNode = { kind: 'process'; key: string; items: ContentBlock[]; cat: ToolSegmentCategory | null; lastIndex: number }
type AnswersNode = { kind: 'answers'; key: string; block: ContentBlock; index: number }
type BlockNode = { kind: 'block'; key: string; block: ContentBlock; index: number }
type RenderNode = ProcessNode | AnswersNode | BlockNode

// A completed ask_user is content, not process: it carries the Q&A record, so
// it breaks out of the tool group and renders as an answers card in the text
// flow. A pending one has no result yet and stays in the group (its input
// form lives elsewhere). Two completion signals, because a canceled/expired/
// failed request can end WITHOUT a tool result — the terminal state then
// lives only in userInput.status (the backend's user_input_request event
// sets UserInput but never a result). The result path also covers pre-v2
// history where userInput is absent.
const ASK_USER_TERMINAL_STATUSES = new Set(['submitted', 'canceled', 'expired', 'failed'])

function isCompletedAskUser(block: ContentBlock): boolean {
  if (block.type !== 'tool') return false
  const b = block as ToolCallBlockType
  if (b.toolName !== 'ask_user') return false
  const result = structuredToolResult(b.result)
  if (Array.isArray(result.answers)) return true
  if (typeof result.status === 'string' && ASK_USER_TERMINAL_STATUSES.has(result.status)) return true
  const s = b.userInput?.status
  return typeof s === 'string' && ASK_USER_TERMINAL_STATUSES.has(s)
}

const renderNodes = computed<RenderNode[]>(() => {
  if (props.message.role !== 'assistant') return []
  const nodes: RenderNode[] = []
  let run: ProcessNode | null = null
  props.message.messages.forEach((block, index) => {
    if (!isVisibleAssistantBlock(block)) return
    if (isCompletedAskUser(block)) {
      run = null
      nodes.push({ kind: 'answers', key: `a${block.id}`, block, index })
      return
    }
    if (block.type === 'tool' || block.type === 'reasoning') {
      const cat = block.type === 'tool'
        ? toolSegmentCategoryForBlock(block as ToolCallBlockType)
        : null
      if (!run) {
        run = { kind: 'process', key: `p${block.id}`, items: [block], cat, lastIndex: index }
        nodes.push(run)
      } else if (cat !== null && run.cat !== null && cat !== run.cat) {
        // Category switch (e.g. finished reading, now editing) → new segment.
        run = { kind: 'process', key: `p${block.id}`, items: [block], cat, lastIndex: index }
        nodes.push(run)
      } else {
        run.items.push(block)
        run.lastIndex = index
        if (run.cat === null && cat !== null) run.cat = cat
      }
    } else {
      run = null
      nodes.push({ kind: 'block', key: `b${block.type}-${block.id}`, block, index })
    }
  })
  return nodes
})

// Centralized reasoning timing. The stream carries no duration, so we measure
// it client-side: stamp a reasoning block the first time it appears mid-stream,
// and finalize it once a later block supersedes it (or the turn ends). This
// covers every reasoning step — including ones immediately followed by a tool
// call — so they show a real "Thought for Ns" instead of a bare "Thought".
watch(
  () => (props.message.role === 'assistant' && props.message.streaming
    ? props.message.messages.map(block => `${block.type}:${block.id}`).join('|')
    : ''),
  () => {
    if (props.message.role !== 'assistant' || !props.message.streaming) return
    const blocks = props.message.messages
    blocks.forEach((block, index) => {
      if (block.type !== 'reasoning') return
      const content = (block as ThinkingBlockType).content ?? ''
      markReasoningSeen(content)
      if (index < blocks.length - 1) finalizeReasoning(content)
    })
  },
  { immediate: true },
)

watch(
  () => props.message.role === 'assistant' && props.message.streaming,
  (streaming, was) => {
    if (!was || streaming || props.message.role !== 'assistant') return
    props.message.messages.forEach((block) => {
      if (block.type === 'reasoning') finalizeReasoning((block as ThinkingBlockType).content ?? '')
    })
  },
)

const relativeTimestamp = computed(() =>
  formatRelativeTime(props.message.timestamp, { locale: locale.value }),
)
const fullTimestamp = computed(() =>
  formatDateTime(props.message.timestamp, { locale: locale.value }),
)
// Precise, calendar-anchored time shown inside the assistant "more" menu —
// "Today 10:11 PM" rather than the decaying "3 hours ago".
const calendarTimestamp = computed(() =>
  formatCalendarTime(props.message.timestamp, { locale: locale.value }),
)

const userCopyText = computed(() =>
  props.message.role === 'user'
    ? (isSkillActivationMessage.value ? skillActivationCopyText.value : userBubbleText.value)
    : '',
)
const assistantPlainText = computed(() => {
  if (props.message.role !== 'assistant') return ''
  return props.message.messages
    .filter((block): block is Extract<ContentBlock, { type: 'text' }> =>
      block.type === 'text' && Boolean((block as { content?: string }).content),
    )
    .map(block => block.content)
    .join('\n\n')
})

</script>
