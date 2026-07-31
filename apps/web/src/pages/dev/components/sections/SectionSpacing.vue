<script setup lang="ts">
import {
  Avatar,
  AvatarFallback,
  Badge,
  Button,
  Input,
  Switch,
} from '@felinic/ui'
import {
  Box,
  Globe,
  Inbox,
  Plus,
  Search,
  Settings2,
  Terminal,
} from 'lucide-vue-next'
import { ref } from 'vue'
import { BackendCard, CalloutBanner, ConfirmDeleteDialog, ExpandableSettingsRow, FieldStack, FormStack, InlineLoadingRow, MetricReadout, PageShell, PanePlaceholder, PersonaTile, SectionGroup, SettingsRow, SettingsSection } from '@felinic/ui'
import SectionShell from '../components/SectionShell.vue'
import Specimen from '../components/Specimen.vue'

// Demo-only state for the two interactive specimens below (ExpandableSettingsRow,
// ConfirmDeleteDialog) — the wall needs a real ref to wire v-model against, same as
// any real caller, but there's no backing data so the confirm handler just fakes a
// short async delete instead of calling a store action.
const expandableOpen = ref(false)
const confirmDeleteOpen = ref(false)
const confirmDeleteLoading = ref(false)

function onConfirmDeleteDemo() {
  confirmDeleteLoading.value = true
  setTimeout(() => {
    confirmDeleteLoading.value = false
    confirmDeleteOpen.value = false
  }, 600)
}

const primitiveRungs = [
  { name: '0.5', value: '2px', class: 'w-0.5', use: 'optical nudge' },
  { name: '1', value: '4px', class: 'w-1', use: 'tight label pair' },
  { name: '1.5', value: '6px', class: 'w-1.5', use: 'text chip' },
  { name: '2', value: '8px', class: 'w-2', use: 'compact control pair' },
  { name: '2.5', value: '10px', class: 'w-2.5', use: 'section title to card' },
  { name: '3', value: '12px', class: 'w-3', use: 'card item gap' },
  { name: '3.5', value: '14px', class: 'w-3.5', use: 'backend card padding' },
  { name: '4', value: '16px', class: 'w-4', use: 'form stack' },
  { name: '5', value: '20px', class: 'w-5', use: 'loose content stack' },
  { name: '6', value: '24px', class: 'w-6', use: 'title to body' },
  { name: '8', value: '32px', class: 'w-8', use: 'section group gap' },
  { name: '10', value: '40px', class: 'w-10', use: 'page top rhythm' },
] as const
</script>

<template>
  <SectionShell
    id="spacing"
    label="Spacing"
    description="Semantic owners for recurring Memoh spacing relationships."
  >
    <div class="grid grid-cols-1 items-start gap-4 xl:grid-cols-2">
      <Specimen
        label="Primitive scale"
        note="values are shared material; roles below decide where they belong"
      >
        <div class="grid w-full gap-2">
          <div
            v-for="rung in primitiveRungs"
            :key="rung.name"
            class="grid grid-cols-[2.5rem_minmax(0,1fr)_3.25rem] items-center gap-3 text-caption"
          >
            <span class="font-mono text-muted-foreground">{{ rung.name }}</span>
            <div class="min-w-0">
              <div :class="['h-2 rounded-full bg-foreground', rung.class]" />
              <p class="mt-1 truncate text-muted-foreground">
                {{ rung.use }}
              </p>
            </div>
            <span class="text-right font-mono text-muted-foreground">{{ rung.value }}</span>
          </div>
        </div>
      </Specimen>

      <Specimen
        label="<SettingsSection> + <SettingsRow>"
        note="row rhythm owns label/description/control spacing inside settings cards"
      >
        <SettingsSection
          title="Runtime"
          class="w-full"
        >
          <SettingsRow
            label="Workspace display"
            description="Expose a browser and desktop surface for this bot."
          >
            <Switch :model-value="true" />
          </SettingsRow>
          <SettingsRow
            label="Workspace"
            description="Rebuild the workspace when runtime files change."
          >
            <Button
              variant="outline"
              size="sm"
            >
              Rebuild
            </Button>
          </SettingsRow>
        </SettingsSection>
      </Specimen>

      <Specimen
        label="Appearance complex row"
        note="source pattern from Appearance; candidate owner, not promoted to a primitive yet"
      >
        <SettingsSection
          title="Typography"
          class="w-full"
        >
          <div class="mx-4 border-b border-border py-3 last:border-b-0">
            <div class="min-w-0">
              <div class="text-sm font-medium text-foreground">
                Code highlighting
              </div>
              <p class="mt-0.5 text-xs text-muted-foreground">
                Choose separate preview themes for light and dark mode.
              </p>
            </div>
            <div class="mt-3 grid gap-3 sm:grid-cols-2">
              <div class="space-y-2">
                <Input
                  model-value="GitHub Light"
                  readonly
                />
                <div class="rounded-md border border-border bg-muted p-3 font-mono text-caption text-muted-foreground">
                  const theme = 'light'
                </div>
              </div>
              <div class="space-y-2">
                <Input
                  model-value="GitHub Dark"
                  readonly
                />
                <div class="rounded-md border border-border bg-muted p-3 font-mono text-caption text-muted-foreground">
                  const theme = 'dark'
                </div>
              </div>
            </div>
          </div>
        </SettingsSection>
      </Specimen>

      <Specimen
        label="<BackendCard>"
        note="list/detail entry spacing for providers, search backends, memory backends"
      >
        <div class="grid w-full grid-cols-1 gap-3 sm:grid-cols-2">
          <BackendCard
            name="OpenRouter"
            subtitle="OpenAI-compatible gateway"
            enabled
          >
            <template #leading>
              <span class="flex size-9 items-center justify-center rounded-full bg-muted text-foreground">
                <Globe class="size-4" />
              </span>
            </template>
          </BackendCard>

          <BackendCard
            name="Remote Runtime"
            subtitle="Connected computer workspace"
          >
            <template #leading>
              <span class="flex size-9 items-center justify-center rounded-full bg-muted text-foreground">
                <Terminal class="size-4" />
              </span>
            </template>
          </BackendCard>
        </div>
      </Specimen>

      <Specimen
        label="<PanePlaceholder>"
        note="centered fill for a whole pane: loading / empty (icon + action) / title emphasis — NOT for inline rows"
      >
        <div class="grid w-full grid-cols-1 gap-3 sm:grid-cols-3">
          <div class="h-40 rounded-md border border-border bg-background">
            <PanePlaceholder loading>
              Loading…
            </PanePlaceholder>
          </div>
          <div class="h-40 rounded-md border border-border bg-background">
            <PanePlaceholder>
              <template #icon>
                <Inbox class="size-10 opacity-30" />
              </template>
              No items yet
              <template #action>
                <Button
                  variant="outline"
                  size="sm"
                >
                  Refresh
                </Button>
              </template>
            </PanePlaceholder>
          </div>
          <div class="h-40 rounded-md border border-border bg-background">
            <PanePlaceholder title="Select a bot">
              Pick one from the sidebar to start chatting.
            </PanePlaceholder>
          </div>
        </div>
      </Specimen>

      <Specimen
        label="<InlineLoadingRow>"
        note="left-aligned in-flow loading row; positioning padding stays with the caller · centered fill → PanePlaceholder · in a button → Button :loading · bare glyph → Spinner"
      >
        <div class="flex w-full flex-col gap-3">
          <InlineLoadingRow>
            Loading…
          </InlineLoadingRow>
          <InlineLoadingRow size="md">
            Loading workspace…
          </InlineLoadingRow>
          <InlineLoadingRow bordered>
            Reading backup file…
          </InlineLoadingRow>
        </div>
      </Specimen>

      <Specimen
        label="<FieldStack> + <FormStack>"
        note="label sits ABOVE the control (space-y-1.5) — distinct from SettingsRow, which puts it BESIDE; FormStack owns the field-to-field rhythm (space-y-4) for a run of them"
      >
        <FormStack class="w-full">
          <FieldStack
            label="Display name"
            for="dev-field-name"
          >
            <Input
              id="dev-field-name"
              placeholder="Assistant"
            />
          </FieldStack>
          <FieldStack
            label="Webhook URL"
            for="dev-field-webhook"
            help="Delivered as a POST with the event payload."
          >
            <Input
              id="dev-field-webhook"
              placeholder="https://"
            />
          </FieldStack>
        </FormStack>
      </Specimen>

      <Specimen
        label="<MetricReadout>"
        note="one cell — the CALLER owns the grid (grid-cols-3 / sm:grid-cols-4); status swaps the value line for a signal dot + label"
      >
        <div class="grid w-full grid-cols-3 gap-3">
          <MetricReadout
            label="CPU"
            value="12%"
            sub="of 2 cores"
          />
          <MetricReadout
            label="Workspace"
            value="Connected"
            status="ok"
          />
          <MetricReadout
            label="Workspace"
            value="Unreachable"
            status="error"
          />
        </div>
      </Specimen>

      <Specimen
        label="<PersonaTile>"
        note="vertical, centered entity/add tile — the counterpart to horizontal BackendCard; do not confuse the two"
      >
        <div class="flex w-full flex-wrap gap-3">
          <PersonaTile name="Aria">
            <template #media>
              <Avatar class="size-14">
                <AvatarFallback>A</AvatarFallback>
              </Avatar>
            </template>
          </PersonaTile>
          <PersonaTile
            name="Add bot"
            variant="add"
          >
            <template #media>
              <Plus class="size-6" />
            </template>
          </PersonaTile>
        </div>
      </Specimen>

      <Specimen
        label="<CalloutBanner>"
        note="framed warning/destructive notice; clickable turns the whole surface into a button with a lead-in chevron — the trailing slot is then usually empty"
      >
        <div class="flex w-full flex-col gap-3">
          <CalloutBanner
            tone="warning"
            title="No search provider configured"
            description="Web search tools are disabled until one is added."
          >
            <Button
              variant="outline"
              size="sm"
            >
              Add provider
            </Button>
          </CalloutBanner>
          <CalloutBanner
            tone="destructive"
            clickable
            title="3 checks failing"
            description="View diagnostics for details."
          />
        </div>
      </Specimen>

      <Specimen
        label="<ExpandableSettingsRow>"
        note="the whole header toggles a body it OWNS, reusing SettingsRow's skeleton as a real <button> — a toggle over a sibling block it doesn't own is not this owner"
      >
        <SettingsSection
          title="Advanced"
          class="w-full"
        >
          <ExpandableSettingsRow
            v-model:open="expandableOpen"
            label="Advanced"
            description="Override the compaction model for this bot."
          >
            <template #expanded>
              <FieldStack
                label="Compaction model"
                help="Falls back to the bot's default chat model."
              >
                <Input placeholder="gpt-4o-mini" />
              </FieldStack>
            </template>
          </ExpandableSettingsRow>
        </SettingsSection>
      </Specimen>

      <Specimen
        label="<ConfirmDeleteDialog>"
        note="sm:max-w-sm confirm skeleton; the CALLER owns the delete call and closes on success — do not hand-roll a Dialog + two-button confirm again"
      >
        <Button
          variant="outline"
          size="sm"
          @click="confirmDeleteOpen = true"
        >
          Delete session
        </Button>
        <ConfirmDeleteDialog
          v-model:open="confirmDeleteOpen"
          title="Delete this session?"
          description="This can't be undone."
          cancel-label="Cancel"
          confirm-label="Confirm"
          :loading="confirmDeleteLoading"
          @confirm="onConfirmDeleteDemo"
        />
      </Specimen>

      <Specimen
        label="<SectionGroup>"
        note="foreground label heading a BARE body (no card of its own) — for pages stacking SEVERAL groups (voice, web-search); a single-group gallery page lets PageShell own the title instead"
        class="xl:col-span-2"
      >
        <SectionGroup
          title="Speaking (TTS)"
          description="Providers used to generate voice replies."
          class="w-full"
        >
          <template #actions>
            <Button
              variant="secondary"
              size="sm"
            >
              <Plus class="size-4" />
              Add
            </Button>
          </template>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <BackendCard
              name="ElevenLabs"
              subtitle="Realistic voice synthesis"
              enabled
            >
              <template #leading>
                <span class="flex size-9 items-center justify-center rounded-full bg-muted text-foreground">
                  <Globe class="size-4" />
                </span>
              </template>
            </BackendCard>

            <BackendCard
              name="Azure Speech"
              subtitle="Cloud text-to-speech"
            >
              <template #leading>
                <span class="flex size-9 items-center justify-center rounded-full bg-muted text-foreground">
                  <Terminal class="size-4" />
                </span>
              </template>
            </BackendCard>
          </div>
        </SectionGroup>
      </Specimen>

      <Specimen
        label="<PageShell>"
        note="one owner for content gutter, title rhythm, actions alignment, and max width"
        class="xl:col-span-2"
      >
        <div class="w-full overflow-hidden rounded-[var(--radius-menu-shell)] bg-background">
          <PageShell
            title="Providers"
            description="Configure model providers and their available models."
          >
            <template #actions>
              <Button
                variant="outline"
              >
                <Plus />
                Add Provider
              </Button>
            </template>

            <div class="space-y-8">
              <SettingsSection title="Provider">
                <SettingsRow
                  label="OpenRouter"
                  description="3 models available"
                >
                  <Badge variant="secondary">
                    Active
                  </Badge>
                </SettingsRow>
              </SettingsSection>

              <SettingsSection title="Models">
                <SettingsRow
                  label="Search"
                  description="Use rows until a denser model-list owner is extracted."
                >
                  <div class="flex items-center gap-2">
                    <Button
                      variant="outline"
                    >
                      <Search />
                      Search
                    </Button>
                  </div>
                </SettingsRow>
              </SettingsSection>
            </div>
          </PageShell>
        </div>
      </Specimen>

      <Specimen
        label="Compression rule"
        note="same value is not the same role; same relationship can share one owner"
        class="xl:col-span-2"
      >
        <div class="grid w-full grid-cols-1 gap-3 lg:grid-cols-3">
          <div class="rounded-[var(--radius-menu-shell)] border border-border bg-card p-4">
            <div class="flex items-center gap-2 text-label font-medium">
              <Box class="size-4" />
              <span>Primitive</span>
            </div>
            <p class="mt-2 text-caption text-muted-foreground">
              `space.4` can be material for forms, card content, and toolbar gaps.
            </p>
          </div>
          <div class="rounded-[var(--radius-menu-shell)] border border-border bg-card p-4">
            <div class="flex items-center gap-2 text-label font-medium">
              <Settings2 class="size-4" />
              <span>Owner</span>
            </div>
            <p class="mt-2 text-caption text-muted-foreground">
              Appearance complex rows show the relationship before we promote a new owner.
            </p>
          </div>
          <div class="rounded-[var(--radius-menu-shell)] border border-border bg-card p-4">
            <div class="flex items-center gap-2 text-label font-medium">
              <Search class="size-4" />
              <span>Audit</span>
            </div>
            <p class="mt-2 text-caption text-muted-foreground">
              A migration should replace repeated relationships, not chase every `gap-4`.
            </p>
          </div>
        </div>
      </Specimen>
    </div>
  </SectionShell>
</template>
