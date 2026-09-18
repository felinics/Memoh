<template>
  <!--
    Owner for the "scrollable framed muted text preview" shape that recurred
    across detail panels (browser snapshot, computer snapshot,
    web-fetch content, spawn task text) with drifting max-h buckets
    (32/48/48/48/60 — 48 is the majority across these five, so that's what
    this owns). Two shapes were deliberately left local instead of forced in:
    - tool-call-detail-send's message-body preview uses `text-foreground/90`
      (primary emphasis) + `py-1.5` + `max-h-96` — it renders the send action's
      actual primary content, not a secondary output/log snippet, so it reads
      at a different weight on purpose.
    - Diff/patch previews (tool-call-detail-edit / -apply-patch) are
      syntax-highlighted via Shiki and rendered through `v-html`, a different
      pipeline than plain interpolated text.
  -->
  <pre class="max-h-48 overflow-x-auto overflow-y-auto rounded-sm bg-muted/30 px-2 py-1 text-xs text-muted-foreground whitespace-pre-wrap break-all"><slot /></pre>
</template>
