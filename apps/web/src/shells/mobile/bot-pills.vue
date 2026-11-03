<template>
  <!-- Every pill is the 48px control height and always shows the name; the
       selected bot also shows its avatar, so selecting a pill only widens it.
       The row scrolls sideways when there are many bots, and fades out at its
       right end instead of cutting a pill off at a hard edge. Its scrollbar is
       hidden in both syntaxes: the Android WebView does not always honor
       scrollbar-width.

       The selected pill is inverted: foreground fill, page-color text. The
       avatar fallback is derived from those two colors only, so one rule
       holds in both themes: its fill is the pill fill moved 20% toward the
       pill text, and its initials use the pill text color.

       The pills opt out of the press ripple: selecting one already animates,
       and the two effects on one tap read as noise. -->
  <div
    ref="rowEl"
    class="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto pl-4 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden [mask-image:linear-gradient(to_right,black_calc(100%-1.5rem),transparent)]"
  >
    <button
      v-for="bot in bots"
      :key="bot.id"
      type="button"
      data-press-none
      :data-bot-id="bot.id"
      :aria-pressed="bot.id === selectedId"
      class="flex h-12 shrink-0 items-center rounded-full text-[0.9375rem] whitespace-nowrap transition-[padding,background-color,color] duration-200 ease-[cubic-bezier(0.2,0,0,1)]"
      :class="bot.id === selectedId
        ? 'bg-foreground pr-4 pl-1.5 font-medium text-background'
        : 'bg-muted px-4'"
      @click="onClick(bot)"
    >
      <span
        class="flex shrink-0 overflow-hidden transition-[width,margin] duration-200 ease-[cubic-bezier(0.2,0,0,1)]"
        :class="bot.id === selectedId ? 'mr-2 w-9' : 'w-0'"
      >
        <Avatar
          class="size-9 shrink-0 transition-[opacity,scale] duration-200 ease-[cubic-bezier(0.2,0,0,1)]"
          :class="bot.id === selectedId ? 'scale-100 opacity-100' : 'scale-75 opacity-0'"
        >
          <AvatarImage
            v-if="bot.avatar_url"
            :src="bot.avatar_url"
            alt=""
          />
          <AvatarFallback class="pill-avatar-fallback text-xs font-medium text-background">
            {{ avatarInitials(label(bot), 'B') }}
          </AvatarFallback>
        </Avatar>
      </span>
      {{ label(bot) }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { BotsBot } from '@memohai/sdk'
import { Avatar, AvatarFallback, AvatarImage } from '@felinic/ui'
import { avatarInitials } from '@/composables/useAvatarInitials'

const props = defineProps<{
  bots: BotsBot[]
  currentId: string | null | undefined
  // Switches the app to the bot; resolves once the switch has settled.
  select: (bot: BotsBot) => Promise<void>
}>()

// The pill animation (duration-200 above) runs on the main thread, as any
// width change does. Switching bots blocks that thread for a few hundred
// milliseconds on a phone (new session list, route, chat page), so the switch
// starts only after the pill has finished moving instead of freezing it
// halfway.
const PILL_MOTION_MS = 200

const pendingId = ref<string | null>(null)
const selectedId = computed(() => pendingId.value ?? props.currentId)

function label(bot: BotsBot) {
  return bot.display_name || bot.name || bot.id || ''
}

const rowEl = ref<HTMLElement | null>(null)
function reveal(id: string | null | undefined, behavior: ScrollBehavior) {
  if (!id) return
  rowEl.value
    ?.querySelector<HTMLElement>(`[data-bot-id="${id}"]`)
    ?.scrollIntoView({ inline: 'nearest', block: 'nearest', behavior })
}

async function onClick(bot: BotsBot) {
  const id = bot.id ?? ''
  if (!id || bot.status === 'error' || id === selectedId.value) return
  pendingId.value = id
  reveal(id, 'smooth')
  await new Promise(resolve => window.setTimeout(resolve, PILL_MOTION_MS))
  // A later tap took over while this one was still animating.
  if (pendingId.value !== id) return
  try {
    await props.select(bot)
  }
  finally {
    if (pendingId.value === id) pendingId.value = null
  }
}

watch(() => props.currentId, id => reveal(id, 'smooth'))
onMounted(() => reveal(props.currentId, 'instant'))
</script>

<style scoped>
/* The selected pill's fill moved 20% toward its text color. */
.pill-avatar-fallback {
  background-color: color-mix(in srgb, var(--foreground) 80%, var(--background));
}
</style>
