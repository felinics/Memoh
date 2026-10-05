<template>
  <div
    data-slot="input-group"
    role="group"
    :aria-label="label || undefined"
    class="chat-composer-edge chat-composer-member rounded-2xl bg-surface-composer"
    :class="compact ? 'px-(--composer-pad) py-1' : 'px-(--composer-pad) py-3'"
  >
    <slot />
  </div>
</template>

<script setup lang="ts">
// ComposerCapsule — the ONE implementation of the composer-dock shell. Every
// box that lives in the dock wears this: the ask_user capsule, the stack-tier
// panel (approvals / command results / errors), and any future dock member.
// The composer itself is the deliberate exception — its two-row flex-wrap
// layout (textarea row + controls row) is its own, so it keeps its own
// markup; everything else shares this shell so the chrome (input-group edge,
// surface, radius, padding) can never drift into N hand-copies. Keep the
// chrome classes here in lockstep with the composer div in chat-pane.vue.
// Inline padding is the composer's own --composer-pad (style.css), so a
// member's content box starts exactly where the composer's does.
// `chat-composer-member` makes the edge mirror the composer's current tier
// (rest / docked / focus), resolved on the ComposerDock ancestor — see
// style.css — so a stacked capsule never reads as a different material.
defineProps<{
  // Accessible region name; omit when the content already names itself.
  label?: string
  // Single-line status bars keep the shell with less vertical padding.
  compact?: boolean
}>()
</script>
