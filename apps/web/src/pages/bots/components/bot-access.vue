<template>
  <PageShell
    variant="tab"
    :title="$t('bots.access.title')"
    :description="$t('bots.access.subtitle')"
  >
    <Tabs
      v-model="activeTab"
      class="w-full"
    >
      <!-- This is scope navigation (two sub-pages), not a value switch — so it's
           underline Tabs, not a SegmentedControl. pl-1 cancels the trigger's own
           px-1 so the tab TEXT lands on the px-2 content rail (aligned with the
           H1 / card titles); the trigger's padding stays as an over-reaching hit
           target without breaking that visual alignment. -->
      <TabsList class="mb-6 pl-1">
        <TabsTrigger value="channel">
          {{ $t('bots.access.channelTab') }}
        </TabsTrigger>
        <TabsTrigger value="workspace">
          {{ $t('bots.access.workspaceTab') }}
        </TabsTrigger>
      </TabsList>

      <!-- Channel members (IM) -->
      <TabsContent
        value="channel"
        class="space-y-8"
      >
        <SettingsSection>
          <SettingsRow
            :label="$t('bots.access.modeTitle')"
            :description="isBlacklistMode ? $t('bots.access.blacklistModeDescription') : $t('bots.access.whitelistModeDescription')"
          >
            <SegmentedControl
              :model-value="defaultEffectDraft"
              :items="accessModeItems"
              :aria-label="$t('bots.access.modeTitle')"
              class="shrink-0"
              @update:model-value="(value) => handleSetDefaultEffect(String(value))"
            />
          </SettingsRow>
        </SettingsSection>

        <SettingsSection>
          <SettingsRow
            :label="$t('bots.access.members.title')"
            :description="$t('bots.access.members.subtitle')"
          >
            <!-- Inline add-member select, same shape as the model picker in
                 settings: a w-56 select-trigger that opens a search menu. Picking
                 an option adds it immediately; the card layout never swaps. -->
            <div class="w-56 shrink-0">
              <SearchableSelectPopover
                v-model="memberFormIdentityId"
                :options="memberCandidateOptions"
                :placeholder="memberAddLabel"
                :aria-label="memberAddLabel"
                :search-placeholder="$t('bots.access.members.searchIdentity')"
                :empty-text="$t('bots.access.members.noIdentityCandidates')"
                :show-group-headers="true"
              >
                <template #option-label="{ option }">
                  <div class="flex min-w-0 items-center gap-2 py-0.5 text-left">
                    <Avatar class="size-6 shrink-0">
                      <AvatarImage
                        :src="optionMeta(option.meta).avatarUrl || ''"
                        :alt="option.label"
                      />
                      <AvatarFallback class="text-caption">
                        {{ option.label.slice(0, 2).toUpperCase() }}
                      </AvatarFallback>
                    </Avatar>
                    <div class="min-w-0">
                      <div class="truncate text-xs">
                        {{ option.label }}
                      </div>
                      <div
                        v-if="optionMeta(option.meta).channelLabel"
                        class="truncate text-xs text-muted-foreground"
                      >
                        {{ optionMeta(option.meta).channelLabel }}
                      </div>
                    </div>
                  </div>
                </template>
              </SearchableSelectPopover>
            </div>
          </SettingsRow>

          <InlineLoadingRow
            v-if="isPendingRules || isPendingManagers"
            size="md"
            surface="card-row"
          >
            {{ $t('common.loading') }}
          </InlineLoadingRow>

          <Empty
            v-else-if="members.length === 0"
            class="py-12"
          >
            <EmptyHeader>
              <EmptyTitle>{{ $t('bots.access.members.title') }}</EmptyTitle>
              <EmptyDescription>{{ memberEmptyDescription }}</EmptyDescription>
            </EmptyHeader>
          </Empty>

          <template v-else>
            <SettingsRow
              v-for="member in members"
              :key="member.channelIdentityId"
            >
              <template #leading>
                <Avatar class="size-7 shrink-0">
                  <AvatarImage
                    :src="member.avatarUrl || ''"
                    :alt="member.label"
                  />
                  <AvatarFallback class="text-caption">
                    {{ member.label.slice(0, 2).toUpperCase() }}
                  </AvatarFallback>
                </Avatar>
              </template>
              <template #content>
                <div class="flex items-center gap-1.5">
                  <span class="truncate text-sm font-medium text-foreground">
                    {{ member.label }}
                  </span>
                </div>
                <div
                  v-if="member.channelType"
                  class="mt-0.5 flex items-center gap-1 truncate text-xs text-muted-foreground"
                >
                  <ChannelIcon
                    :channel="member.channelType"
                    size="1em"
                  />
                  <span>{{ formatPlatformName(member.channelType) }}</span>
                  <span v-if="member.kind === 'group'"> · {{ $t('bots.access.members.groupBadge') }}</span>
                </div>
              </template>

              <div class="flex items-center gap-3">
                <label class="flex cursor-pointer items-center gap-1.5 text-xs text-foreground">
                  <Checkbox
                    :model-value="member.chat"
                    :disabled="isRowBusy(member)"
                    @update:model-value="(v) => toggleChat(member, v === true)"
                  />
                  {{ $t('bots.access.members.chat') }}
                </label>
                <!-- Groups can't be channel managers (bot_channel_admins is keyed by
                     channel_identity, which groups don't have), so Manage is shown disabled
                     rather than hidden — keeping the Chat/Manage columns aligned across
                     row kinds. -->
                <label
                  class="flex items-center gap-1.5 text-xs"
                  :class="member.kind === 'group'
                    ? 'cursor-not-allowed text-muted-foreground'
                    : 'cursor-pointer text-foreground'"
                >
                  <Checkbox
                    :model-value="member.manage"
                    :disabled="isRowBusy(member) || member.kind === 'group'"
                    @update:model-value="(v) => toggleManage(member, v === true)"
                  />
                  {{ $t('bots.access.members.manage') }}
                </label>

                <!-- Info icon next to Manage: its presence marks a platform member
                 (linked to a workspace account). The popover explains where the
                 Manage permission comes from and, when locally overridden, offers
                 a "reset to inherited" action. Reka Popover is click-triggered so
                 the inner button stays reachable. -->
                <Popover v-if="member.bound || member.manageInherited">
                  <PopoverTrigger as-child>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      class="text-muted-foreground"
                      :title="$t('bots.access.members.platformMember')"
                      :aria-label="$t('bots.access.members.platformMember')"
                    >
                      <Info class="size-3.5" />
                    </Button>
                  </PopoverTrigger>
                  <PopoverContent
                    align="end"
                    class="w-72 space-y-2 text-left"
                  >
                    <div class="flex items-center gap-1.5 text-xs font-medium text-foreground">
                      <Info class="size-3.5 text-muted-foreground" />
                      {{ $t('bots.access.members.platformMember') }}
                    </div>
                    <p class="text-xs leading-relaxed text-muted-foreground">
                      {{ member.manageInherited
                        ? (member.manageHasOverride
                          ? $t('bots.access.members.overrideActive')
                          : $t('bots.access.members.inheritedFollowing'))
                        : $t('bots.access.members.platformMemberHint') }}
                    </p>
                    <Button
                      v-if="member.manageInherited && member.manageHasOverride"
                      variant="outline"
                      size="sm"
                      class="w-full gap-1.5"
                      :disabled="isRowBusy(member)"
                      @click="recoverInherit(member)"
                    >
                      <RotateCcw class="size-3.5" />
                      {{ $t('bots.access.members.recoverInherit') }}
                    </Button>
                  </PopoverContent>
                </Popover>

                <!-- Only local-only visitors can be removed here. Platform members (bound)
                 come from Workspace Members, so they are managed via the checkboxes
                 (untick Manage to suppress) rather than deleted from this list. -->
                <ConfirmPopover
                  v-if="!member.bound && !member.manageInherited"
                  :message="member.kind === 'group'
                    ? $t('bots.access.members.removeGroupConfirm')
                    : $t('bots.access.members.removeConfirm')"
                  :cancel-text="$t('common.cancel')"
                  :confirm-text="$t('common.delete')"
                  @confirm="() => removeMember(member)"
                >
                  <template #trigger>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      class="text-muted-foreground"
                      :disabled="isRowBusy(member)"
                    >
                      <Trash2 class="size-3.5" />
                    </Button>
                  </template>
                </ConfirmPopover>
              </div>
            </SettingsRow>
          </template>
        </SettingsSection>

        <!-- Named entry into advanced rules: an ActionCard opening a focused
             DIALOG — not a push-in sub-page and not an in-card disclosure.
             The structural rule from the design pass: a facet of the current
             page never grows a sub-level (sub-pages are reserved for entities
             like a Provider); a dialog keeps this page visibly underneath, so
             "where am I" never arises and no back-button chrome is needed. -->
        <section class="space-y-2.5">
          <h2 class="px-2 text-label font-medium text-muted-foreground">
            {{ $t('bots.access.rulesTitle') }}
          </h2>
          <ActionCard
            :title="$t('bots.access.advanced.entryTitle')"
            @click="rulesOpen = true"
          >
            <template #icon>
              <ShieldCheck />
            </template>
          </ActionCard>
        </section>

        <!-- Advanced rules dialog: the full list + add/edit form. Behavior is
             unchanged from the old sub-surface — only the container. view-swap
             panel: DialogViewHeader renders the close inline, so DialogPanel
             disables the built-in corner close. -->
        <Dialog v-model:open="rulesOpen">
          <DialogPanel view-swap>
            <!-- Header follows the "centered title needs both flanks
                 balanced" rule (UI Web guidance § dialog header fork), via the
                 shared DialogViewHeader: centered over a populated LIST (the
                 body weight below anchors it); the form view and the bare
                 empty state flush LEFT — the form is a workbench (one focused
                 form, house-default left title; tried centered + back chevron
                 and it read odd), and its way back is the footer Cancel. The
                 built-in corner close is disabled (DialogPanel view-swap) so
                 DialogViewHeader keeps title / close on ONE centerline. The
                 form is a VIEW SWAP, never its own titled card (card-in-card). -->
            <DialogViewHeader :centered="centerDialogTitle">
              {{ formVisible ? (editingRule ? $t('bots.access.editRule') : addListEntryLabel) : $t('bots.access.advanced.entryTitle') }}
            </DialogViewHeader>

            <!-- DialogBody (scroll + scroll-edge fade) stays separate from
                 AutoHeight: AutoHeight's root is overflow-hidden to clip the
                 height tween, so it can't also be the scroller — a form taller
                 than the dialog cap would be clipped with no way to scroll.
                 The grid row caps DialogBody at the dialog's max height and it
                 scrolls; AutoHeight animates the list<->form height change
                 within it. -->
            <DialogBody>
              <AutoHeight>
                <!-- Toolbar only rides ABOVE a populated list: search (left) +
                   add (right), balanced like the models toolbar — spacing alone
                   separates it from the first row, no divider. When the list is
                   empty the guiding action lives INSIDE the Empty block
                   (EmptyContent) instead — a lone button floated over a "No
                   rules" message is the orphaned-action anti-pattern the skill
                   bans; the empty state's own button is the single call to
                   action. Same shape as the other list empty states. -->
                <div
                  v-if="!formVisible && advancedRules.length"
                  class="flex items-center gap-3 pt-1 pb-3"
                >
                  <!-- Search fills the row up to the add button: a capped
                       search left a dead void between the two anchors that
                       read as a layout mistake, not balance. Full-bleed
                       search + trailing action is the arrangement that holds
                       at panel width. -->
                  <InputGroup
                    size="sm"
                    class="min-w-0 flex-1"
                  >
                    <InputGroupAddon align="inline-start">
                      <Search class="size-4 text-muted-foreground" />
                    </InputGroupAddon>
                    <InputGroupInput
                      v-model="ruleSearchQuery"
                      :placeholder="$t('bots.access.searchRules')"
                    />
                  </InputGroup>
                  <Button
                    size="sm"
                    variant="outline"
                    class="shrink-0"
                    @click="openAddDialog"
                  >
                    <Plus class="size-4" />
                    {{ addListEntryLabel }}
                  </Button>
                </div>

                <!-- !formVisible matters: without it the existing rows (and
                     their border-b) leak in above the form — the form view must
                     be the dialog's ONLY body.
                     ui-allow-shape: a list-management dialog row (avatar/channel
                     glyph + target + enabled badge + toggle/edit/delete cluster),
                     not the label↔control SettingsRow — composing that owner here
                     would force a phantom leading column and drop the trailing
                     action group. Borrows the 3.75rem rung for list rhythm. -->
                <template v-if="!formVisible && filteredAdvancedRules.length">
                  <div
                    v-for="rule in filteredAdvancedRules"
                    :key="rule.id"
                    class="flex min-h-[3.75rem] items-center gap-3 border-b border-border py-3 last:border-b-0"
                  >
                    <div
                      v-if="rule.subject_channel_type"
                      class="flex size-8 shrink-0 items-center justify-center text-muted-foreground"
                    >
                      <ChannelIcon
                        :channel="rule.subject_channel_type"
                        size="1em"
                      />
                    </div>
                    <Avatar
                      v-else-if="rule.channel_identity_id"
                      class="size-8 shrink-0"
                    >
                      <AvatarImage
                        :src="rule.channel_identity_avatar_url || ''"
                        :alt="describeRuleTarget(rule)"
                      />
                      <AvatarFallback class="text-caption">
                        {{ ruleTargetFallback(rule) }}
                      </AvatarFallback>
                    </Avatar>
                    <div
                      v-else
                      class="flex size-8 shrink-0 items-center justify-center text-muted-foreground"
                    >
                      <Users class="size-4" />
                    </div>

                    <div class="min-w-0 flex-1">
                      <div class="flex min-w-0 items-center gap-2">
                        <p class="truncate text-sm font-medium text-foreground">
                          {{ describeRuleTarget(rule) }}
                        </p>
                        <Badge
                          :variant="rule.enabled ? 'secondary' : 'outline'"
                          size="sm"
                        >
                          {{ rule.enabled ? $t('bots.access.ruleEnabled') : $t('bots.access.ruleDisabled') }}
                        </Badge>
                      </div>
                      <div class="mt-0.5 flex min-w-0 items-center text-xs text-muted-foreground">
                        <span class="shrink-0">{{ ruleScopePrefix(rule) }}</span>
                        <template v-if="ruleScopeDetail(rule)">
                          <span class="mx-1 shrink-0">: </span>
                          <span class="truncate">{{ ruleScopeDetail(rule) }}</span>
                        </template>
                      </div>
                    </div>

                    <div class="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        class="text-muted-foreground"
                        :aria-label="rule.enabled ? $t('bots.access.disableRule') : $t('bots.access.enableRule')"
                        @click="handleToggleEnabled(rule, !(rule.enabled ?? false))"
                      >
                        <Power class="size-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        class="text-muted-foreground"
                        :aria-label="$t('common.edit')"
                        @click="openEditDialog(rule)"
                      >
                        <SquarePen class="size-3.5" />
                      </Button>
                      <ConfirmPopover
                        :message="$t('bots.access.deleteConfirmDescription')"
                        :cancel-text="$t('common.cancel')"
                        :confirm-text="$t('common.delete')"
                        @confirm="handleDeleteRule(rule.id!)"
                      >
                        <template #trigger>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            class="text-muted-foreground"
                            :aria-label="$t('common.delete')"
                          >
                            <Trash2 class="size-3.5" />
                          </Button>
                        </template>
                      </ConfirmPopover>
                    </div>
                  </div>
                </template>

                <!-- Search active but nothing matches: a quiet line, NOT the
                     "No Rules" empty (rules exist — inviting the user to add
                     one because their search missed would be misleading). -->
                <p
                  v-else-if="!formVisible && advancedRules.length"
                  class="py-12 text-center text-sm text-muted-foreground"
                >
                  {{ $t('bots.access.searchNoMatch') }}
                </p>

                <Empty
                  v-else-if="!formVisible"
                  class="py-12"
                >
                  <EmptyHeader>
                    <EmptyTitle>{{ $t('bots.access.rulesEmpty') }}</EmptyTitle>
                    <EmptyDescription>{{ $t('bots.access.rulesEmptyDescription') }}</EmptyDescription>
                  </EmptyHeader>
                  <EmptyContent>
                    <Button
                      size="sm"
                      variant="outline"
                      @click="openAddDialog"
                    >
                      <Plus class="size-4" />
                      {{ addListEntryLabel }}
                    </Button>
                  </EmptyContent>
                </Empty>

                <!-- Form view: the title lives in the DialogViewHeader and the
                   way back is the footer Cancel, so this
                   section carries NO title bar and NO border of its own — it is
                   the dialog's single view while open, not a bordered sub-card. -->
                <section
                  v-if="formVisible"
                  class="py-1"
                >
                  <form
                    class="space-y-4"
                    @submit.prevent="handleSaveRule(false)"
                  >
                    <div class="grid gap-4 sm:grid-cols-2">
                      <div class="space-y-1.5">
                        <div class="flex items-center justify-between gap-2">
                          <Label class="text-xs font-medium text-muted-foreground">{{ $t('bots.access.platformQuestion') }}</Label>
                          <Button
                            v-if="ruleForm.subjectChannelType"
                            type="button"
                            variant="ghost"
                            size="text"
                            @click="setPlatformScope('')"
                          >
                            {{ $t('bots.access.allPlatforms') }}
                          </Button>
                        </div>
                        <SearchableSelectPopover
                          v-model="ruleForm.subjectChannelType"
                          :options="platformOptions"
                          :placeholder="$t('bots.access.allPlatforms')"
                          :search-placeholder="$t('bots.access.searchPlatform')"
                          :empty-text="$t('bots.access.noPlatformCandidates')"
                          :show-group-headers="false"
                          @update:model-value="setPlatformScope"
                        />
                      </div>

                      <div class="space-y-1.5">
                        <div class="flex items-center justify-between gap-2">
                          <Label class="text-xs font-medium text-muted-foreground">{{ $t('bots.access.userQuestion') }}</Label>
                          <Button
                            v-if="ruleForm.channelIdentityId"
                            type="button"
                            variant="ghost"
                            size="text"
                            @click="setChannelIdentity('')"
                          >
                            {{ $t('bots.access.allUsers') }}
                          </Button>
                        </div>
                        <SearchableSelectPopover
                          v-model="ruleForm.channelIdentityId"
                          :options="filteredIdentityOptions"
                          :placeholder="$t('bots.access.selectIdentity')"
                          :search-placeholder="$t('bots.access.searchIdentity')"
                          :empty-text="$t('bots.access.noIdentityCandidates')"
                          @update:model-value="setChannelIdentity"
                        />
                      </div>
                    </div>

                    <div class="space-y-2">
                      <Label class="text-xs font-medium text-muted-foreground">{{ $t('bots.access.scopeQuestion') }}</Label>
                      <SegmentedControl
                        :model-value="ruleForm.sourceConversationType"
                        :items="chatScopeOptions"
                        :aria-label="$t('bots.access.scopeQuestion')"
                        class="w-full sm:w-fit"
                        @update:model-value="(value) => setChatScope(String(value))"
                      />
                    </div>

                    <div
                      v-if="showSpecificConversationSection"
                      class="space-y-3"
                    >
                      <div class="grid gap-3 sm:grid-cols-2">
                        <div class="space-y-1.5">
                          <Label class="text-xs font-medium text-muted-foreground">{{ $t('bots.access.conversationId') }}</Label>
                          <Input
                            v-model="ruleForm.sourceConversationId"
                            class="h-8"
                            :placeholder="$t('bots.access.conversationIdPlaceholder')"
                          />
                        </div>
                        <div
                          v-if="ruleForm.sourceConversationType === 'thread'"
                          class="space-y-1.5"
                        >
                          <Label class="text-xs font-medium text-muted-foreground">{{ $t('bots.access.threadId') }}</Label>
                          <Input
                            v-model="ruleForm.sourceThreadId"
                            class="h-8"
                            :placeholder="$t('bots.access.threadIdPlaceholder')"
                          />
                        </div>
                      </div>
                    </div>

                    <div class="space-y-1.5">
                      <Label class="text-xs font-medium text-muted-foreground">{{ $t('bots.access.description') }}</Label>
                      <Input
                        v-model="ruleForm.description"
                        class="h-8"
                        :placeholder="$t('bots.access.descriptionPlaceholder')"
                      />
                    </div>

                    <p
                      v-if="formError"
                      class="text-xs text-destructive"
                    >
                      {{ formError }}
                    </p>

                    <div class="flex justify-end gap-2 pt-2">
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        @click="formVisible = false"
                      >
                        {{ $t('common.cancel') }}
                      </Button>
                      <Button
                        type="submit"
                        size="sm"
                        :loading="isSavingRule"
                      >
                        {{ $t('bots.access.saveAndEnable') }}
                      </Button>
                    </div>
                  </form>
                </section>
              </AutoHeight>
            </DialogBody>
          </DialogPanel>
        </Dialog>
      </TabsContent>

      <TabsContent
        value="workspace"
        class="space-y-8"
      >
        <BotUserAccess :bot-id="botId" />
      </TabsContent>
    </Tabs>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  Plus,
  SquarePen,
  Trash2,
  Search,
  Users,
  Power,
  Info,
  RotateCcw,
  ShieldCheck,
} from 'lucide-vue-next'
import {
  ActionCard,
  AutoHeight,
  Button,
  Dialog,
  DialogBody,
  DialogPanel,
  DialogViewHeader,
  Input,
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  Label,
  Avatar,
  AvatarImage,
  AvatarFallback,
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  Badge,
  Checkbox,
  SegmentedControl,
  Tabs,
  TabsList,
  TabsTrigger,
  TabsContent,
  Popover,
  PopoverTrigger,
  PopoverContent,
} from '@felinic/ui'
import ChannelIcon from '@/components/channel-icon/index.vue'
import SearchableSelectPopover from '@/components/searchable-select-popover/index.vue'
import BotUserAccess from './bot-user-access.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { channelTypeDisplayName } from '@/utils/channel-type-label'
import type { AclObservedConversationCandidate, AclRule, AclSourceScope, ChannelaccessManager, HandlersChannelMeta } from '@memohai/sdk'
import {
  getChannels,
  getBotsByBotIdAclRules,
  getBotsByBotIdAclDefaultEffect,
  putBotsByBotIdAclDefaultEffect,
  postBotsByBotIdAclRules,
  putBotsByBotIdAclRulesByRuleId,
  deleteBotsByBotIdAclRulesByRuleId,
  getBotsByBotIdAclChannelIdentities,
  getBotsByBotIdAclChannelTypesByChannelTypeConversations,
  getBotsByBotIdChannelManagers,
  postBotsByBotIdChannelManagers,
  deleteBotsByBotIdChannelManagersByChannelIdentityId,
} from '@memohai/sdk'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

const activeTab = ref<'channel' | 'workspace'>('channel')

const accessModeItems = computed(() => [
  {
    value: 'allow',
    label: t('bots.access.blacklistMode'),
  },
  {
    value: 'deny',
    label: t('bots.access.whitelistMode'),
  },
])

const chatScopeOptions = computed(() => [
  { value: '', label: t('bots.access.chatScopeAny') },
  { value: 'private', label: t('bots.access.privateConversationGroup') },
  { value: 'group', label: t('bots.access.groupConversationGroup') },
  { value: 'thread', label: t('bots.access.threadConversationGroup') },
])

const aclExcludedChannelTypes = new Set(['web'])

interface MemberOptionMeta {
  kind?: 'identity' | 'group'
  avatarUrl?: string
  channelLabel?: string
  conversationId?: string
  channelType?: string
}

function optionMeta(meta: unknown): MemberOptionMeta {
  return (meta ?? {}) as MemberOptionMeta
}

// Stable row key for a group-target member. Encodes channel + conversation so it
// can never collide with a channel_identity UUID, and the same pair always maps
// to the same row across the picker, the members list, and busy/pending sets.
function groupRowKey(channelType: string, conversationId: string): string {
  return `group:${channelType}:${conversationId}`
}

// ---- queries ----

const { data: rulesData, isPending: isPendingRules } = useQuery({
  key: () => ['bot-acl-rules', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdAclRules({ path: { bot_id: props.botId }, throwOnError: true })
    return data
  },
  enabled: () => !!props.botId,
})

const { data: managersData, isPending: isPendingManagers } = useQuery({
  key: () => ['bot-channel-managers', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdChannelManagers({ path: { bot_id: props.botId }, throwOnError: true })
    return data
  },
  enabled: () => !!props.botId,
})

const { data: channelMetas } = useQuery({
  key: () => ['channels'],
  query: async (): Promise<HandlersChannelMeta[]> => {
    const { data } = await getChannels({ throwOnError: true })
    return data ?? []
  },
})

const { data: defaultEffectData } = useQuery({
  key: () => ['bot-acl-default-effect', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdAclDefaultEffect({ path: { bot_id: props.botId }, throwOnError: true })
    return data
  },
  enabled: () => !!props.botId,
})

const { data: identityCandidates } = useQuery({
  key: () => ['bot-acl-identities', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdAclChannelIdentities({
      path: { bot_id: props.botId },
      query: { limit: 100 },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

// ---- derived: mode ----

const defaultEffectDraft = ref('allow')
const isSavingDefaultEffect = ref(false)

watch(defaultEffectData, (data) => {
  if (data?.default_effect) defaultEffectDraft.value = data.default_effect
}, { immediate: true })

const isBlacklistMode = computed(() => defaultEffectDraft.value === 'allow')
const listEntryEffect = computed(() => (isBlacklistMode.value ? 'deny' : 'allow'))

const rules = computed(() => rulesData.value?.items ?? [])
const managers = computed<ChannelaccessManager[]>(() => managersData.value?.items ?? [])

const platformMetaByType = computed(() => {
  const items = new Map<string, HandlersChannelMeta>()
  for (const meta of channelMetas.value ?? []) {
    const type = meta.type?.trim()
    if (type) items.set(type, meta)
  }
  return items
})
const platformOptions = computed(() =>
  [...platformMetaByType.value.values()]
    .map(meta => ({ value: meta.type?.trim() ?? '', label: formatPlatformName(meta.type, meta.display_name) }))
    .filter(option => option.value && !aclExcludedChannelTypes.has(option.value))
    .sort((a, b) => a.label.localeCompare(b.label)),
)

// A pure-identity rule targets a single channel identity with no platform/scope.
function isPureIdentityRule(rule: AclRule): boolean {
  return !!rule.channel_identity_id && !rule.subject_channel_type && !rule.source_scope
}

// A pure-group rule targets "everyone in this group on a platform": no identity,
// subject is the platform (subject_channel_type), scope pins the group conversation.
// subject_channel_type is required by the backend — resolveSourceChannel derives the
// DB-mandatory source_channel from it; without it the source_scope_check constraint
// rejects the rule. The evaluator then matches every message from that group on that
// platform regardless of sender.
function isPureGroupRule(rule: AclRule): boolean {
  return !rule.channel_identity_id
    && !!rule.subject_channel_type
    && !!rule.source_scope?.conversation_type
    && rule.source_scope.conversation_type === 'group'
    && !!rule.source_scope.conversation_id
}

// Identity-scoped chat rules for the current mode (deny in blacklist, allow in whitelist).
const identityChatRules = computed(() =>
  rules.value.filter(r => r.effect === listEntryEffect.value && isPureIdentityRule(r)),
)

// Group-target chat rules for the current mode — rendered as first-class member rows.
const groupRules = computed(() =>
  rules.value.filter(r => r.effect === listEntryEffect.value && isPureGroupRule(r)),
)

// Advanced rules: platform-wide or conversation-scoped combinations that are
// neither a pure-identity nor a pure-group member (e.g. "this person in this group").
const advancedRules = computed(() =>
  rules.value.filter(r => r.effect === listEntryEffect.value && !isPureIdentityRule(r) && !isPureGroupRule(r)),
)

// ---- members aggregation ----

interface MemberRow {
  // For identity rows: the channel_identity UUID. For group rows: the synthetic
  // groupRowKey — used as the v-for key and the busy/pending identity.
  channelIdentityId: string
  kind: 'identity' | 'group'
  label: string
  avatarUrl: string
  channelType: string
  conversationId?: string
  chat: boolean
  chatRuleId?: string
  manage: boolean
  manageInherited: boolean
  manageHasOverride: boolean
  // bound = linked to a workspace member of this bot (a "platform member"),
  // regardless of whether it carries Manage. Drives the info (ⓘ) marker.
  bound: boolean
}

const pendingIds = ref<Set<string>>(new Set())
const busyIds = ref<Set<string>>(new Set())
// Optimistic group row for an in-flight add. Identity adds use pendingIds (a
// placeholder row filled from identityInfoById on refetch); a group carries its
// own meta up front, so we hold the full row here until the persisted rule lands.
const pendingGroupRow = ref<MemberRow | null>(null)

// Local UI overrides applied while a chat/manage mutation is in flight. They are
// cleared by the mutation that created them; keeping cleanup local avoids a
// members -> overrides -> members update loop that can interrupt popovers.
const localOverrides = ref(new Map<string, { chat?: boolean; manage?: boolean }>())

const identityInfoById = computed(() => {
  const map = new Map<string, { label: string, avatarUrl: string, channelType: string }>()
  for (const i of identityCandidates.value?.items ?? []) {
    if (!i.id) continue
    map.set(i.id, {
      label: i.display_name || i.channel_subject_id || i.id,
      avatarUrl: i.avatar_url ?? '',
      channelType: i.channel ?? '',
    })
  }
  return map
})

const members = computed<MemberRow[]>(() => {
  const byId = new Map<string, MemberRow>()
  const ensure = (id: string): MemberRow => {
    let row = byId.get(id)
    if (!row) {
      row = {
        channelIdentityId: id,
        kind: 'identity',
        label: id,
        avatarUrl: '',
        channelType: '',
        chat: isBlacklistMode.value, // default: blacklist allows, whitelist blocks
        manage: false,
        manageInherited: false,
        manageHasOverride: false,
        bound: false,
      }
      byId.set(id, row)
    }
    return row
  }

  // Managers (manage / inherited / override + display info).
  for (const m of managers.value) {
    const id = m.channel_identity_id
    if (!id) continue
    const row = ensure(id)
    row.manage = m.manage ?? false
    row.manageInherited = m.inherited ?? false
    row.manageHasOverride = m.has_override ?? false
    row.bound = m.bound ?? false
    if (m.channel_identity_display_name) row.label = m.channel_identity_display_name
    else if (m.channel_subject_id) row.label = m.channel_subject_id
    if (m.channel_identity_avatar_url) row.avatarUrl = m.channel_identity_avatar_url
    if (m.channel_type) row.channelType = m.channel_type
  }

  // Identity-scoped chat rules for current mode.
  for (const rule of identityChatRules.value) {
    const id = rule.channel_identity_id
    if (!id) continue
    const row = ensure(id)
    row.chatRuleId = rule.id
    // Whitelist: an enabled allow rule means chat ON; disabled means chat OFF.
    // Blacklist: an enabled deny rule means chat OFF; disabled means chat ON.
    const enabled = rule.enabled ?? true
    row.chat = isBlacklistMode.value ? !enabled : enabled
    if (rule.channel_identity_display_name) row.label = rule.channel_identity_display_name
    else if (rule.channel_subject_id) row.label = rule.channel_subject_id
    if (rule.channel_identity_avatar_url) row.avatarUrl = rule.channel_identity_avatar_url
    if (rule.channel_type) row.channelType = rule.channel_type
  }

  // Group-target chat rules for current mode — first-class group member rows.
  for (const rule of groupRules.value) {
    const conversationId = rule.source_scope?.conversation_id
    if (!conversationId) continue
    const ct = rule.subject_channel_type ?? rule.channel_type ?? ''
    const key = groupRowKey(ct, conversationId)
    const row = ensure(key)
    row.kind = 'group'
    row.conversationId = conversationId
    row.channelType = ct
    row.chatRuleId = rule.id
    // Same chat semantics as identity rules: whitelist allow / blacklist deny.
    const enabled = rule.enabled ?? true
    row.chat = isBlacklistMode.value ? !enabled : enabled
    if (rule.source_conversation_name) row.label = rule.source_conversation_name
    else row.label = conversationId
    if (rule.source_conversation_avatar_url) row.avatarUrl = rule.source_conversation_avatar_url
  }

  // Optimistic rows for an in-flight add, until the persisted record is refetched.
  for (const id of pendingIds.value) {
    ensure(id)
  }

  // Optimistic group row for an in-flight add. Only seeds when the persisted
  // rule hasn't landed yet (byId has no entry for this key) — once refetch
  // populates the real row via groupRules above, this stops overriding it.
  if (pendingGroupRow.value && !byId.has(pendingGroupRow.value.channelIdentityId)) {
    const row = ensure(pendingGroupRow.value.channelIdentityId)
    Object.assign(row, pendingGroupRow.value)
  }

  // Fill display info for group rows from the candidate directory where missing.
  const groupInfoByKey = new Map<string, { label: string, avatarUrl: string, channelType: string }>()
  for (const g of groupCandidates.value ?? []) {
    if (!g.conversation_id || !g.channel) continue
    groupInfoByKey.set(groupRowKey(g.channel, g.conversation_id), {
      label: g.conversation_name || g.conversation_id,
      avatarUrl: g.conversation_avatar_url ?? '',
      channelType: g.channel,
    })
  }
  for (const row of byId.values()) {
    if (row.kind !== 'group') continue
    const key = row.channelIdentityId
    const info = groupInfoByKey.get(key)
    if (info) {
      if ((!row.label || row.label === row.conversationId) && info.label) row.label = info.label
      if (!row.avatarUrl) row.avatarUrl = info.avatarUrl
      if (!row.channelType) row.channelType = info.channelType
    }
  }

  // Fill display info from candidate directory where missing.
  for (const row of byId.values()) {
    if (!row.label || row.label === row.channelIdentityId || !row.avatarUrl || !row.channelType) {
      const info = identityInfoById.value.get(row.channelIdentityId)
      if (info) {
        if (!row.label || row.label === row.channelIdentityId) row.label = info.label
        if (!row.avatarUrl) row.avatarUrl = info.avatarUrl
        if (!row.channelType) row.channelType = info.channelType
      }
    }
  }

  // Apply any in-flight optimistic overrides so the UI flips immediately while
  // the mutation is still on the wire.
  for (const row of byId.values()) {
    const override = localOverrides.value.get(row.channelIdentityId)
    if (!override) continue
    if (override.chat !== undefined) row.chat = override.chat
    if (override.manage !== undefined) row.manage = override.manage
  }

  return [...byId.values()].sort((a, b) => a.label.localeCompare(b.label))
})

function isRowBusy(member: MemberRow): boolean {
  return busyIds.value.has(member.channelIdentityId)
}

// ---- member add form ----

const memberFormIdentityId = ref('')

// Group-chat candidates: the backend only exposes observed conversations per
// platform (no "list all groups for this bot" endpoint), so once channel
// metadata is available we prefetch each known platform and merge the observed
// group conversations for the member picker.
const { data: groupCandidates } = useQuery({
  key: () => ['bot-acl-group-candidates', props.botId],
  query: async (): Promise<AclObservedConversationCandidate[]> => {
    const types = platformOptions.value.map(p => p.value).filter(Boolean)
    const results = await Promise.all(types.map(async (channelType) => {
      try {
        const { data } = await getBotsByBotIdAclChannelTypesByChannelTypeConversations({
          path: { bot_id: props.botId, channel_type: channelType },
          throwOnError: true,
        })
        return (data?.items ?? []).map(item => ({ ...item, channel: item.channel || channelType }))
      }
      catch {
        return []
      }
    }))
    const seen = new Set<string>()
    const merged: AclObservedConversationCandidate[] = []
    for (const item of results.flat()) {
      // Only group conversations — the endpoint returns every observed route on the
      // platform (DMs and threads too); a non-group conversation_id paired with a
      // 'group' source_scope would never match inbound traffic (dead rule).
      if (!item.conversation_id || !item.channel) continue
      if (item.conversation_type !== 'group') continue
      const dedupe = `${item.channel}:${item.conversation_id}`
      if (seen.has(dedupe)) continue
      seen.add(dedupe)
      merged.push(item)
    }
    return merged
  },
  enabled: () => !!props.botId && (channelMetas.value?.length ?? 0) > 0,
})

// Watches need to live below the members computed and its dependencies
// (groupCandidates, identityInfoById, etc.) so the first eager evaluation
// never hits an uninitialized variable.
watch(isBlacklistMode, () => {
  localOverrides.value = new Map()
})

function setLocalOverride(key: string, patch: { chat?: boolean; manage?: boolean }) {
  localOverrides.value = new Map(localOverrides.value).set(key, {
    ...localOverrides.value.get(key),
    ...patch,
  })
}

function clearLocalOverride(key: string, field?: 'chat' | 'manage') {
  const next = new Map(localOverrides.value)
  if (!field) {
    next.delete(key)
  }
  else {
    const current = next.get(key)
    if (current) {
      const updated = { ...current }
      delete updated[field]
      if (updated.chat === undefined && updated.manage === undefined) next.delete(key)
      else next.set(key, updated)
    }
  }
  localOverrides.value = next
}

const memberAddLabel = computed(() =>
  isBlacklistMode.value ? t('bots.access.members.addBlocked') : t('bots.access.members.add'),
)
const memberEmptyDescription = computed(() =>
  isBlacklistMode.value ? t('bots.access.members.emptyBlacklist') : t('bots.access.members.emptyWhitelist'),
)
const memberAddedMessage = computed(() =>
  isBlacklistMode.value ? t('bots.access.members.blocked') : t('bots.access.members.added'),
)
const memberAddFailedMessage = computed(() =>
  isBlacklistMode.value ? t('bots.access.members.blockFailed') : t('bots.access.members.addFailed'),
)

interface MemberCandidateOption {
  value: string
  label: string
  description?: string
  group: string
  groupLabel: string
  keywords: string[]
  meta: MemberOptionMeta
}

const memberCandidateOptions = computed<MemberCandidateOption[]>(() => {
  const present = new Set(members.value.map(m => m.channelIdentityId))
  const identityOptions = (identityCandidates.value?.items ?? [])
    .filter(i => i.id && !present.has(i.id) && !aclExcludedChannelTypes.has(i.channel ?? ''))
    .map(i => ({
      value: i.id ?? '',
      label: i.display_name || i.channel_subject_id || i.id || '',
      group: 'identities',
      groupLabel: t('bots.access.members.identityGroupLabel'),
      keywords: [i.display_name ?? '', i.channel_subject_id ?? '', i.channel ?? ''],
      meta: { kind: 'identity', avatarUrl: i.avatar_url, channelLabel: channelTypeDisplayName(t, i.channel ?? '') },
    }))
  const groupOptions = (groupCandidates.value ?? [])
    .filter(g => g.conversation_id && g.channel && !aclExcludedChannelTypes.has(g.channel))
    .flatMap((g) => {
      const key = groupRowKey(g.channel ?? '', g.conversation_id ?? '')
      if (present.has(key)) return []
      return [{
        value: key,
        label: g.conversation_name || g.conversation_id || '',
        group: 'groups',
        groupLabel: t('bots.access.groupConversationGroup'),
        keywords: [g.conversation_name ?? '', g.conversation_id ?? '', g.channel ?? ''],
        meta: {
          kind: 'group',
          avatarUrl: g.conversation_avatar_url,
          channelLabel: channelTypeDisplayName(t, g.channel ?? ''),
          conversationId: g.conversation_id,
          channelType: g.channel,
        },
      }]
    })
  return [...identityOptions, ...groupOptions]
})

// Add persists immediately (no in-memory-only draft): adding a member writes the
// list entry for the active mode. Whitelist entries allow chat; blacklist entries
// deny chat. Manage is only written by the Manage checkbox so list membership
// cannot accidentally suppress inherited workspace permissions.
async function confirmAddMember() {
  const value = memberFormIdentityId.value.trim()
  if (!value) return
  const isGroup = value.startsWith('group:')
  // Capture the selected option's meta before resetting the picker: the select
  // is a single v-model, so read the option while it's still selected, then
  // clear it in finally to return the trigger to its placeholder.
  const selectedOption = isGroup
    ? memberCandidateOptions.value.find(o => o.value === value)
    : undefined
  const groupMeta = isGroup ? optionMeta(selectedOption?.meta) : undefined
  busyIds.value.add(value)
  if (isGroup) {
    // Optimistic group row: we already have the full meta (name/avatar/channel/
    // conversationId), so show the row immediately in a busy state instead of
    // waiting for the refetch to surface it.
    pendingGroupRow.value = {
      channelIdentityId: value,
      kind: 'group',
      label: selectedOption?.label || groupMeta?.conversationId || value,
      avatarUrl: groupMeta?.avatarUrl ?? '',
      channelType: groupMeta?.channelType ?? '',
      conversationId: groupMeta?.conversationId,
      chat: !isBlacklistMode.value,
      manage: false,
      manageInherited: false,
      manageHasOverride: false,
      bound: false,
    }
  }
  else {
    pendingIds.value.add(value)
  }
  try {
    if (isGroup) {
      const conversationId = groupMeta?.conversationId ?? ''
      const channelType = groupMeta?.channelType ?? ''
      if (!conversationId || !channelType) throw new Error('missing conversation id or channel')
      await createGroupRule(channelType, conversationId, isBlacklistMode.value ? 'deny' : 'allow')
    }
    else if (isBlacklistMode.value) {
      await createIdentityRule(value, 'deny')
    }
    else {
      await createIdentityRule(value, 'allow')
    }
    await Promise.all([invalidateRules(), invalidateManagers()])
    toast.success(memberAddedMessage.value)
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, memberAddFailedMessage.value))
  }
  finally {
    if (isGroup) pendingGroupRow.value = null
    else pendingIds.value.delete(value)
    busyIds.value.delete(value)
    memberFormIdentityId.value = ''
  }
}

// The inline select commits on pick: selecting an option fires confirmAddMember
// immediately (no separate Confirm button). The model is reset in confirmAddMember's
// finally so the trigger returns to its placeholder, ready for the next add.
watch(memberFormIdentityId, (value) => {
  if (value) confirmAddMember()
})

// ---- chat / manage toggles ----

async function createIdentityRule(channelIdentityId: string, effect: string) {
  await postBotsByBotIdAclRules({
    path: { bot_id: props.botId },
    body: { enabled: true, effect, channel_identity_id: channelIdentityId },
    throwOnError: true,
  })
}

// "Everyone in this group on this platform" — subject is the platform
// (subject_channel_type) and scope pins the group conversation. subject_channel_type
// is mandatory: the backend's resolveSourceChannel derives the DB-required source_channel
// from it (without it, the source_scope_check constraint rejects the rule). The
// platform pin is redundant for matching — conversation_id is already platform-specific —
// but the constraint demands it.
async function createGroupRule(channelType: string, conversationId: string, effect: string) {
  await postBotsByBotIdAclRules({
    path: { bot_id: props.botId },
    body: {
      enabled: true,
      effect,
      subject_channel_type: channelType,
      source_scope: { conversation_type: 'group', conversation_id: conversationId },
    },
    throwOnError: true,
  })
}

async function deleteRule(ruleId: string) {
  await deleteBotsByBotIdAclRulesByRuleId({ path: { bot_id: props.botId, rule_id: ruleId }, throwOnError: true })
}

function invalidateRules() {
  return queryCache.invalidateQueries({ key: ['bot-acl-rules', props.botId] })
}

function invalidateManagers() {
  return queryCache.invalidateQueries({ key: ['bot-channel-managers', props.botId] })
}

async function toggleChat(member: MemberRow, next: boolean) {
  const key = member.channelIdentityId
  setLocalOverride(key, { chat: next })
  busyIds.value.add(key)
  try {
    await applyChatToggle(member, next)
    await invalidateRules()
    clearLocalOverride(key, 'chat')
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, t('bots.access.members.updateFailed')))
    clearLocalOverride(key, 'chat')
  }
  finally {
    busyIds.value.delete(key)
  }
}

async function applyChatToggle(member: MemberRow, next: boolean) {
  // The "natural" enabled state for the active mode: whitelist wants allow rules
  // enabled; blacklist wants deny rules enabled. Chat ON always matches that
  // state, chat OFF is the opposite.
  const desiredEnabled = !isBlacklistMode.value
  const effect = isBlacklistMode.value ? 'deny' : 'allow'
  if (member.kind === 'group') {
    const conversationId = member.conversationId ?? ''
    const channelType = member.channelType ?? ''
    if (!conversationId || !channelType) throw new Error('missing group info')
    if (next === desiredEnabled) {
      if (member.chatRuleId) {
        await putBotsByBotIdAclRulesByRuleId({
          path: { bot_id: props.botId, rule_id: member.chatRuleId },
          body: {
            enabled: true,
            effect,
            subject_channel_type: channelType,
            source_scope: { conversation_type: 'group', conversation_id: conversationId },
          },
          throwOnError: true,
        })
      }
      else {
        await createGroupRule(channelType, conversationId, effect)
      }
    }
    else if (member.chatRuleId) {
      await putBotsByBotIdAclRulesByRuleId({
        path: { bot_id: props.botId, rule_id: member.chatRuleId },
        body: {
          enabled: false,
          effect,
          subject_channel_type: channelType,
          source_scope: { conversation_type: 'group', conversation_id: conversationId },
        },
        throwOnError: true,
      })
    }
  }
  else if (next === desiredEnabled) {
    if (member.chatRuleId) {
      await putBotsByBotIdAclRulesByRuleId({
        path: { bot_id: props.botId, rule_id: member.chatRuleId },
        body: {
          enabled: true,
          effect,
          channel_identity_id: member.channelIdentityId,
        },
        throwOnError: true,
      })
    }
    else {
      await createIdentityRule(member.channelIdentityId, effect)
    }
  }
  else if (member.chatRuleId) {
    await putBotsByBotIdAclRulesByRuleId({
      path: { bot_id: props.botId, rule_id: member.chatRuleId },
      body: {
        enabled: false,
        effect,
        channel_identity_id: member.channelIdentityId,
      },
      throwOnError: true,
    })
  }
}

async function toggleManage(member: MemberRow, next: boolean) {
  // Groups can't be channel managers (bot_channel_admins is keyed by
  // channel_identity). The checkbox is disabled for group rows, so this is a
  // defensive guard — never expected to fire.
  if (member.kind === 'group') return
  const key = member.channelIdentityId
  setLocalOverride(key, { manage: next })
  busyIds.value.add(key)
  try {
    // Toggling Manage always writes an explicit local override (granted = next);
    // it never deletes the membership. This keeps the row stable (final state =
    // local override ?? inherited). To stop overriding an inherited member, use
    // "Reset to inherited" in the info popover; to remove a local member entirely,
    // use the Trash action.
    await postBotsByBotIdChannelManagers({
      path: { bot_id: props.botId },
      body: { channel_identity_id: member.channelIdentityId, granted: next },
      throwOnError: true,
    })
    await invalidateManagers()
    clearLocalOverride(key, 'manage')
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, t('bots.access.members.updateFailed')))
    clearLocalOverride(key, 'manage')
  }
  finally {
    busyIds.value.delete(key)
  }
}

async function recoverInherit(member: MemberRow) {
  busyIds.value.add(member.channelIdentityId)
  try {
    await deleteBotsByBotIdChannelManagersByChannelIdentityId({
      path: { bot_id: props.botId, channel_identity_id: member.channelIdentityId },
      throwOnError: true,
    })
    await invalidateManagers()
    toast.success(t('bots.access.members.updated'))
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, t('bots.access.members.updateFailed')))
  }
  finally {
    busyIds.value.delete(member.channelIdentityId)
  }
}

async function removeMember(member: MemberRow) {
  const key = member.channelIdentityId
  busyIds.value.add(key)
  try {
    if (member.chatRuleId) await deleteRule(member.chatRuleId)
    if (member.manageHasOverride || (member.manage && !member.manageInherited)) {
      await deleteBotsByBotIdChannelManagersByChannelIdentityId({
        path: { bot_id: props.botId, channel_identity_id: key },
        throwOnError: true,
      }).catch(() => undefined)
    }
    pendingIds.value.delete(key)
    await Promise.all([invalidateRules(), invalidateManagers()])
    toast.success(t('bots.access.members.removed'))
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, t('bots.access.members.removeFailed')))
  }
  finally {
    busyIds.value.delete(key)
    clearLocalOverride(key)
  }
}

// ---- default effect ----

async function handleSetDefaultEffect(effect: string) {
  const previousEffect = defaultEffectDraft.value
  if (effect === previousEffect || isSavingDefaultEffect.value) return
  defaultEffectDraft.value = effect
  isSavingDefaultEffect.value = true
  try {
    await putBotsByBotIdAclDefaultEffect({ path: { bot_id: props.botId }, body: { default_effect: effect }, throwOnError: true })
    queryCache.invalidateQueries({ key: ['bot-acl-default-effect', props.botId] })
  }
  catch (e) {
    defaultEffectDraft.value = previousEffect
    toast.error(resolveApiErrorMessage(e, t('bots.access.saveFailed')))
  }
  finally {
    isSavingDefaultEffect.value = false
  }
}

// ---- advanced rules ----

// Advanced rules dialog visibility. The rule list + form share this
// component's script; opening/closing is pure visibility.
const rulesOpen = ref(false)
// Reopening the dialog starts fresh: drop any leftover search filter and a
// half-open form so the user always lands on the full list.
watch(rulesOpen, (open) => {
  if (open) return
  ruleSearchQuery.value = ''
  formVisible.value = false
})

interface RuleForm {
  effect: string
  subjectChannelType: string
  channelIdentityId: string
  sourceConversationType: string
  sourceConversationId: string
  sourceThreadId: string
  description: string
}

function createRuleForm(effect = 'deny'): RuleForm {
  return {
    effect,
    subjectChannelType: '',
    channelIdentityId: '',
    sourceConversationType: '',
    sourceConversationId: '',
    sourceThreadId: '',
    description: '',
  }
}

const ruleForm = reactive(createRuleForm())
const formVisible = ref(false)
const editingRule = ref<AclRule | null>(null)
const formError = ref('')
const savingRuleAction = ref(false)
const isSavingRule = computed(() => savingRuleAction.value)

// Toolbar search over the advanced-rule list. Matches the human-readable
// projections (target / scope / description) — the strings the user actually
// sees in the rows — rather than raw rule fields.
const ruleSearchQuery = ref('')
const filteredAdvancedRules = computed(() => {
  const query = ruleSearchQuery.value.trim().toLowerCase()
  if (!query) return advancedRules.value
  return advancedRules.value.filter(rule =>
    [describeRuleTarget(rule), ruleScopePrefix(rule), ruleScopeDetail(rule), rule.description ?? '']
      .some(text => text.toLowerCase().includes(query)),
  )
})

// A centered dialog title needs something on BOTH flanks to balance against:
// the back chevron in form view, or the list body's own weight below. The bare
// empty state has neither (no chevron, only a centered "No rules" block), so a
// centered title would float untethered — it drops to the workbench default
// (title left, close right). See the UI Web guidance § dialog header fork.
// Centered ONLY over a populated list (its body weight anchors the title;
// human-reviewed as the golden list form). The form view is a workbench —
// left title, no back chevron (tried centered+chevron, read odd; footer
// Cancel is the way back) — and the bare empty state has nothing to anchor
// a centered title either. An all-states-left variant and a table-layout
// list were both tried and rejected on sight.
const centerDialogTitle = computed(() => !formVisible.value && advancedRules.value.length > 0)

const identityOptions = computed(() =>
  (identityCandidates.value?.items ?? [])
    .filter(i => !aclExcludedChannelTypes.has(i.channel ?? ''))
    .map(i => ({
      value: i.id ?? '',
      label: i.display_name || i.channel_subject_id || i.id || '',
      meta: { avatarUrl: i.avatar_url, channel: i.channel, channelLabel: formatPlatformName(i.channel) },
    })),
)

const filteredIdentityOptions = computed(() => {
  const platform = ruleForm.subjectChannelType.trim()
  if (!platform) return identityOptions.value
  return identityOptions.value.filter(option => option.meta.channel === platform)
})

const addListEntryLabel = computed(() =>
  isBlacklistMode.value ? t('bots.access.addBlacklistEntry') : t('bots.access.addWhitelistEntry'),
)
const showSpecificConversationSection = computed(() =>
  ruleForm.sourceConversationType === 'group'
  || ruleForm.sourceConversationType === 'thread'
  || !!ruleForm.sourceConversationId
  || !!ruleForm.sourceThreadId,
)
watch(listEntryEffect, (effect) => {
  if (formVisible.value && !editingRule.value) ruleForm.effect = effect
})

function openAddDialog() {
  editingRule.value = null
  Object.assign(ruleForm, createRuleForm(listEntryEffect.value))
  formError.value = ''
  formVisible.value = true
}

function openEditDialog(rule: AclRule) {
  editingRule.value = rule
  ruleForm.effect = rule.effect ?? 'deny'
  ruleForm.subjectChannelType = rule.subject_channel_type ?? ''
  ruleForm.channelIdentityId = rule.channel_identity_id ?? ''
  ruleForm.sourceConversationType = rule.source_scope?.conversation_type ?? ''
  ruleForm.sourceConversationId = rule.source_scope?.conversation_id ?? ''
  ruleForm.sourceThreadId = rule.source_scope?.thread_id ?? ''
  ruleForm.description = rule.description ?? ''
  formError.value = ''
  formVisible.value = true
}

function setChatScope(scope: string) {
  if (scope === '' || scope === 'private' || scope !== ruleForm.sourceConversationType) {
    ruleForm.sourceConversationId = ''
    ruleForm.sourceThreadId = ''
  }
  ruleForm.sourceConversationType = scope
}

function setPlatformScope(channelType: string) {
  ruleForm.subjectChannelType = channelType
}

function setChannelIdentity(identityId: string) {
  ruleForm.channelIdentityId = identityId
}

function buildSourceScope(): AclSourceScope | undefined {
  const scope: AclSourceScope = {}
  if (ruleForm.sourceConversationType) scope.conversation_type = ruleForm.sourceConversationType
  if (ruleForm.sourceConversationId) scope.conversation_id = ruleForm.sourceConversationId
  if (ruleForm.sourceThreadId) scope.thread_id = ruleForm.sourceThreadId
  if (!scope.conversation_type && !scope.conversation_id && !scope.thread_id) return undefined
  return scope
}

async function handleSaveRule(_enable: boolean) {
  formError.value = ''
  savingRuleAction.value = true
  try {
    const body = {
      enabled: true,
      effect: ruleForm.effect,
      channel_identity_id: ruleForm.channelIdentityId || undefined,
      subject_channel_type: ruleForm.subjectChannelType || undefined,
      source_scope: buildSourceScope(),
      description: ruleForm.description || undefined,
    }
    if (editingRule.value?.id) {
      await putBotsByBotIdAclRulesByRuleId({ path: { bot_id: props.botId, rule_id: editingRule.value.id }, body, throwOnError: true })
    }
    else {
      await postBotsByBotIdAclRules({ path: { bot_id: props.botId }, body, throwOnError: true })
    }
    await invalidateRules()
    toast.success(t('bots.access.ruleSaved'))
    formVisible.value = false
  }
  catch (e) {
    formError.value = resolveApiErrorMessage(e, t('bots.access.saveFailed'))
  }
  finally {
    savingRuleAction.value = false
  }
}

async function handleDeleteRule(ruleId: string) {
  try {
    await deleteRule(ruleId)
    await invalidateRules()
    toast.success(t('bots.access.deleteSuccess'))
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, t('bots.access.deleteFailed')))
  }
}

async function handleToggleEnabled(rule: AclRule, enabled: boolean) {
  try {
    await putBotsByBotIdAclRulesByRuleId({
      path: { bot_id: props.botId, rule_id: rule.id! },
      body: {
        enabled,
        effect: rule.effect ?? 'deny',
        channel_identity_id: rule.channel_identity_id,
        subject_channel_type: rule.subject_channel_type,
        source_scope: rule.source_scope,
        description: rule.description,
      },
      throwOnError: true,
    })
    await invalidateRules()
  }
  catch (e) {
    toast.error(resolveApiErrorMessage(e, t('bots.access.saveFailed')))
  }
}

// ---- display helpers ----

function describeRuleTarget(rule: AclRule): string {
  const platformType = rule.subject_channel_type || rule.channel_type
  const platform = platformType ? formatPlatformName(platformType) : ''
  const user = rule.channel_identity_display_name || rule.channel_subject_id || rule.channel_identity_id || ''
  if (rule.subject_channel_type && rule.channel_identity_id) return t('bots.access.platformUserTargetPreview', { platform, user: user || '?' })
  if (rule.subject_channel_type) return t('bots.access.platformTargetPreview', { platform })
  if (rule.channel_identity_id) return t('bots.access.userTargetPreview', { user: user || '?' })
  return t('bots.access.subjectAllLabel')
}

function formatPlatformName(type?: string | null, displayName?: string | null): string {
  const raw = type?.trim() ?? ''
  const meta = raw ? platformMetaByType.value.get(raw) : undefined
  return channelTypeDisplayName(t, raw, displayName ?? meta?.display_name)
}

function ruleTargetFallback(rule: AclRule): string {
  const label = describeRuleTarget(rule).trim()
  return label ? label.slice(0, 2).toUpperCase() : '?'
}

function ruleScopePrefix(rule: AclRule): string {
  const scope = rule.source_scope
  if (!scope?.conversation_type) return t('bots.access.chatScopeAny')
  switch (scope.conversation_type) {
    case 'private': return t('bots.access.privateConversationGroup')
    case 'group': return t('bots.access.groupConversationGroup')
    case 'thread': return t('bots.access.threadConversationGroup')
    default: return scope.conversation_type
  }
}

function ruleScopeDetail(rule: AclRule): string {
  const scope = rule.source_scope
  const conversationID = scope?.conversation_id?.trim()
  if (!conversationID) return ''
  const name = rule.source_conversation_name?.trim()
  const displayName = name ? `${name} (${conversationID})` : conversationID
  const thread = scope?.thread_id ? ` · thread:${scope.thread_id}` : ''
  return `${displayName}${thread}`
}
</script>
