<script setup lang="ts">
// Feedback: notices, toasts, empty states. The global <Toaster> already lives
// in App.vue, so this just fires toast() — no extra mount here.
import {
  Button,
  CalloutBanner,
  Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle,
  Progress,
} from '@felinic/ui'
import { toast } from '@felinic/ui'
import { ErrorIcon } from '@memohai/icon/ui'
import { CircleAlert, Inbox } from 'lucide-vue-next'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import SectionShell from '../components/SectionShell.vue'
import Specimen from '../components/Specimen.vue'
import ComposerPanel from '@/pages/home/components/composer-panel.vue'

// Fire a burst so the stack reads as a quiet, fully-readable column — newest on
// top, every card's content visible (no depth stacking that hides prior messages).
function runStackDemo() {
  const fns = [
    () => toast.success('Bot saved'),
    () => toast.info('Model synced'),
    () => toast.warning('Channel reconnecting…'),
    () => toast('Workspace synced'),
  ]
  fns.forEach((fn, i) => setTimeout(fn, i * 220))
}

// Live bar — mimics the container image pull / create SSE progress the server
// streams, looping 0 → 100 so the transition reads at a glance.
const liveProgress = ref(8)
let progressTimer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  progressTimer = setInterval(() => {
    // Land exactly on 100 (clamp the last step), then loop back to 0 — reka's
    // ProgressRoot rejects any value above max (100).
    liveProgress.value = liveProgress.value >= 100 ? 0 : Math.min(100, liveProgress.value + 12)
  }, 900)
})
onBeforeUnmount(() => clearInterval(progressTimer))

const noticeTones = ['neutral', 'warning', 'destructive'] as const

// Real ComposerPanel fed canned state, so the dock's error / command-error
// sections render exactly as in chat without reproducing a failure.
const composerError = 'Model "gpt-5.5" is unavailable for this provider. Pick another model and try again.'
const commandErrorPanel = {
  isError: true,
  title: '/model',
  text: 'Unknown model "claude-x". Run /model to list available models.',
  items: [],
}
const rawChatError = 'Upstream 429: rate limit exceeded for organization org_7Hq2 (retry after 38s)'
</script>

<template>
  <SectionShell
    id="feedback"
    label="Feedback"
    description="Notices, toast notifications, and empty states."
  >
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <div class="lg:col-span-2">
        <Specimen
          label="<CalloutBanner :tone>"
          note="the ONE framed notice (errors, warnings, neutral info). Tone lives in the icon + surface wash only; text stays foreground / muted. The icon centers on the first line at any wrap count."
        >
          <div class="grid w-full grid-cols-1 gap-3 lg:grid-cols-3">
            <CalloutBanner
              v-for="tone in noticeTones"
              :key="tone"
              :tone="tone"
              :title="`${tone.charAt(0).toUpperCase()}${tone.slice(1)} notice`"
              description="A sentence that explains what happened and what to do next."
            />
          </div>
        </Specimen>
      </div>

      <Specimen
        label="<CalloutBanner tone=&quot;destructive&quot; :description> + #details"
        note="description-only promotes the message to the first-line rung; #details carries the raw backend error in mono caption"
      >
        <div class="flex w-full flex-col gap-3">
          <CalloutBanner
            tone="destructive"
            description="Failed to load dependency script."
          />
          <CalloutBanner
            tone="destructive"
            title="Installation failed"
            description="Open the log for the full output, or retry."
          >
            <template #details>
              exit status 1: npm ERR! code ERESOLVE
            </template>
          </CalloutBanner>
        </div>
      </Specimen>

      <Specimen
        label="<CalloutBanner> + actions · clickable"
        note="default slot = trailing actions (stack under the text on narrow widths); clickable makes the whole surface the button"
      >
        <div class="flex w-full flex-col gap-3">
          <CalloutBanner
            tone="destructive"
            title="Cannot reach provider"
            description="Check the base URL and network, then retry."
          >
            <Button
              variant="outline"
              size="sm"
            >
              Retry
            </Button>
          </CalloutBanner>
          <CalloutBanner
            tone="warning"
            clickable
            title="3 checks failing"
            description="View diagnostics for details."
          />
        </div>
      </Specimen>

      <Specimen
        label="chat transcript — <CalloutBanner size=&quot;sm&quot;>"
        note="inline error / notice blocks in an assistant turn (message-item.vue)"
      >
        <div class="flex w-full flex-col gap-2">
          <p class="text-control text-foreground">
            Let me fetch the latest release notes for you.
          </p>
          <CalloutBanner
            tone="destructive"
            size="sm"
            :description="rawChatError"
          />
          <CalloutBanner
            tone="warning"
            size="sm"
            description="Web search is unavailable for this turn; answering from memory."
          />
        </div>
      </Specimen>

      <Specimen
        label="error glyph — <ErrorIcon> (@memohai/icon/ui) vs lucide CircleAlert"
        note="12 / 14 / 16px, top row the error glyph every destructive notice and field error uses, bottom row the lucide original it replaced"
      >
        <div class="flex flex-col gap-3 text-destructive">
          <div class="flex items-center gap-4">
            <ErrorIcon class="size-3" />
            <ErrorIcon class="size-3.5" />
            <ErrorIcon class="size-4" />
          </div>
          <div class="flex items-center gap-4">
            <CircleAlert class="size-3" />
            <CircleAlert class="size-3.5" />
            <CircleAlert class="size-4" />
          </div>
        </div>
      </Specimen>

      <Specimen
        label="composer dock — <ComposerPanel> (real component)"
        note="send / model-switch error and a failed command result, stacked above a docked composer; the capsule edge follows the composer's tier via the dock ancestor"
      >
        <div class="chat-composer-dock flex w-full flex-col gap-2">
          <ComposerPanel
            :approvals="[]"
            :command-panel="commandErrorPanel"
            :error-message="composerError"
          />
          <!-- Reference only: mirrors the docked composer's chrome classes in
               chat-pane.vue so the two edges can be compared side by side. -->
          <div
            data-slot="input-group"
            class="chat-composer-edge chat-composer-docked rounded-2xl bg-surface-composer p-(--composer-pad)"
          >
            <p class="pl-2 pr-1 pt-2 pb-1.5 text-base text-muted-foreground">
              Ask anything…
            </p>
          </div>
        </div>
      </Specimen>

      <div class="lg:col-span-2">
        <Specimen
          label="toast(...) — variants"
          note="real global Toaster (App.vue, top-right) · semantic color on the icon only · skin in style.css"
        >
          <Button
            variant="outline"
            size="sm"
            @click="toast('Workspace synced')"
          >
            Default
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.success('Bot saved', { description: 'Your changes are live.' })"
          >
            Success
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.warning('Reconnecting…', { description: 'The channel dropped and is retrying.' })"
          >
            Warning
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.error('Save failed', { description: 'Check your network and try again.' })"
          >
            Error
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.info('New model available', { description: 'gpt-5.5 is ready to use.' })"
          >
            Info
          </Button>
        </Specimen>
      </div>

      <div class="lg:col-span-2">
        <Specimen
          label="toast(...) — long content auto-shaping"
          note="a long / unbreakable, titleless blob (raw backend error, path, URL, connection string) is auto-shaped into a variant heading + gray description (like 'Upload failed') instead of a bold wall of title text. Short titles stay single-line."
        >
          <Button
            variant="outline"
            size="sm"
            @click="toast.error('not found: readdir: open /Users/qqqqqf/.memoh/workspaces/1/documents/项目: no such file or directory')"
          >
            Raw backend error
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast('https://example.com/api/v1/workspaces/1/documents/very/deeply/nested/path/that/never/breaks/file.tar.gz?token=abcdefghijklmnopqrstuvwxyz0123456789')"
          >
            Unbreakable URL
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.error('Upload failed', { description: 'Could not write /Users/qqqqqf/.memoh/workspaces/1/documents/项目/long-unbreakable-filename-without-spaces-1234567890.bin — the destination directory does not exist.' })"
          >
            Long title + desc
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.warning('connection_string=postgresql://memoh_user:supersecretpassword@db.internal.memoh.example.com:5432/memoh_production?sslmode=require', { action: { label: 'Copy', onClick: () => {} } })"
          >
            Long blob + action
          </Button>
        </Specimen>
      </div>

      <div class="lg:col-span-2">
        <Specimen
          label="toast(...) — rich behaviors"
          note="action · single-line (Retry) · long (10s) · Stack fires 4 (full column) · dismiss all"
        >
          <Button
            variant="outline"
            size="sm"
            @click="toast('Message archived', { action: { label: 'Undo', onClick: () => {} } })"
          >
            With action
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast('Workspace sync paused', { action: { label: 'Retry', onClick: () => {} } })"
          >
            Single-line action
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast.warning('MCP pencil: Server disconnected.', { description: 'For troubleshooting guidance, please visit our debugging documentation.', action: { label: 'Open developer settings', onClick: () => {} } })"
          >
            Long text + Action
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="toast('Heads up', { duration: 10000 })"
          >
            Long (10s)
          </Button>
          <Button
            variant="outline"
            size="sm"
            @click="runStackDemo"
          >
            Stack ×4
          </Button>
          <Button
            variant="ghost"
            size="sm"
            @click="toast.dismiss()"
          >
            Dismiss all
          </Button>
        </Specimen>
      </div>

      <div class="lg:col-span-2">
        <Specimen
          label="<Progress>"
          note="neutral rail + selection-blue fill — workspace setup / upload progress"
        >
          <div class="flex w-full max-w-md flex-col gap-4">
            <Progress :model-value="30" />
            <Progress :model-value="66" />
            <Progress :model-value="100" />
            <Progress :model-value="liveProgress" />
          </div>
        </Specimen>
      </div>

      <div class="lg:col-span-2">
        <Specimen label="<Empty>">
          <Empty class="w-full max-w-sm">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <Inbox />
              </EmptyMedia>
              <EmptyTitle>No messages</EmptyTitle>
              <EmptyDescription>You're all caught up. New messages will appear here.</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button
                variant="primary"
                size="sm"
              >
                Refresh
              </Button>
            </EmptyContent>
          </Empty>
        </Specimen>
      </div>
    </div>
  </SectionShell>
</template>
