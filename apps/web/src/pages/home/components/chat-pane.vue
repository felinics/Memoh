<template>
  <div
    ref="rootEl"
    class="flex-1 flex flex-col h-full min-w-0 relative"
    v-on="dropHandlers"
  >
    <div
      v-if="!currentBotId"
      class="flex-1"
    >
      <PanePlaceholder :title="$t('chat.selectBot')">
        {{ $t('chat.selectBotHint') }}
      </PanePlaceholder>
    </div>

    <template v-else>
      <section class="flex-1 relative w-full px-3 sm:px-5 lg:px-8">
        <section class="absolute inset-0">
          <div
            aria-hidden="true"
            class="pointer-events-none absolute inset-x-0 top-0 z-(--z-raised) h-10 bg-gradient-to-b from-surface-editor to-transparent"
          />
          <ScrollArea
            ref="scrollContainer"
            class="h-full"
          >
            <!-- Same horizontal rhythm as the composer below (px-4 sm:px-6
                 lg:px-10) so the input box and the message column share one
                 width at every pane size — they must never diverge. The
                 bottom padding tracks the dock's measured height (composer,
                 ask_user capsule, approval panel) instead of a fixed rung, so
                 the last message can always scroll clear of it. -->
            <div
              class="w-full max-w-[840px] mx-auto px-4 pt-6 space-y-6 sm:px-6 lg:px-10"
              :style="{ paddingBottom: messagesBottomPad }"
            >
              <div
                ref="loadMoreSentinel"
                aria-hidden="true"
                class="h-px w-full"
              />
              <div
                v-if="loadingOlder"
                class="flex justify-center py-2"
              >
                <Spinner class="size-3.5" />
              </div>

              <!-- A session with a live run but no messages yet (a subagent
                   that has not produced output) reads as starting, not empty. -->
              <div
                v-if="messages.length === 0 && !loadingChats && !loadingMessages && streaming"
                class="flex items-center justify-center min-h-75"
              >
                <Spinner class="size-3.5" />
              </div>

              <!-- Read-only sessions (system / synced channel threads) can't
                   take new input, so an empty one states why it has nothing.
                   A fresh, writable chat instead gets the centered welcome
                   composer below, never a stray line in a blank pane. -->
              <div
                v-else-if="messages.length === 0 && !loadingChats && !loadingMessages && activeChatReadOnly"
                class="flex items-center justify-center min-h-75"
              >
                <p class="text-muted-foreground text-xs">
                  {{ $t('chat.emptySystemSession') }}
                </p>
              </div>

              <!-- One persistent container per turn, keyed by the turn's
                   opening message id — a send APPENDS a container; previous
                   turns' DOM is never re-parented (see messageTurns for why
                   that is load-bearing). The send pin reserves viewport space
                   by setting an inline min-height on the LAST turn's container
                   (see useChatScroll's Pin section). -->
              <div
                v-for="(turn, turnIndex) in messageTurns"
                :key="turn.id"
                :ref="turnIndex === messageTurns.length - 1 ? setLastTurnEl : undefined"
                :style="turnReserveStyle(turn.id)"
                class="space-y-6"
              >
                <template
                  v-for="(msg, msgIndex) in turn.messages"
                  :key="msg.id"
                >
                  <ForkSourceDivider
                    v-if="showForkSourceDividerBefore(turn.start + msgIndex)"
                    :title="forkSourceTitle"
                    :disabled="openingForkSource"
                    @open-source="handleForkSourceClick"
                  />

                  <div
                    :data-message-id="msg.id"
                    :data-external-message-id="(msg.role === 'user' || msg.role === 'assistant') ? msg.externalMessageId : undefined"
                    class="transition-[background-color] duration-500 scroll-mt-2 px-2 -mx-2"
                    :class="highlightedMessageId === msg.id ? 'bg-muted/45' : ''"
                    :data-anchor="msg.id"
                  >
                    <MessageItem
                      :message="msg"
                      :bot-id="currentBotId"
                      :channel-thread="isChannelThread"
                      :channel-platform="channelPlatform"
                      :bot-name="currentBot?.name"
                      :bot-avatar-url="currentBot?.avatar_url"
                      :on-open-media="galleryOpenBySrc"
                      :on-reply-click="handleReplyJump"
                      :on-retry-message="handleRetryMessage"
                      :can-retry-latest-assistant="isRetryableTurn(msg)"
                      :can-edit-latest-user="isEditableTurn(msg)"
                      :can-fork-assistant="canForkAssistant"
                      :is-scrolling="isScrolling"
                      :is-last-message="msg.id === lastMessageId"
                      @active="onMessageActive"
                      @edit-message="handleEditMessage"
                      @fork-message="handleForkMessage"
                    />
                  </div>

                  <ForkSourceDivider
                    v-if="showForkSourceDividerAfter(msg, turn.start + msgIndex)"
                    :title="forkSourceTitle"
                    :disabled="openingForkSource"
                    @open-source="handleForkSourceClick"
                  />
                </template>
              </div>
            </div>
          </ScrollArea>

          <ChatScrollRail
            :messages="messages"
            :scroll-el="scrollEl"
            :enabled="isVisible && !loadingChats"
            @jump="handleRailJump"
          />
        </section>
      </section>

      <MediaGalleryLightbox
        :items="galleryItems"
        :open-index="galleryOpenIndex"
        @update:open-index="gallerySetOpenIndex"
      />

      <MediaGalleryLightbox
        :items="composerPreviewItems"
        :open-index="composerPreviewIndex"
        appearance="frost"
        @update:open-index="composerPreviewIndex = $event"
      />

      <Dialog v-model:open="pastedViewerOpen">
        <DialogContent class="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{{ $t('chat.pastedViewerTitle') }}</DialogTitle>
          </DialogHeader>
          <pre class="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border bg-surface-composer p-3 text-caption leading-relaxed text-foreground">{{ pastedViewerText }}</pre>
        </DialogContent>
      </Dialog>

      <ChatForkDialog
        v-model:open="forkDialogOpen"
        :turn-id="pendingForkTurnId"
      />

      <!-- The composer is a single instance reused in both layouts: pinned to
           the bottom once a conversation exists, or lifted to the vertical
           centre (with a greeting above it) while the chat is still empty, so a
           fresh chat opens on an inviting page instead of a near-blank pane. -->
      <!-- No outer horizontal gutter here on purpose: the message list lives in a
           `section.absolute.inset-0` layer that fills its parent's padding box, so
           it bypasses the section's px-3/sm:px-5/lg:px-8 gutter and only carries the
           inner px-4/sm:px-6/lg:px-10. The composer must drop the same outer gutter
           so its inner padding is the ONLY horizontal inset — matching the message
           column edge-for-edge at every width. -->
      <div
        v-if="!activeChatReadOnly"
        class="pointer-events-none absolute z-(--z-panel)"
        :class="[
          isWelcome
            ? 'inset-0 flex flex-col items-center justify-start pt-[28dvh]'
            : 'inset-x-0 bottom-0 pt-2 pb-7',
          { invisible: composerPlacementPending },
        ]"
        :style="composerLiftPx > 0 ? { bottom: `${composerLiftPx}px` } : undefined"
      >
        <!-- Opaque backdrop, bottom-anchored, rising only to the box's widest point
             (its vertical centre). The box is solid and sits above the messages, so
             above that line its rounded top simply floats over whatever is there —
             that area is left unmasked on purpose. From the widest point down (where
             the box curves back in and would leave gaps by its bottom corners, plus
             the strip beneath it) this fill hides everything, so nothing bleeds out
             below the box. No fade: the top edge meets the box where it is already
             full width, so the seam is hidden behind the box itself. -->
        <div
          v-if="!isWelcome"
          aria-hidden="true"
          class="absolute inset-x-0 bottom-0 bg-surface-editor"
          :style="{ height: dockMaskHeight }"
        />
        <!-- welcome: top-anchored column — the greeting and the composer's top
             edge stay pinned at the shared viewport anchor, so a growing composer
             (text or attachments) only extends downward and never pushes the
             greeting up; normal: display:contents removes this from layout. -->
        <div :class="isWelcome ? 'flex w-full flex-col items-center gap-8 md:-translate-x-3' : 'contents'">
          <div
            v-if="isWelcome"
            class="mx-auto w-full max-w-[44rem] px-4 text-left sm:px-6 lg:px-10"
          >
            <h1
              data-welcome-heading
              class="px-4 text-balance text-foreground"
            >
              {{ welcomeGreeting }}
            </h1>
          </div>
          <!-- A fresh chat uses a focused content measure; once conversation starts,
               the composer expands to the message column. Both keep the same responsive
               gutters (px-4 sm:px-6 lg:px-10), so their internal alignment does not
               change — the inner gutter still relaxes on a cramped pane, but both
               edges move together. The -translate-x-0.5 is a 2px optical nudge:
               the column math is centered, but the eye reads the composer a hair
               right of the message column (the scroll rail eats the right edge),
               so the whole dock unit shifts left a touch to sit where it looks
               centered. Desktop only — mobile has no scroll rail, so there the
               nudge would just push the composer off centre. -->
          <div
            class="pointer-events-auto relative mx-auto w-full px-4 sm:px-6 lg:px-10 md:-translate-x-0.5"
            :class="isWelcome ? 'max-w-[44rem]' : 'max-w-[840px]'"
          >
            <Transition
              enter-active-class="motion-safe:transition-opacity motion-safe:duration-150 ease-out"
              enter-from-class="motion-safe:opacity-0"
              enter-to-class="opacity-100"
              leave-active-class="motion-safe:transition-opacity motion-safe:duration-150 ease-in"
              leave-from-class="opacity-100"
              leave-to-class="motion-safe:opacity-0"
            >
              <BgTaskPill
                v-if="bgTaskPill"
                :pill="bgTaskPill"
                class="absolute left-0 bottom-full z-(--z-sticky) mb-2 max-w-[calc(50%-2rem)]"
                @jump="scrollToOffscreen"
              />
            </Transition>

            <Transition
              enter-active-class="transition-opacity duration-150 ease-out"
              enter-from-class="opacity-0"
              enter-to-class="opacity-100"
              leave-active-class="transition-opacity duration-150 ease-in"
              leave-from-class="opacity-100"
              leave-to-class="opacity-0"
            >
              <Button
                v-if="showJumpToBottom"
                type="button"
                size="icon"
                variant="ghost"
                class="absolute left-1/2 bottom-full z-(--z-sticky) mb-4 size-9 -translate-x-1/2 rounded-full border border-border bg-card text-foreground"
                aria-label="Scroll to latest message"
                @click="scrollToBottom"
              >
                <ArrowDown class="size-4" />
              </Button>
            </Transition>

            <input
              ref="fileInput"
              type="file"
              multiple
              class="hidden"
              @change="handleFileInputChange"
            >
            <Transition
              enter-active-class="transition-opacity duration-150 ease-out"
              enter-from-class="opacity-0"
              enter-to-class="opacity-100"
              leave-active-class="transition-opacity duration-100 ease-in"
              leave-from-class="opacity-100"
              leave-to-class="opacity-0"
            >
              <Command
                v-if="slashPanelOpen"
                class="absolute inset-x-4 bottom-full z-(--z-panel) mb-2 h-auto w-auto"
              >
                <CommandKeyBridge ref="slashPickerBridge">
                  <CommandList class="max-h-[min(20rem,45dvh)] overscroll-contain [scrollbar-gutter:stable]">
                    <CommandGroup
                      v-if="visibleSlashQuickActions.length"
                      :heading="$t('chat.slash.quickActions')"
                    >
                      <CommandItem
                        v-for="action in visibleSlashQuickActions"
                        :key="action.id"
                        :value="action.label"
                        @select="selectSlashQuickAction(action)"
                      >
                        <component
                          :is="action.icon"
                          class="size-4 shrink-0 text-muted-foreground"
                        />
                        <span class="min-w-0 flex-1">
                          <span class="block truncate text-control">{{ action.label }}</span>
                          <span class="block truncate text-caption text-muted-foreground">{{ action.description }}</span>
                        </span>
                      </CommandItem>
                    </CommandGroup>
                    <CommandSeparator
                      v-if="visibleSlashQuickActions.length && (visibleACPAgentCommands.length || visibleSlashSkills.length)"
                    />
                    <CommandGroup
                      v-if="visibleACPAgentCommands.length"
                      :heading="$t('chat.slash.agentCommands')"
                    >
                      <CommandItem
                        v-for="command in visibleACPAgentCommands"
                        :key="command.name"
                        :value="`/${command.name}`"
                        @select="selectACPAgentCommand(command)"
                      >
                        <span class="min-w-0 flex-1">
                          <span class="block truncate text-control">/{{ command.name }}</span>
                          <span
                            v-if="command.description"
                            class="block truncate text-caption text-muted-foreground"
                          >{{ command.description }}</span>
                          <span
                            v-if="command.input_hint"
                            class="block truncate text-caption text-muted-foreground"
                          >{{ $t('chat.slash.agentCommandInputHint', { hint: command.input_hint }) }}</span>
                        </span>
                      </CommandItem>
                    </CommandGroup>
                    <CommandSeparator v-if="visibleACPAgentCommands.length && visibleSlashSkills.length" />
                    <CommandGroup
                      v-if="visibleSlashSkills.length"
                      :heading="$t('chat.slash.skills')"
                    >
                      <CommandItem
                        v-for="skill in visibleSlashSkills"
                        :key="skill.name"
                        :value="skill.name"
                        @select="addRequestedSkill(skill)"
                      >
                        <Sparkles class="size-4 shrink-0 text-muted-foreground" />
                        <span class="min-w-0 flex-1">
                          <span class="block truncate text-control">{{ skill.display_name || skill.name }}</span>
                          <span
                            v-if="skill.description"
                            class="block truncate text-caption text-muted-foreground"
                          >{{ skill.description }}</span>
                        </span>
                      </CommandItem>
                    </CommandGroup>
                    <div
                      v-if="safeSkillCatalogLoading"
                      class="py-6 text-center text-body text-muted-foreground"
                    >
                      {{ $t('chat.slash.loadingSkills') }}
                    </div>
                    <div
                      v-else-if="!slashPanelHasResults"
                      class="py-6 text-center text-body text-muted-foreground"
                    >
                      {{ $t('chat.slash.noResults') }}
                    </div>
                  </CommandList>
                </CommandKeyBridge>
              </Command>
            </Transition>
            <ComposerDock
              ref="dockEl"
              :approvals="pendingApprovals"
              :command-panel="composerCommandPanel"
              :error-message="composerError"
              :pending-user-input="pendingUserInput"
              @select-command-item="selectCommandResultItem"
              @dismiss-command="clearCurrentCommandEvent"
              @reveal-composer="handleDockRevealComposer"
            >
              <!-- The composer is ALWAYS a two-row card (textarea on top,
                   controls below) — no pill↔multiline morph: a fixed rounded-2xl
                   box, so its shape never depends on the content and nothing
                   animates mid-typing.
                   Docked (non-welcome) state compresses and quiets: no min
                   height + tighter padding (p-2.5) + a shorter textarea row
                   (min-h-10) pull the two rows together — the centered welcome
                   card keeps the full presence (min-h-28, p-3); docked it sits
                   under the conversation and should read lighter, with the edge
                   softened to --border-soft (.chat-composer-docked, style.css).
                   Mobile radius is DERIVED from the control circles inside:
                   radius tracks the control radius — 44px controls → 22, i.e.
                   rounded-3xl (24, nearest rung); the same rule on desktop
                   (32px controls → 16) is exactly the rounded-2xl the card
                   already wears. (The concentric alternative, control radius +
                   padding = 32, read as too round in QA.) -->
              <div
                ref="composerEl"
                data-slot="input-group"
                role="group"
                class="chat-composer-edge relative flex w-full flex-wrap content-between items-end gap-1 rounded-2xl bg-surface-composer cursor-text max-md:rounded-3xl max-md:p-2.5"
                :class="isWelcome ? 'min-h-28 p-3' : 'p-2.5 chat-composer-docked'"
                @click="handleComposerClick"
              >
                <!-- The attachment row reveals via a grid 0fr↔1fr track so a card
                   is unveiled in place — it never translates and is always
                   clipped, so it can't overflow the box — while the composer
                   grows around it. The inner min-h-0 + overflow-hidden is what
                   lets the grid track actually collapse below content height. -->
                <Transition
                  enter-active-class="transition-[grid-template-rows] motion-reduce:transition-none"
                  enter-from-class="grid-rows-[0fr]"
                  enter-to-class="grid-rows-[1fr]"
                  leave-active-class="transition-[grid-template-rows] motion-reduce:transition-none"
                  leave-from-class="grid-rows-[1fr]"
                  leave-to-class="grid-rows-[0fr]"
                  :duration="ATTACHMENT_ANIM_MS"
                >
                  <div
                    v-if="showAttachmentGrid"
                    class="order-first grid w-full basis-full"
                    :style="{ transitionDuration: `${ATTACHMENT_ANIM_MS}ms`, transitionTimingFunction: 'cubic-bezier(0.25, 0.1, 0.25, 1)' }"
                  >
                    <div class="min-h-0 overflow-hidden">
                      <div class="flex flex-wrap gap-2 pb-1.5">
                        <ChatAttachmentCard
                          v-for="preview in pendingPreviews"
                          :key="preview.key"
                          :kind="preview.isPasted ? 'pasted' : (preview.isMedia ? 'media' : 'file')"
                          :src="preview.url"
                          :video="preview.isVideo"
                          :name="preview.file.name"
                          :ext="preview.ext"
                          :lines="preview.lines"
                          :text="preview.pastedText"
                          :size="preview.size"
                          :loading="preview.loading"
                          removable
                          :clickable="preview.isPasted || (preview.isMedia && !!preview.url)"
                          @remove="removeAttachment(preview.i)"
                          @preview="preview.isPasted ? (pastedViewerText = preview.pastedText) : openComposerPreview(preview.url)"
                        />
                      </div>
                    </div>
                  </div>
                </Transition>

                <Transition
                  enter-active-class="transition-opacity duration-150 ease-out"
                  enter-from-class="opacity-0"
                  enter-to-class="opacity-100"
                  leave-active-class="transition-opacity duration-100 ease-in"
                  leave-from-class="opacity-100"
                  leave-to-class="opacity-0"
                >
                  <div
                    v-if="skillSlashEnabled && requestedSkills.length"
                    class="order-first flex w-full basis-full flex-wrap gap-1.5 pb-1.5"
                  >
                    <div
                      v-for="skill in requestedSkills"
                      :key="requestedSkillKey(skill)"
                      class="flex min-h-8 max-w-full items-center gap-1.5 rounded-full bg-accent py-0.5 pl-2.5 pr-0.5 text-label text-foreground"
                    >
                      <Sparkles class="size-3.5 shrink-0 text-muted-foreground" />
                      <span class="min-w-0 truncate">{{ skill.display_name || skill.name }}</span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        :aria-label="$t('chat.slash.removeSkill', { name: skill.display_name || skill.name })"
                        class="shrink-0"
                        @click="removeRequestedSkill(skill)"
                      >
                        <X class="size-3.5" />
                      </Button>
                    </div>
                  </div>
                </Transition>

                <textarea
                  ref="textareaEl"
                  v-model="inputText"
                  rows="1"
                  :placeholder="activeChatReadOnly ? $t('chat.readonlyHint') : $t('chat.inputPlaceholder')"
                  :disabled="!currentBotId || activeChatReadOnly || loadingMessages || voiceInputState !== 'idle'"
                  class="order-none max-h-52 w-full basis-full field-sizing-content resize-none break-words bg-transparent pl-2 pr-1 pt-2 pb-1.5 text-base leading-[var(--chat-leading)] text-foreground outline-none placeholder:text-[var(--field-placeholder)] disabled:cursor-not-allowed"
                  :class="isWelcome ? 'min-h-12' : 'min-h-10'"
                  @keydown="handleComposerKeydown"
                  @paste="handlePaste"
                />

                <!-- max-md size bumps on the composer controls (the ＋ and voice
                     buttons, the model trigger, and the send ring below) grow the
                     tap targets to the 44px touch floor on phones; desktop keeps
                     the compact size. -->
                <DropdownMenu v-model:open="agentPopoverOpen">
                  <DropdownMenuTrigger as-child>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      shape="circle"
                      :disabled="!currentBotId || activeChatReadOnly || composerConfigPending"
                      :title="$t('chat.composerActions')"
                      class="order-1 self-end text-muted-foreground max-md:size-11"
                      :aria-label="$t('chat.composerActions')"
                    >
                      <Spinner
                        v-if="agentChanging"
                        class="size-4 max-md:size-5"
                      />
                      <Plus
                        v-else
                        :stroke-width="1.5"
                        class="size-4 max-md:size-5"
                      />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent
                    class="w-56"
                    align="start"
                    side="top"
                  >
                    <!-- The agent runtime is fixed once a session has any turns,
                       so the switcher only appears while the session is still
                       empty. Showing it disabled in an active chat just dangles
                       a choice that can't be made. -->
                    <template v-if="canChangeAgent && enabledBotAgents.length">
                      <DropdownMenuLabel>{{ $t('chat.agent') }}</DropdownMenuLabel>
                      <DropdownMenuItem @select="selectMemohAgent">
                        <img
                          src="/logo.svg"
                          alt=""
                          class="size-4 shrink-0"
                        >
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.agentMemoh') }}</span>
                        <Check
                          v-if="!activeIsExternalAgent"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-for="agent in enabledBotAgents"
                        :key="agent.id"
                        @select="selectBotAgent(agent)"
                      >
                        <component
                          :is="botAgentIcon(agent, true)"
                          class="size-4 shrink-0"
                        />
                        <span class="min-w-0 flex-1 truncate">{{ botAgentName(agent) }}</span>
                        <Check
                          v-if="activeBotAgentID === agent.id"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                    </template>
                    <!-- Folder binding. A draft picks where it lands here, the
                       same choice the sidebar's per-folder ＋ makes, so a new
                       chat isn't stuck folderless just because it was started
                       from the composer. Once the session exists the binding
                       pins its workspace target for life, so the picker gives
                       way to a read-only entry. -->
                    <template v-if="composerFolderPickable">
                      <DropdownMenuSeparator v-if="canChangeAgent && enabledBotAgents.length" />
                      <DropdownMenuLabel>{{ $t('chat.folder') }}</DropdownMenuLabel>
                      <DropdownMenuItem @select="clearWorkingFolder">
                        <X class="size-4 shrink-0" />
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.folderDetachDraft') }}</span>
                        <Check
                          v-if="!draftWorkingFolder"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-for="folder in selectableFolders"
                        :key="folder.id"
                        @select="selectWorkingFolder(folder)"
                      >
                        <FolderOpen class="size-4 shrink-0" />
                        <span class="min-w-0 flex-1 truncate">{{ folder.name }}</span>
                        <Check
                          v-if="draftWorkingFolder?.id === folder.id"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                    </template>
                    <template v-else-if="composerFolderLocked">
                      <DropdownMenuSeparator v-if="canChangeAgent && enabledBotAgents.length" />
                      <DropdownMenuLabel>{{ $t('chat.folder') }}</DropdownMenuLabel>
                      <DropdownMenuItem disabled>
                        <FolderOpen class="size-4 shrink-0" />
                        <span class="min-w-0 flex-1 truncate">{{ composerFolderName }}</span>
                        <Check class="ml-auto" />
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-if="!activeSession"
                        @select="clearWorkingFolder"
                      >
                        <X class="size-4 shrink-0" />
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.folderDetachDraft') }}</span>
                      </DropdownMenuItem>
                    </template>
                    <DropdownMenuSeparator v-if="(canChangeAgent && enabledBotAgents.length) || showComposerFolderSection" />
                    <DropdownMenuItem
                      :disabled="!currentBotId || activeChatReadOnly || streaming || loadingMessages"
                      @select="fileInput?.click()"
                    >
                      <Paperclip />
                      <span class="min-w-0 flex-1 truncate">{{ $t('chat.attachFiles') }}</span>
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>

                <!-- Destination selector: a peer of the ＋ menu in the
                     controls row. Selection only; ACL lives elsewhere. -->
                <ComposerContinueOn
                  v-if="showComputersMenu"
                  :targets="workspaceTargets"
                  :selected-target-id="selectedWorkspaceTargetId"
                  :selected-missing="selectedWorkspaceTargetMissing"
                  :selected-snapshot-name="workspaceTargetSelection.snapshot?.name ?? ''"
                  :locked="computerSwitchLocked"
                  :initial-loading="workspaceTargetsInitialLoading"
                  :load-failed="workspaceTargetsLoadFailed"
                  :bot-id="currentBotId ?? ''"
                  :bot-name="currentBot?.display_name || currentBot?.name || ''"
                  @select="selectWorkspaceTarget"
                  @menu-open="refetchWorkspaceTargets"
                />

                <!-- The controls row owns the remaining width and right-aligns,
                     so a long model name truncates instead of overflowing. -->
                <div class="order-3 flex min-w-0 flex-1 items-center justify-end gap-2 self-end">
                  <Popover
                    v-if="!activeUsesExternalAgentComposer || activeUsesACPRuntime || activeUsesDirectRuntime"
                    v-model:open="modelPopoverOpen"
                  >
                    <PopoverTrigger as-child>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        shape="circle"
                        :disabled="!currentBotId || activeChatReadOnly || composerConfigPending"
                        class="composer-pill-press min-w-0 shrink max-md:h-11"
                        :style="{ maxWidth: `${modelTriggerMaxWidth}px` }"
                      >
                        <!-- One transformable wrapper for the press squish —
                             same contract as composer-continue-on's pill. -->
                        <span class="composer-pill-content inline-flex min-w-0 items-center gap-2">
                          <Spinner
                            v-if="composerConfigPending || composerModelsLoading"
                            class="size-3.5 shrink-0"
                          />
                          <span class="min-w-0 truncate text-label text-composer-control-label">{{ modelTriggerLabel }}</span>
                          <ChevronDown
                            class="size-3.5 shrink-0 text-muted-foreground"
                            :stroke-width="1.5"
                          />
                        </span>
                      </Button>
                    </PopoverTrigger>
                    <!-- `menu` makes this host transparent: the inner
                         menuChromeClass div already owns the border/shadow/
                         radius, so a chromed host would draw a doubled edge
                         (same pattern as model-select.vue). -->
                    <PopoverContent
                      menu
                      class="w-80 max-w-[calc(100vw-2rem)] overflow-hidden p-0"
                      align="end"
                      side="top"
                      :side-offset="4"
                    >
                      <!-- The chrome wrapper covers BOTH branches: with the
                           host transparent (`menu`), a bare loading row would
                           float on the chat UI with no surface at all. -->
                      <div :class="menuChromeClass">
                        <InlineLoadingRow
                          v-if="composerModelsLoading"
                          class="px-2 py-3"
                        >
                          {{ $t('common.loading') }}
                        </InlineLoadingRow>
                        <div
                          v-else-if="directModelCatalogError"
                          class="space-y-3 p-3"
                        >
                          <p class="text-body text-muted-foreground">
                            {{ directModelCatalogError }}
                          </p>
                          <Button
                            v-if="directRuntimeAuthRequired"
                            variant="outline"
                            size="sm"
                            class="w-full"
                            @click="openDirectAgentSettings"
                          >
                            {{ $t('bots.agent.openSettings') }}
                          </Button>
                          <Button
                            v-else
                            variant="outline"
                            size="sm"
                            class="w-full"
                            @click="retryDirectModelCatalog"
                          >
                            {{ $t('common.retry') }}
                          </Button>
                        </div>
                        <template v-else>
                          <div
                            v-if="activeUsesACPRuntime && !activeIsPendingExternalAgent && acpModes.length"
                            class="border-b border-border p-3"
                          >
                            <div class="mb-2 text-label text-foreground">
                              {{ $t('chat.sessionMode') }}
                            </div>
                            <Select
                              :model-value="currentACPModeId"
                              :disabled="activeChatReadOnly || streaming || acpConfigChanging"
                              @update:model-value="onACPModeSelected"
                            >
                              <SelectTrigger class="w-full">
                                <SelectValue :placeholder="$t('chat.sessionModePlaceholder')" />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem
                                  v-for="mode in acpModes"
                                  :key="mode.id"
                                  :value="mode.id"
                                >
                                  <div class="min-w-0">
                                    <div class="truncate">
                                      {{ mode.name?.trim() || mode.id }}
                                    </div>
                                    <div
                                      v-if="mode.description?.trim()"
                                      class="text-caption text-muted-foreground"
                                    >
                                      {{ mode.description }}
                                    </div>
                                  </div>
                                </SelectItem>
                              </SelectContent>
                            </Select>
                            <p class="mt-2 rounded-md border border-warning-border bg-warning-soft p-2 text-caption text-warning-foreground">
                              {{ $t('chat.sessionModeCaution') }}
                            </p>
                          </div>
                          <ModelOptions
                            :model-value="overrideModelId"
                            :reasoning-effort="overrideReasoningEffort"
                            :reasoning-options="composerReasoningOptions"
                            :models="composerModels"
                            :providers="composerModelProviders"
                            :none-label="activeUsesDirectRuntime ? composerDefaultModelLabel : undefined"
                            model-type="chat"
                            :open="modelPopoverOpen"
                            :show-reasoning="!activeUsesDirectRuntime || !!composerReasoningOptions?.length"
                            @update:model-value="onComposerModelValueSelected"
                            @update:reasoning-effort="onComposerReasoningEffortSelected"
                          />
                        </template>
                      </div>
                    </PopoverContent>
                  </Popover>

                  <div class="relative size-8 max-md:size-11 shrink-0">
                    <!-- Mic and send share this one slot (never both visible):
                         with nothing to send, voice input IS the affordance
                         here; typing (or attaching) hands it to send. Mic is a
                         filled PRIMARY circle at rest (near-black, not brand —
                         brand stays scarce, reserved for send/stop), so the
                         slot reads as one continuous filled control that swaps
                         its glyph and meaning on the same cross-fade timing.
                         The context-pressure ring itself is only shelved for
                         now, not deleted — its useSessionInfo data source
                         stays wired below (untouched) because the /compact
                         quick action's live percentage still reads off it. -->
                    <Button
                      type="button"
                      variant="primary"
                      shape="circle"
                      :disabled="voiceInputDisabled"
                      :title="voiceInputLabel"
                      :aria-label="voiceInputLabel"
                      class="absolute inset-0 size-8 max-md:size-11 rounded-full transition-[opacity,scale] duration-200 ease-out motion-reduce:transition-none"
                      :class="micVisible ? 'scale-100 opacity-100' : 'pointer-events-none scale-75 opacity-0'"
                      @click="handleVoiceInput"
                    >
                      <Spinner
                        v-if="voiceInputState === 'transcribing'"
                        class="size-4 max-md:size-5"
                      />
                      <svg
                        v-else
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2.5"
                        stroke-linecap="round"
                        class="size-4.5 max-md:size-5"
                        :class="voiceInputState === 'recording' ? 'motion-safe:animate-pulse' : undefined"
                        aria-hidden="true"
                      >
                        <!-- Relaxed envelope: the center bar spans only 14 of
                             the 24 viewBox units — the full-18 spike made the
                             glyph read tense. Ends are vertical ovals
                             (2.5 × 3.5 — a hair taller than pure circles), mids
                             hold half the max (7). The 4.5-unit gaps are
                             untouched: denser spacing smudges at this size. -->
                        <path d="M3 11.5v1" />
                        <path d="M7.5 8.5v7" />
                        <path d="M12 5v14" />
                        <path d="M16.5 8.5v7" />
                        <path d="M21 11.5v1" />
                      </svg>
                    </Button>
                    <!-- Send and stop are one brand circle: the surface never
                         changes between the two states, only the glyph cross-fades
                         (arrow ⇄ stop square), so the button can't blink color or
                         shape mid-turn. While streaming it stays clickable to abort. -->
                    <Button
                      type="button"
                      variant="brand"
                      :disabled="streaming ? false : (!showSend || !currentBotId || activeChatReadOnly || loadingMessages || composerConfigPending || composerHasNoModel)"
                      :aria-label="streaming ? 'Stop generating response' : 'Send message'"
                      class="absolute inset-0 size-8 max-md:size-11 rounded-full transition-[opacity,scale] duration-200 ease-[cubic-bezier(0.34,1.56,0.64,1)] motion-reduce:transition-none"
                      :class="sendButtonVisible ? 'scale-100 opacity-100' : 'pointer-events-none scale-0 opacity-0'"
                      @click="streaming ? chatStore.abort(paneTarget) : handleSend()"
                    >
                      <span
                        class="grid size-[18px] max-md:size-5 shrink-0 place-items-center"
                        aria-hidden="true"
                      >
                        <svg
                          viewBox="0 0 24 24"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="2.75"
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          class="col-start-1 row-start-1 size-[18px] max-md:size-5 transition-opacity duration-200 ease-out motion-reduce:transition-none"
                          :class="streaming ? 'opacity-0' : 'opacity-100'"
                        >
                          <path d="M12 19.5 V5" />
                          <path d="M6 10.5 L12 4.5 L18 10.5" />
                        </svg>
                        <svg
                          viewBox="0 0 24 24"
                          fill="currentColor"
                          class="col-start-1 row-start-1 size-4 max-md:size-4.5 transition-opacity duration-200 ease-out motion-reduce:transition-none"
                          :class="streaming ? 'opacity-100' : 'opacity-0'"
                        >
                          <rect
                            x="4"
                            y="4"
                            width="16"
                            height="16"
                            rx="3"
                          />
                        </svg>
                      </span>
                    </Button>
                  </div>
                </div>
              </div>
            </ComposerDock>
          </div>
        </div>
      </div>
    </template>

    <!-- Region-scoped drop feedback. Sits OUTSIDE the v-else so it can also
         cover the "pick a bot" placeholder — where the zone is disabled, so the
         overlay stays dark and the OS shows its no-drop cursor instead of the
         drop silently doing nothing. Dropped files land in the attachment tray,
         unsent: the user still gets to type the message that goes with them. -->
    <FileDropOverlay
      :active="dropActive"
      :bounds="dropBounds"
      :icon="ImagePlus"
      :label="$t('chat.dropToAttach')"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onBeforeUnmount, useTemplateRef, watch, onWatcherCleanup, nextTick, onActivated, onDeactivated } from 'vue'
import {
  ImagePlus,
  Paperclip,
  Plus,
  ChevronDown,
  ArrowDown,
  Check,
  FolderOpen,
  Sparkles,
  X,
  HelpCircle,
  List,
  Minimize2,
  Package,
  SquarePen,
  ShieldCheck,
} from 'lucide-vue-next'
import { Button, Command, CommandGroup, CommandItem, CommandKeyBridge, CommandList, CommandSeparator, Dialog, DialogContent, DialogHeader, DialogTitle, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger, InlineLoadingRow, PanePlaceholder, Popover, PopoverContent, PopoverTrigger, ScrollArea, Select, SelectContent, SelectItem, SelectTrigger, SelectValue, Spinner, menuChromeClass, toast } from '@felinic/ui'
import { useChatStore, type ExternalAgentSessionInput, type ChatMessage, type ChatWorkspaceTargetSnapshot, type SendMessageResult } from '@/store/chat-list'
import { useWorkdirsStore } from '@/store/workdirs'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { storeToRefs } from 'pinia'
import { useElementSize, useIntersectionObserver } from '@vueuse/core'
import { useQuery } from '@pinia/colada'
import { getAcpProfiles, getBotsByBotIdAgents, getBotsByBotIdSettings, getBotsByBotIdWorkspaceTargets, postTranscriptionModelsByIdTest } from '@memohai/sdk'
import type { AcpprofilePublicProfile, BotagentsBotAgent, WorkspaceWorkspaceTarget } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import FileDropOverlay from '@/components/file-drop-overlay/index.vue'
import { useFileDropZone } from '@/composables/useFileDropZone'
import { registerChatFileDropTarget } from '../composables/chat-file-drop-target'
import { readDroppedFiles } from '@/utils/dropped-files'
import MessageItem from './message-item.vue'
import ComposerContinueOn from './composer-continue-on.vue'
import ChatAttachmentCard from './chat-attachment-card.vue'
import { useChatScroll } from '../composables/useChatScroll'
import BgTaskPill from './bg-task-pill.vue'
import ForkSourceDivider from './fork-source-divider.vue'
import ChatForkDialog from './chat-fork-dialog.vue'
import ComposerDock from './composer-dock.vue'
import { usePendingApprovals } from '../composables/usePendingApprovals'
import ChatScrollRail, { type ScrollRailSegment } from './chat-scroll-rail.vue'
import { provideBgTaskBeacons } from '../composables/useBgTaskBeacons'
import MediaGalleryLightbox from './media-gallery-lightbox.vue'
import { useSessionInfo } from '../composables/useSessionInfo'
import ModelOptions from '@/pages/bots/components/model-options.vue'
import { EFFORT_LABELS, REASONING_EFFORT_DISABLE, reconcileStoredEffort } from '@/pages/bots/components/reasoning-effort'
import { useMediaGallery } from '../composables/useMediaGallery'
import { ATTACHMENT_ANIM_MS, attachmentToFile, fileToAttachment, useComposerAttachments } from '../composables/useComposerAttachments'
import { useComposerDrafts } from '../composables/useComposerDrafts'
import { COMPOSER_MASK_BELOW_PX, useComposerLayout } from '../composables/useComposerLayout'
import { provideChatViewTarget } from '../composables/useChatViewContext'
import { provideConnectorLogos } from '../composables/useConnectorLogos'
import { fetchSafeSkillCatalog, fetchSession, type ChatAttachment, type CommandActionError, type CommandActionListItem, type RequestedSkillSelection, type UIUserInput } from '@/composables/api/useChat'
import { commandResultPresentation, isCommandResultItemVisible, resolveCommandResultSelection } from './slash-command-result'
import { captureChatPaneSendContext, composerHasNoModel as hasNoComposerModel, matchesChatPaneSendContext, pinnedSubagentModelId as resolvePinnedSubagentModelId, shouldRefreshACPComposerConfig } from './chat-pane-send'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { useACPRuntime } from '@/composables/useACPRuntime'
import { useAgentModelCatalog } from '@/composables/useAgentModelCatalog'
import { useIsMobile } from '@/composables/useIsMobile'
import { useVirtualKeyboard } from '@/composables/useVirtualKeyboard'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH, findMissingRequiredManagedField, normalizeACPAgentID, readACPAgentConfig } from '@/utils/acp'
import { BOT_AGENT_RUNTIME_ACP, BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, botAgentIcon, botAgentName, botAgentProvider, isDirectBotAgentConfigured, normalizeBotAgentRuntime } from '@/utils/bot-agent'
import { isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'
import { hasBotPermission } from '@/utils/bot-permissions'
import { workspaceTargetAvailable } from '@/utils/workspace-target'
import { findLatestPendingChatDecision } from './chat-pending-decision'
import {
  acpSlashCommandComposerText,
  composerLocalQuickActionID,
  isBoundACPRuntimeForTarget,
  visibleACPSlashCommands,
  type ACPAvailableCommand,
} from '@/utils/acp-slash-commands'

const props = withDefaults(defineProps<{
  // Stable dockview panel id (e.g. `chat:3`). Used for per-tab composer drafts and
  // the keep-alive key — it does NOT change when a draft acquires a real session.
  tabId?: string
  // The session this pane renders (null = unsaved draft). Decoupled from tabId so
  // a draft→real promotion never remounts this pane.
  sessionId?: string | null
  visible?: boolean
  active?: boolean
}>(), {
  tabId: 'chat',
  sessionId: null,
  visible: true,
  active: true,
})

const { t } = useI18n()
const router = useRouter()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()
const { pill: bgTaskPill, scrollToOffscreen, cleanup: cleanupBgTaskBeacons } = provideBgTaskBeacons()
onBeforeUnmount(cleanupBgTaskBeacons)
const {
  fileInput,
  pendingFiles,
  pendingPreviews,
  composerPreviewItems,
  composerPreviewIndex,
  openComposerPreview,
  pastedViewerText,
  pastedViewerOpen,
  showAttachmentGrid,
  removeAttachment,
  handleFileInputChange,
  handlePaste,
} = useComposerAttachments()

const composerError = ref('')
const forkDialogOpen = ref(false)
const pendingForkTurnId = ref('')
const modelPopoverOpen = ref(false)
const agentPopoverOpen = ref(false)
const agentChanging = ref(false)
const acpConfigChangeScope = ref('')

const {
  currentBotId,
  bots,
  loadingChats,
  hasExplicitSessionSelection,
} = storeToRefs(chatStore)

const isActive = computed(() => props.active !== false)
const isVisible = computed(() => props.visible !== false)
const paneTarget = computed(() => ({
  botId: currentBotId.value?.trim() ?? '',
  sessionId: props.sessionId?.trim() || null,
  viewId: props.tabId.trim() || 'chat',
}))
provideChatViewTarget(paneTarget)
// Resolved once per pane so every tool row below can mark a Connect-It call
// with its connector's logo without each row running its own lookup.
provideConnectorLogos(paneTarget)
const paneView = computed(() => chatStore.chatView(paneTarget.value))
const messages = computed(() => paneView.value.transcript.visibleMessages.value)
const loadingMessages = computed(() => paneView.value.transcript.loadingMessages.value)
const loadingOlder = computed(() => paneView.value.transcript.loadingOlder.value)
const hasMoreOlder = computed(() => paneView.value.transcript.hasMoreOlder.value)
const streaming = computed(() => chatStore.isChatViewStreaming(paneTarget.value))
const creatingSession = computed(() => chatStore.isChatViewCreatingSession(paneTarget.value))
const activeChatTarget = computed(() => chatStore.chatTargetFor(paneTarget.value))
const activeSession = computed(() => activeChatTarget.value.session)
const activeChatReadOnly = computed(() => chatStore.chatReadOnlyFor(paneTarget.value))
const activeChatCanFork = computed(() => chatStore.chatCanForkFor(paneTarget.value))
const overrideModelId = ref('')
const overrideReasoningEffort = ref('')
// Set once the user picks a model in this pane, so late-arriving defaults
// (a subagent's pinned model, bot settings) never overwrite their choice.
const userPickedModel = ref(false)
const paneComposerScope = computed(() => {
  const botId = paneTarget.value.botId
  return botId ? `${botId}:${paneTarget.value.viewId}` : 'chat'
})
const startupSendFailure = computed(() => chatStore.startupSendFailureFor(
  paneTarget.value,
  paneComposerScope.value,
))
const hasRenderedSession = computed(() =>
  !!(paneTarget.value.sessionId || activeChatTarget.value.sessionId || '').trim(),
)

// A fresh, writable chat opens with the composer centred and a greeting above
// it. Read-only sessions (system / synced channel threads) hide the composer
// entirely, so they never reach this state.
const isWelcome = computed(() =>
  !!currentBotId.value
  && !hasRenderedSession.value
  && !activeChatReadOnly.value
  && !loadingChats.value
  && messages.value.length === 0,
)

// During boot, "a draft that stays a draft" and "a draft about to be
// repointed to the most recent session" are indistinguishable until
// fetchSessions returns (bootstrap auto-picks at the END of the load).
// isWelcome waits out that window via !loadingChats — but rendering the
// docked posture meanwhile made a hard refresh of the welcome page flash
// bottom → center. While placement is undecidable, hide the composer
// instead: `invisible` keeps layout and the dock measurements alive,
// where v-if would unmount them. A session panel carries its sessionId
// from the first frame, so this gate never engages on session routes.
const composerPlacementPending = computed(() => loadingChats.value && !hasRenderedSession.value)

// Rotate the greeting per fresh chat so the entry point feels alive rather than
// a fixed banner; the pick stays stable while a single welcome screen is shown
// and re-rolls when a new empty chat (bot/session) is opened.
const WELCOME_GREETING_KEYS = [
  'chat.welcome.g1', 'chat.welcome.g2', 'chat.welcome.g3', 'chat.welcome.g4',
  'chat.welcome.g5', 'chat.welcome.g6', 'chat.welcome.g7', 'chat.welcome.g8',
  'chat.welcome.g9', 'chat.welcome.g10', 'chat.welcome.g11', 'chat.welcome.g12',
] as const
function pickWelcomeGreetingIndex() {
  return Math.floor(Math.random() * WELCOME_GREETING_KEYS.length)
}
const welcomeGreetingIndex = ref(pickWelcomeGreetingIndex())
const welcomeGreeting = computed(() => {
  // A draft under a working folder names its destination instead of the
  // generic rotation — the greeting doubles as the binding's visibility.
  const folderName = draftWorkingFolder.value?.name?.trim()
  if (folderName) return t('chat.welcome.folder', { name: folderName })
  return t(WELCOME_GREETING_KEYS[welcomeGreetingIndex.value] ?? WELCOME_GREETING_KEYS[0])
})
watch([isWelcome, currentBotId, () => activeSession.value?.id], ([welcome]) => {
  if (welcome) welcomeGreetingIndex.value = pickWelcomeGreetingIndex()
})

const pendingDecision = computed(() => findLatestPendingChatDecision(messages.value))
const pendingUserInput = computed<UIUserInput | null>(() => (
  pendingDecision.value?.kind === 'user_input'
    ? pendingDecision.value.userInput
    : null
))

const { items: pendingApprovals } = usePendingApprovals(messages)

const hasPendingToolApproval = computed(() => pendingApprovals.value.length > 0)

const canForkAssistant = computed(() =>
  !streaming.value
  && !loadingMessages.value
  && !activeChatReadOnly.value
  && activeChatCanFork.value
  && (activeChatTarget.value.runtimeType === 'model'
    || activeChatTarget.value.runtimeType === BOT_AGENT_RUNTIME_CODEX),
)

// Retry/edit rewrite persisted history and replay it, which only runtimes
// whose context Memoh itself assembles can honor.
const activeSupportsTurnReplacement = computed(() => activeChatTarget.value.runtimeType === 'model')

// The turn id, not a message id: a turn carries it from admission, so the
// affordance is live the moment the round exists rather than after the
// database twin of that round has been fetched back.
const latestRetryableAssistantTurnId = computed(() => {
  if (streaming.value || loadingMessages.value || activeChatReadOnly.value) return ''
  if (!activeSupportsTurnReplacement.value) return ''
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const message = messages.value[i]
    if (message?.role === 'assistant' && !message.streaming && !message.__optimistic) {
      return message.turnId?.trim() ?? ''
    }
  }
  return ''
})

const latestEditableUserTurnId = computed(() => {
  if (streaming.value || loadingMessages.value || activeChatReadOnly.value) return ''
  if (!activeSupportsTurnReplacement.value) return ''
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const message = messages.value[i]
    if (message?.role === 'user' && !message.streaming && !message.__optimistic) {
      return message.turnId?.trim() ?? ''
    }
  }
  return ''
})

// Both halves of a round share one turn id, so the role decides which
// affordance a message gets: retry belongs to the reply, edit to the request.
function isRetryableTurn(message: ChatMessage): boolean {
  const turnId = latestRetryableAssistantTurnId.value
  return message.role === 'assistant' && turnId !== '' && turnId === (message.turnId?.trim() ?? '')
}

function isEditableTurn(message: ChatMessage): boolean {
  const turnId = latestEditableUserTurnId.value
  return message.role === 'user' && turnId !== '' && turnId === (message.turnId?.trim() ?? '')
}

const { data: botSettings, isLoading: botSettingsLoading } = useQuery({
  key: () => ['bot-settings', currentBotId.value],
  query: async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const { data } = await (getBotsByBotIdSettings as any)({
      path: { bot_id: currentBotId.value! },
      throwOnError: true,
    })
    return data as import('@memohai/sdk').SettingsSettings | undefined
  },
  enabled: () => !!currentBotId.value,
})

const { data: acpProfileData, isLoading: acpProfilesLoading } = useQuery({
  key: () => ['acp-profiles'],
  query: async () => {
    const { data } = await getAcpProfiles({ throwOnError: true })
    return data
  },
})

const { data: botAgentData, isLoading: botAgentsLoading } = useQuery({
  key: () => ['bot-agents', currentBotId.value],
  query: async () => {
    const { data } = await getBotsByBotIdAgents({
      path: { bot_id: currentBotId.value! },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!currentBotId.value,
})

const currentBot = computed(() => bots.value.find(bot => bot.id === currentBotId.value) ?? null)
const canWorkspaceRead = computed(() => (
  hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_read')
))

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & {
  target_id: string
  kind: string
}

const {
  data: workspaceTargetsResponse,
  error: workspaceTargetsError,
  isLoading: workspaceTargetsLoading,
  refetch: refetchWorkspaceTargets,
} = useQuery({
  key: () => ['bot-workspace-targets', currentBotId.value ?? ''],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: currentBotId.value! },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!currentBotId.value && canWorkspaceRead.value,
  refetchOnWindowFocus: true,
})

const workspaceTargets = computed<ValidWorkspaceTarget[]>(() => (
  (workspaceTargetsResponse.value?.targets ?? []).filter((target): target is ValidWorkspaceTarget => (
    typeof target.target_id === 'string'
    && target.target_id.length > 0
    && typeof target.kind === 'string'
    && target.kind.length > 0
  ))
))
const primaryWorkspaceTarget = computed(() => (
  workspaceTargets.value.find(target => target.primary)
  ?? workspaceTargets.value.find(target => target.target_id === 'native')
  ?? null
))
const workspaceTargetsInitialLoading = computed(() => (
  workspaceTargetsLoading.value && !workspaceTargetsResponse.value
))
const workspaceTargetsLoadFailed = computed(() => (
  !!workspaceTargetsError.value && !workspaceTargetsResponse.value
))

// A third-party synced thread (Telegram/Discord/...) is a multi-participant
// group conversation rather than the local 1:1 chat. The message list switches
// to a group layout for these: every turn is left-aligned with an avatar +
// sender name + channel badge, including the bot's own replies.
const channelPlatform = computed(() => (activeSession.value?.channel_type ?? '').trim().toLowerCase())
const isChannelThread = computed(() => !!channelPlatform.value && channelPlatform.value !== 'local')

interface ForkSourceMeta {
  sessionId: string
  title: string
  sourceMessageId?: string
  forkMessageId?: string
}

const acpProfiles = computed<AcpprofilePublicProfile[]>(() => acpProfileData.value?.items ?? [])
const currentBotMetadata = computed(() => currentBot.value?.metadata as Record<string, unknown> | undefined)
const botAgents = computed<BotagentsBotAgent[]>(() => botAgentData.value?.items ?? [])
const enabledBotAgents = computed(() => botAgents.value.filter(agent => agent.enabled !== false && !!agent.id))

const activeSessionMetadata = computed<Record<string, unknown>>(() => activeChatTarget.value.metadata)
const forkSource = computed<ForkSourceMeta | null>(() => {
  const raw = activeSessionMetadata.value.forked_from
  if (!raw || typeof raw !== 'object') return null
  const record = raw as Record<string, unknown>
  const sessionId = String(record.session_id ?? '').trim()
  if (!sessionId) return null
  const title = String(record.title ?? '').trim() || t('chat.unknownSession')
  const sourceMessageId = String(record.message_id ?? '').trim()
  const forkMessageId = String(record.fork_message_id ?? '').trim()
  return {
    sessionId,
    title,
    ...(sourceMessageId ? { sourceMessageId } : {}),
    ...(forkMessageId ? { forkMessageId } : {}),
  }
})
const forkSourceTitle = computed(() => forkSource.value?.title ?? '')
const openingForkSource = ref(false)
const forkSourceDividerAfterIndex = computed<number | null>(() => {
  const source = forkSource.value
  if (!source || messages.value.length === 0) return null
  const forkMessageId = source.forkMessageId?.trim()
  if (!forkMessageId) return null
  const index = messages.value.findIndex(messageMatchesForkSource)
  return index >= 0 ? index : null
})
const activeIsPendingExternalAgent = computed(() => activeChatTarget.value.isPendingExternalAgent)
const activeIsExternalAgent = computed(() => activeChatTarget.value.isExternalAgent)
const activeUsesExternalAgentComposer = computed(() => activeIsPendingExternalAgent.value || activeIsExternalAgent.value)
// ---- workdir binding ----
// A session bound to a bot workdir (or a draft under the bot's working
// folder) has its workspace target pinned by that binding: the computer
// switcher is replaced by a read-only folder entry, and sends carry no
// explicit workspace_target_id — the backend derives it from the binding.
const workdirsStore = useWorkdirsStore()
watch(() => currentBotId.value, (botId) => {
  if (botId) void workdirsStore.ensureWorkdirs(botId)
}, { immediate: true })
const activeSessionWorkdirId = computed(() => (activeSession.value?.workdir_id ?? '').trim())
const draftWorkingFolder = computed(() => {
  if (activeSession.value || !currentBotId.value) return null
  const workdir = workdirsStore.workingWorkdirFor(currentBotId.value)
  if (!workdir) return null
  // External Agent sessions can only bind native-workspace workdirs; a remote working
  // workdir is skipped at creation, so don't pretend it applies here.
  if (activeUsesExternalAgentComposer.value && workdir.target_kind === 'remote') return null
  return workdir
})
const composerFolderLocked = computed(() => (
  !!activeSessionWorkdirId.value || !!draftWorkingFolder.value
))
const composerFolderName = computed(() => {
  if (activeSessionWorkdirId.value) {
    const workdir = workdirsStore.workdirById(currentBotId.value, activeSessionWorkdirId.value)
    return workdir?.name?.trim() || t('chat.folderUnavailable')
  }
  return draftWorkingFolder.value?.name?.trim() || t('chat.folderUnavailable')
})
// Folders a draft may bind to. ACP runs only in the native workspace, so a
// remote folder is left out rather than offered as a choice that binds nothing.
const selectableFolders = computed(() => {
  const folders = workdirsStore.workdirsFor(currentBotId.value).filter(folder => !folder.archived && !!folder.id)
  if (activeUsesExternalAgentComposer.value) return folders.filter(folder => folder.target_kind !== 'remote')
  return folders
})
// The picker only makes sense before the session exists; an empty folder list
// falls through to the locked entry (or to nothing at all).
const composerFolderPickable = computed(() => !activeSession.value && selectableFolders.value.length > 0)
const showComposerFolderSection = computed(() => composerFolderPickable.value || composerFolderLocked.value)

function selectWorkingFolder(folder: BotWorkdir) {
  workdirsStore.setWorkingWorkdir(currentBotId.value, folder.id ?? null)
}

function clearWorkingFolder() {
  workdirsStore.setWorkingWorkdir(currentBotId.value, null)
}

// Sends from a folder-bound chat carry no explicit workspace_target_id: the
// binding decides the target, and a lingering earlier selection would be
// rejected by the backend as a target conflict.
const sendWorkspaceTargetId = computed(() => (
  composerFolderLocked.value ? '' : selectedWorkspaceTargetId.value
))

const showComputersMenu = computed(() => (
  !activeIsExternalAgent.value
  && !activeIsPendingExternalAgent.value
  && canWorkspaceRead.value
  && !composerFolderLocked.value
))
const computerSwitchLocked = computed(() => (
  streaming.value
  || creatingSession.value
  || loadingMessages.value
  || agentChanging.value
  || hasPendingToolApproval.value
  || !!pendingUserInput.value
))
const workspaceTargetSelection = computed(() => (
  chatStore.workspaceTargetSelectionFor(paneTarget.value)
))
const selectedWorkspaceTargetId = computed(() => workspaceTargetSelection.value.targetId)
const selectedWorkspaceTarget = computed(() => (
  workspaceTargets.value.find(target => target.target_id === selectedWorkspaceTargetId.value) ?? null
))
const selectedWorkspaceTargetMissing = computed(() => (
  !!selectedWorkspaceTargetId.value
  && !selectedWorkspaceTarget.value
  && !workspaceTargetsInitialLoading.value
))

function snapshotForWorkspaceTarget(target: ValidWorkspaceTarget): ChatWorkspaceTargetSnapshot {
  return {
    target_id: target.target_id,
    kind: target.kind,
    name: target.name,
  }
}

function workspaceTargetFromSessionMetadata(metadata: Record<string, unknown>): ChatWorkspaceTargetSnapshot | null {
  const rawSnapshot = metadata.workspace_target
  const snapshot = rawSnapshot && typeof rawSnapshot === 'object'
    ? rawSnapshot as Record<string, unknown>
    : {}
  const targetId = String(metadata.workspace_target_id ?? snapshot.target_id ?? '').trim()
  if (!targetId) return null
  const value = (key: string) => {
    const raw = snapshot[key]
    return typeof raw === 'string' && raw.trim() ? raw.trim() : undefined
  }
  return {
    target_id: targetId,
    kind: value('kind'),
    name: value('name'),
  }
}

function selectWorkspaceTarget(target: ValidWorkspaceTarget) {
  if (computerSwitchLocked.value || !workspaceTargetAvailable(target)) return
  chatStore.setWorkspaceTargetSelection(
    paneTarget.value,
    target.target_id,
    snapshotForWorkspaceTarget(target),
  )
}

watch([
  activeIsExternalAgent,
  activeIsPendingExternalAgent,
  activeSessionMetadata,
  workspaceTargets,
  paneTarget,
], () => {
  if (activeIsExternalAgent.value || activeIsPendingExternalAgent.value) {
    chatStore.resetWorkspaceTargetSelection(paneTarget.value)
    return
  }
  const sessionTarget = paneTarget.value.sessionId
    ? workspaceTargetFromSessionMetadata(activeSessionMetadata.value)
    : null
  if (sessionTarget) {
    chatStore.initializeWorkspaceTargetSelection(
      paneTarget.value,
      sessionTarget.target_id,
      sessionTarget,
      'session',
    )
    return
  }
  const primary = primaryWorkspaceTarget.value
  if (!primary) return
  chatStore.initializeWorkspaceTargetSelection(
    paneTarget.value,
    primary.target_id,
    snapshotForWorkspaceTarget(primary),
    'default',
  )
}, { immediate: true })
const activeBotAgentID = computed(() =>
  activeSession.value?.bot_agent_id?.trim()
  || chatStore.pendingExternalAgentStateFor(paneTarget.value)?.input.botAgentId?.trim()
  || '',
)
const activeUsesACPRuntime = computed(() => (
  activeUsesExternalAgentComposer.value && activeChatTarget.value.runtimeType === 'acp_agent'
))
const activeDirectRuntime = computed(() => {
  if (!activeUsesExternalAgentComposer.value) return ''
  const runtime = activeChatTarget.value.runtimeType
  if (runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE) return runtime
  return ''
})
const activeUsesDirectRuntime = computed(() => activeDirectRuntime.value !== '')
const activeACPAgentId = computed(() => normalizeACPAgentID(activeSessionMetadata.value.acp_agent_id))
const activeACPProjectPath = computed(() => String(activeSessionMetadata.value.project_path ?? '').trim())
const activeACPProjectMode = computed(() => String(activeSessionMetadata.value.acp_project_mode ?? '').trim())
const acpOperationScope = computed(() => JSON.stringify([
  paneTarget.value.botId,
  paneTarget.value.sessionId,
  paneTarget.value.viewId,
  activeACPAgentId.value,
  activeACPProjectPath.value,
  activeACPProjectMode.value,
]))
const acpConfigChanging = computed(() => acpConfigChangeScope.value === acpOperationScope.value)
function messageMatchesForkSource(message: ChatMessage): boolean {
  const forkMessageId = forkSource.value?.forkMessageId?.trim()
  if (!forkMessageId) return false
  const candidates = [
    message.serverId,
    message.id,
    message.role === 'system' ? undefined : message.externalMessageId,
  ]
  return candidates.some(candidate => candidate?.trim() === forkMessageId)
}

function showForkSourceDividerAfter(message: ChatMessage, index: number): boolean {
  return Boolean(forkSource.value)
    && index === forkSourceDividerAfterIndex.value
    && messages.value[index] === message
}

function showForkSourceDividerBefore(index: number): boolean {
  return Boolean(forkSource.value)
    && (!forkSource.value?.forkMessageId || forkSourceDividerAfterIndex.value === null)
    && index === 0
}

const activeSessionId = computed(() => paneTarget.value.sessionId ?? activeSession.value?.id ?? '')
const requestedSkills = ref<RequestedSkillSelection[]>([])
const slashPanelSuppressedPrefix = ref('')
const skillSlashEnabled = computed(() => !activeIsExternalAgent.value && !activeIsPendingExternalAgent.value)
const { data: safeSkillCatalog, isLoading: safeSkillCatalogLoading } = useQuery({
  key: () => ['bot-safe-skills-catalog', currentBotId.value ?? ''],
  query: () => fetchSafeSkillCatalog(currentBotId.value!),
  enabled: () => !!currentBotId.value && skillSlashEnabled.value,
  refetchOnWindowFocus: false,
})
const safeSkills = computed(() => skillSlashEnabled.value ? safeSkillCatalog.value ?? [] : [])

function requestedSkillKey(skill: Pick<RequestedSkillSelection, 'name'>): string {
  return skill.name.trim()
}

function addRequestedSkill(skill: RequestedSkillSelection) {
  if (!skillSlashEnabled.value) return
  const name = skill.name?.trim()
  if (!name) return
  const key = requestedSkillKey({ name })
  if (requestedSkills.value.some(item => requestedSkillKey(item) === key)) return
  requestedSkills.value = [...requestedSkills.value, {
    name,
    display_name: skill.display_name?.trim() || undefined,
    description: skill.description?.trim() || undefined,
    source_kind: skill.source_kind?.trim() || undefined,
    state: skill.state?.trim() || undefined,
  }]
  if (inputText.value.trimStart().startsWith('/')) {
    inputText.value = ''
    saveInputDraft(inputDraftKey.value, '')
  }
  clearCurrentCommandEvent()
  void nextTick(focusTextarea)
}

function removeRequestedSkill(skill: RequestedSkillSelection) {
  const key = requestedSkillKey(skill)
  requestedSkills.value = requestedSkills.value.filter(item => requestedSkillKey(item) !== key)
  void nextTick(focusTextarea)
}

watch([currentBotId, activeSessionId], () => {
  requestedSkills.value = []
  slashPanelSuppressedPrefix.value = ''
  clearCurrentCommandEvent()
})

watch(skillSlashEnabled, (enabled) => {
  if (enabled) return
  requestedSkills.value = []
  clearCurrentCommandEvent()
})

const slashQuickActions = computed(() => [
  {
    id: 'help',
    label: '/help',
    description: t('chat.slash.helpDescription'),
    icon: HelpCircle,
  },
  ...(skillSlashEnabled.value
    ? [{
        id: 'skill.list',
        label: '/skill list',
        description: t('chat.slash.skillListDescription'),
        icon: List,
      }]
    : []),
  {
    id: 'new',
    label: '/new',
    description: t('chat.slash.newDescription'),
    icon: SquarePen,
  },
  ...((boundLiveACPRuntime.value || activeIsPendingExternalAgent.value)
    && acpModes.value.length > 0
    ? [{
        id: 'permission',
        label: '/permission',
        description: t('chat.slash.permissionDescription'),
        icon: ShieldCheck,
      }]
    : []),
  ...(canCompactViaSlash.value
    ? [{
        id: 'compact',
        label: '/compact',
        description: sessionContextPercentKnown.value
          ? t('chat.slash.compactDescription', { percent: Math.round(sessionContextPercent.value) })
          : t('chat.slash.compactDescriptionNoStats'),
        icon: Minimize2,
      }]
    : []),
  ...(!activeIsExternalAgent.value && !activeIsPendingExternalAgent.value
    ? [{
        id: 'model',
        label: '/model',
        description: t('chat.slash.modelDescription', { model: selectedModelLabel.value }),
        icon: Package,
      }]
    : []),
])

const slashQuery = computed(() => {
  const text = inputText.value
  if (!text.trimStart().startsWith('/')) return ''
  const slashIndex = text.indexOf('/')
  return text.slice(slashIndex + 1).trim().toLowerCase()
})
const slashPanelOpen = computed(() =>
  isActive.value
  && !!currentBotId.value
  && !activeChatReadOnly.value
  && !loadingMessages.value
  && inputText.value.trimStart().startsWith('/')
  && !slashPanelSuppressedPrefix.value
  && !inputText.value.includes('\n'),
)
function slashMatches(label: string, description = ''): boolean {
  const query = slashQuery.value
  if (!query) return true
  const haystack = `${label} ${description}`.toLowerCase()
  return haystack.includes(query)
}
const visibleSlashQuickActions = computed(() =>
  slashQuickActions.value.filter(action => slashMatches(action.label, action.description)),
)
const visibleSlashSkills = computed(() =>
  safeSkills.value.filter(skill => slashMatches(skill.name, skill.description ?? '')),
)
const composerACPAvailableCommands = computed(() => (
  activeUsesACPRuntime.value && (boundLiveACPRuntime.value || activeIsPendingExternalAgent.value)
    ? acpAvailableCommands.value
    : []
))
const visibleACPAgentCommands = computed(() =>
  visibleACPSlashCommands(composerACPAvailableCommands.value, slashQuery.value),
)
const slashPanelHasResults = computed(() =>
  visibleSlashQuickActions.value.length > 0
  || visibleACPAgentCommands.value.length > 0
  || visibleSlashSkills.value.length > 0,
)

// Session usage for the /compact quick action's live description ("42% full")
// and its availability. Shares the query key with SessionInfoRing/panel, so
// this adds no extra fetch.
const {
  usedTokens: sessionUsedTokens,
  contextWindow: sessionContextWindow,
  contextPercent: sessionContextPercent,
  isCompacting: isCompactingSession,
  triggerCompact: triggerSessionCompact,
} = useSessionInfo({
  botId: computed(() => paneTarget.value.botId),
  sessionId: computed(() => paneTarget.value.sessionId),
  visible: isVisible,
  overrideModelId,
  fallbackContextWindow: computed(() => activeModel.value?.config?.context_window ?? null),
})
const sessionContextPercentKnown = computed(() => sessionContextWindow.value != null && sessionContextWindow.value > 0)
const canCompactViaSlash = computed(() =>
  !!activeSessionId.value && !activeIsExternalAgent.value && sessionUsedTokens.value > 0 && !isCompactingSession.value,
)

// Client-side quick actions run an existing UI affordance directly instead of
// round-tripping text through send: /compact triggers the session-info
// panel's compaction, /model opens the composer's model picker. Everything
// else keeps the type-and-send flow (the store intercepts /new; /help and
// /skill list execute server-side).
async function runPendingPermission(text: string) {
  const modeId = text.trim().replace(/^\/permission(?:\s+|$)/i, '').trim()
  try {
    const runtime = modeId ? await setACPMode(modeId) : await ensureACPRuntime()
    if (!runtime?.modes) return
    const currentModeId = runtime.modes.current_mode_id ?? ''
    chatStore.rememberCommandEvent({
      type: 'command_result',
      composer_scope: paneComposerScope.value,
      action_id: 'permission',
      terminal: true,
      result: {
        kind: modeId ? 'permission_mode_changed' : 'permission_modes',
        items: (runtime.modes.available_modes ?? []).flatMap((mode): CommandActionListItem[] => {
          const id = mode.id ?? ''
          if (!id) return []
          return [{
            id,
            title: mode.name || id,
            description: mode.description,
            kind: id === currentModeId ? 'acp_mode_current' : 'acp_mode',
          }]
        }),
      },
    }, currentPaneCommandScope())
  } catch (error) {
    composerError.value = resolveApiErrorMessage(error, t('chat.modeSwitchFailed'))
  }
}

function runLocalQuickAction(id: string, text = ''): boolean {
  if (id === 'compact') {
    if (!canCompactViaSlash.value) {
      composerError.value = t('chat.slash.compactUnavailable')
      return true
    }
    void triggerSessionCompact()
    return true
  }
  if (id === 'model') {
    modelPopoverOpen.value = true
    return true
  }
  if (id === 'permission' && activeIsPendingExternalAgent.value) {
    void runPendingPermission(text || '/permission')
    return true
  }
  return false
}

function localQuickActionBlocked(): boolean {
  if (pendingFiles.value.length > 0) {
    composerError.value = t('chat.slash.attachmentsUnsupported')
    return true
  }
  if (requestedSkills.value.length > 0) {
    composerError.value = t('chat.slash.errorMessages.invalid_skill_slash_syntax')
    return true
  }
  return false
}

function selectSlashQuickAction(action: { id: string, label: string }) {
  slashPanelSuppressedPrefix.value = ''
  if (localQuickActionBlocked()) return
  if (runLocalQuickAction(action.id, action.label)) {
    inputText.value = ''
    saveInputDraft(inputDraftKey.value, '')
    void nextTick(focusTextarea)
    return
  }
  sendSlashCommandText(action.label)
}

function sendSlashCommandText(text: string) {
  slashPanelSuppressedPrefix.value = ''
  inputText.value = text
  saveInputDraft(inputDraftKey.value, text)
  void nextTick(() => {
    focusTextarea()
    void handleSend()
  })
}

function selectACPAgentCommand(command: ACPAvailableCommand) {
  const text = acpSlashCommandComposerText(command)
  if (!text) return
  slashPanelSuppressedPrefix.value = text.trimEnd()
  inputText.value = text
  saveInputDraft(inputDraftKey.value, text)
  void nextTick(focusTextarea)
}

// Typed forms of the client-side quick actions ("/compact", "/model") — must
// be intercepted before the store send path, which would otherwise classify
// them as skill activation and fail with requested_skill_not_found.
function localQuickActionIDForSlash(text: string): string {
  if (activeIsPendingExternalAgent.value && /^\/permission(?:\s|$)/i.test(text.trim())) return 'permission'
  return composerLocalQuickActionID(
    text,
    activeIsExternalAgent.value || activeIsPendingExternalAgent.value,
  )
}

function currentPaneCommandScope() {
  const activeBotId = paneTarget.value.botId
  const renderedSessionId = paneTarget.value.sessionId ?? ''
  const paneScope = paneComposerScope.value
  return renderedSessionId
    ? { botId: activeBotId || undefined, sessionId: renderedSessionId, composerScope: paneScope }
    : { botId: activeBotId || undefined, composerScope: paneScope }
}

function clearCurrentCommandEvent() {
  chatStore.clearCommandEvent(currentPaneCommandScope())
}

const commandPanelEvent = computed(() => chatStore.commandEventForScope(currentPaneCommandScope()))
const commandResult = computed(() => commandPanelEvent.value?.type === 'command_result' ? commandPanelEvent.value.result : null)
const commandError = computed(() => commandPanelEvent.value?.type === 'command_error' ? commandPanelEvent.value.error : null)
const commandPanelActionID = computed(() => commandPanelEvent.value?.action_id?.trim() ?? '')
const commandPanelIsError = computed(() => !!commandError.value)
const presentedCommandResult = computed(() => commandResult.value
  ? commandResultPresentation(commandResult.value, {
      modesTitle: t('chat.slash.permissionModesTitle'),
      modesText: t('chat.slash.permissionModesText'),
      changedTitle: t('chat.slash.permissionModeChangedTitle'),
      changedText: t('chat.slash.permissionModeChangedText'),
      currentMode: t('chat.slash.permissionCurrentMode'),
    })
  : null)
const commandPanelTitle = computed(() => {
  if (commandError.value) return t('chat.slash.commandError')
  return presentedCommandResult.value?.title || t('chat.slash.commandResult')
})
function localizedCommandErrorMessage(error: CommandActionError): string {
  const code = error.code.trim()
  if (code) {
    const key = `chat.slash.errorMessages.${code}`
    const translated = t(key)
    if (translated !== key) return translated
  }
  return error.message || t('chat.slash.errorMessages.generic')
}

const commandPanelText = computed(() => commandError.value ? localizedCommandErrorMessage(commandError.value) : presentedCommandResult.value?.text || '')
const commandResultItems = computed(() =>
  (presentedCommandResult.value?.items ?? []).filter(item => isCommandResultItemVisible(item, commandPanelActionID.value)),
)

// Pre-digested view model for the panel's command section; the raw event and
// the filtered items stay local because the composer keyboard arbitration
// (Escape / arrows / Enter) reads them too.
const composerCommandPanel = computed(() => {
  if (!commandPanelEvent.value) return null
  return {
    isError: commandPanelIsError.value,
    title: commandPanelTitle.value,
    text: commandPanelText.value,
    items: commandResultItems.value,
  }
})

function selectCommandResultItem(item: CommandActionListItem) {
  const selection = resolveCommandResultSelection(item, commandPanelActionID.value)
  if (!selection) return
  if (selection.kind === 'quick_action') {
    clearCurrentCommandEvent()
    selectSlashQuickAction({
      id: selection.id,
      label: selection.text,
    })
    return
  }
  if (selection.kind === 'acp_permission') {
    clearCurrentCommandEvent()
    void onACPModeSelected(selection.modeId)
    return
  }
  if (!skillSlashEnabled.value) return
  addRequestedSkill({
    name: selection.id,
    display_name: selection.title,
    description: selection.description,
  })
}
const {
  runtime: acpCapabilityRuntime,
  availableCommands: acpAvailableCommands,
  modes: acpModes,
  currentModeId: currentACPModeId,
  models: acpModels,
  currentModelId: currentACPModelId,
  reasoningEfforts: acpReasoningEfforts,
  currentReasoningEffort: currentACPReasoningEffort,
  isEnsuring: acpRuntimeEnsuring,
  isPreparing: acpConfigPreparing,
  ensure: ensureACPRuntime,
  setMode: setACPMode,
  setModel: setACPModel,
  setReasoning: setACPReasoning,
} = useACPRuntime({
  target: paneTarget,
  pending: activeIsPendingExternalAgent,
  enabled: computed(() => activeUsesACPRuntime.value && !!currentBotId.value),
  agentId: activeACPAgentId,
  projectPath: activeACPProjectPath,
})
const boundLiveACPRuntime = computed(() => {
  return activeIsExternalAgent.value
    && !activeIsPendingExternalAgent.value
    && isBoundACPRuntimeForTarget(acpCapabilityRuntime.value, {
      sessionId: paneTarget.value.sessionId ?? '',
      agentId: activeACPAgentId.value,
      projectPath: activeACPProjectPath.value,
  })
})

const acpModelsLoading = computed(() =>
  activeUsesACPRuntime.value
  && !acpCapabilityRuntime.value?.models
  && (agentChanging.value || acpRuntimeEnsuring.value),
)
const {
  catalog: composerModelCatalog,
  nativeModels: models,
  isLoading: composerModelsLoading,
  error: composerModelCatalogError,
  refresh: refreshComposerModelCatalog,
} = useAgentModelCatalog({
  botId: currentBotId,
  botAgentId: activeBotAgentID,
  runtime: computed(() => activeChatTarget.value.runtimeType),
  selectedModelId: overrideModelId,
  acpModels,
  acpCurrentModelId: currentACPModelId,
  acpReasoningEfforts,
  acpCurrentReasoningEffort: currentACPReasoningEffort,
  acpLoading: computed(() => acpModelsLoading.value || acpConfigPreparing.value),
  refreshACP: () => refreshACPComposerConfig(),
})

const directRuntimeAuthRequired = computed(() =>
  !!activeDirectRuntime.value
  && (
    isApiErrorCode(composerModelCatalogError.value, 'external_runtime.auth_required')
    || isApiErrorCode(composerModelCatalogError.value, 'agent_credential.not_found')
    || isApiErrorCode(composerModelCatalogError.value, 'agent_credential.reauthorization_required')
  ),
)
const directModelCatalogError = computed(() => {
  if (!activeUsesDirectRuntime.value || !composerModelCatalogError.value) return ''
  return resolveApiErrorMessage(composerModelCatalogError.value, t('bots.agent.modelsLoadFailed'))
})

const composerConfigPending = computed(() => activeUsesExternalAgentComposer.value && (
  agentChanging.value || (activeUsesACPRuntime.value && (acpConfigChanging.value || acpConfigPreparing.value))
))
const canChangeAgent = computed(() => !streaming.value
  && !creatingSession.value
  && !composerConfigPending.value
  && messages.value.length === 0)

const composerModels = computed(() => composerModelCatalog.value.models)
const composerModelProviders = computed(() => composerModelCatalog.value.providers)

// "Default" alone tells the user nothing — resolve what it actually means:
// the runtime's configured model for direct runtimes, the bot's chat model
// for the native composer. Falls back to the bare label while the catalog is
// still loading (or, for Claude Code, when the runtime keeps its default to
// itself).
const composerDefaultModelName = computed(() => {
  let id = ''
  if (activeUsesDirectRuntime.value) {
    id = composerModelCatalog.value.configuredModelId || composerModelCatalog.value.defaultModelId
  } else if (!activeUsesExternalAgentComposer.value) {
    id = botSettings.value?.chat_model_id ?? ''
  }
  id = id.trim()
  if (!id) return ''
  const model = composerModels.value.find(m => m.id === id || m.model_id === id)
  return model?.name || model?.model_id || id
})
const composerDefaultModelLabel = computed(() =>
  composerDefaultModelName.value
    ? t('chat.modelDefaultNamed', { model: composerDefaultModelName.value })
    : t('chat.modelDefault'))
const composerReasoningOptions = computed(() => {
  const efforts = composerModelCatalog.value.reasoningEfforts
  if (!efforts) return undefined
  return efforts.flatMap((effort) => {
    const value = effort.id?.trim() ?? ''
    if (!value) return []
    const runtimeLabel = effort.name?.trim() ?? ''
    const translatedLabel = EFFORT_LABELS[value] ? t(EFFORT_LABELS[value]) : value
    return [{
      value,
      label: runtimeLabel && runtimeLabel !== value ? runtimeLabel : translatedLabel,
      description: effort.description?.trim() || undefined,
    }]
  })
})

function openDirectAgentSettings() {
  const botName = currentBot.value?.name || currentBot.value?.id || currentBotId.value
  if (!botName) return
  modelPopoverOpen.value = false
  void router.push({
    name: 'bot-detail',
    params: { botName },
    query: { tab: 'agents' },
  })
}

function retryDirectModelCatalog() {
  void refreshComposerModelCatalog()
}

const activeModel = computed(() => {
  const id = overrideModelId.value || botSettings.value?.chat_model_id || ''
  return models.value.find((m) => m.id === id)
})

// PDFs reach the model natively only when it carries the file-input
// capability; without it the file lands in the workspace as a path the model
// cannot open. Warn at attach time so the user is not surprised mid-turn.
// External Agent sessions are exempt — Claude Code / Codex read PDFs themselves.
const isPdfFile = (file: File) =>
  file.type === 'application/pdf' || file.name.toLowerCase().endsWith('.pdf')

// Mirrors the backend's nativeAttachmentMaxBinaryBytes: larger PDFs are demoted
// to the workspace-path fallback even when the model supports file-input.
const nativePdfMaxBytes = 12 * 1024 * 1024

watch(() => pendingFiles.value.length, (len, prevLen) => {
  if (len <= (prevLen ?? 0)) return
  if (activeUsesExternalAgentComposer.value) return
  const model = activeModel.value
  if (!model) return
  const added = pendingFiles.value.slice(prevLen ?? 0)
  if (!model.config?.compatibilities?.includes('file-input')) {
    if (added.some(isPdfFile)) {
      toast.warning(t('chat.pdfUnsupportedByModel'))
    }
    return
  }
  if (added.some((file) => isPdfFile(file) && file.size > nativePdfMaxBytes)) {
    toast.warning(t('chat.pdfTooLargeForNative'))
  }
})

// Dropping files on the conversation is the Paperclip by another route: they
// land in the same pending tray, so the attachment cards, previews, and the PDF
// warning above all follow with no extra wiring. Nothing is sent — the user
// still writes the message that goes with the files.
async function handleFilesDrop(transfer: DataTransfer) {
  const { files, skippedFolders } = await readDroppedFiles(transfer)
  for (const file of files) pendingFiles.value.push(file)
  // An attachment is one file, so a folder has nothing to become here. Warned
  // even when loose files DID land in the same drop: the cards would otherwise
  // read as "everything arrived" while the folder vanished silently.
  if (skippedFolders > 0) {
    toast.warning(t('chat.dropFolderUnsupported'))
  }
}

// Same conditions that disable the Paperclip: no bot, a read-only or streaming
// turn, history still loading. A disabled zone keeps the overlay dark, so the
// OS no-drop cursor answers instead of a drop that goes nowhere.
const fileDropDisabled = () => !currentBotId.value || activeChatReadOnly.value || streaming.value || loadingMessages.value

// Root element, exposed through the drop-target registry so the page-level base
// zone can anchor its overlay over THIS pane (a global drag points at the
// composer it will land in) instead of floating a third, window-centred anchor.
const rootEl = useTemplateRef<HTMLElement>('rootEl')

const { active: dropActive, bounds: dropBounds, handlers: dropHandlers } = useFileDropZone({
  disabled: fileDropDisabled,
  onDrop: transfer => void handleFilesDrop(transfer),
})

// While this pane is the focused dock panel it is also the page-level target:
// files dropped outside every region zone (e.g. the sidebar on a non-Files
// view) are forwarded by the base zone in main-section into THIS composer's
// tray. Cleanup runs on blur and on unmount (watcher stop), and the registry's
// identity guard makes focus handoff between splits order-safe.
watch(isActive, (focused) => {
  if (!focused) return
  onWatcherCleanup(registerChatFileDropTarget({
    onDrop: transfer => void handleFilesDrop(transfer),
    disabled: fileDropDisabled,
    hostEl: () => rootEl.value,
  }))
}, { immediate: true })

type DefaultExternalAgentSettings = {
  default_bot_agent_id?: string
  chat_runtime?: string
  chat_acp_agent_id?: string
  chat_acp_project_path?: string
  chat_acp_project_mode?: string
}

type DefaultExternalAgentAvailability = {
  input: ExternalAgentSessionInput | null
  messageKey: string
  loading: boolean
}

const defaultExternalAgentAvailability = computed<DefaultExternalAgentAvailability>(() => {
  const settings = botSettings.value as (DefaultExternalAgentSettings | undefined)
  if (!settings) {
    return { input: null, messageKey: '', loading: !!currentBotId.value && botSettingsLoading.value }
  }
  if (settings.chat_runtime !== 'acp_agent' && settings.chat_runtime !== 'codex' && settings.chat_runtime !== 'claude-code') return { input: null, messageKey: '', loading: false }
  if (!hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_exec')) {
    return { input: null, messageKey: 'chat.defaultAgentNoWorkspaceExec', loading: false }
  }
  const botAgentId = settings.default_bot_agent_id?.trim() ?? ''
  if (!botAgentId) return { input: null, messageKey: 'chat.defaultAgentMissing', loading: false }
  if (!botAgentData.value) {
    return {
      input: null,
      messageKey: botAgentsLoading.value ? 'chat.defaultExternalAgentLoading' : 'chat.defaultAgentUnavailable',
      loading: botAgentsLoading.value,
    }
  }
  const agent = botAgents.value.find(item => item.id === botAgentId)
  if (!agent || agent.enabled === false) {
    return { input: null, messageKey: 'chat.defaultAgentDisabled', loading: false }
  }
  const agentId = botAgentProvider(agent)
  const directConfigured = isDirectBotAgentConfigured(agent)
  if (directConfigured === false) {
    return { input: null, messageKey: 'chat.defaultAgentNotConfigured', loading: false }
  }
  if (directConfigured === null) {
    if (!acpProfileData.value) {
      return {
        input: null,
        messageKey: acpProfilesLoading.value ? 'chat.defaultExternalAgentLoading' : 'chat.defaultAgentUnavailable',
        loading: acpProfilesLoading.value,
      }
    }
    const profile = acpProfiles.value.find(item => normalizeACPAgentID(item.id) === agentId)
    if (!profile) return { input: null, messageKey: 'chat.defaultAgentUnavailable', loading: false }
    const config = readACPAgentConfig(currentBotMetadata.value, agentId)
    if (config.setupModeSet && findMissingRequiredManagedField(profile, config.managed, config.setupMode)) {
      return { input: null, messageKey: 'chat.defaultAgentNotConfigured', loading: false }
    }
  }
  return {
    input: {
      botAgentId,
      runtime: normalizeBotAgentRuntime(agent.runtime) || BOT_AGENT_RUNTIME_ACP,
      agentId,
      projectPath: settings.chat_acp_project_path?.trim() || ACP_DEFAULT_PROJECT_PATH,
      projectMode: settings.chat_acp_project_mode?.trim() || ACP_DEFAULT_PROJECT_MODE,
    },
    messageKey: '',
    loading: false,
  }
})
const defaultExternalAgentSessionInput = computed(() => defaultExternalAgentAvailability.value.input)
const defaultExternalAgentUnavailableMessage = computed(() =>
  defaultExternalAgentAvailability.value.messageKey ? t(defaultExternalAgentAvailability.value.messageKey) : '',
)
const defaultExternalAgentLoading = computed(() => defaultExternalAgentAvailability.value.loading)
const defaultExternalAgentComposerError = ref('')

function clearDefaultExternalAgentComposerError() {
  if (defaultExternalAgentComposerError.value && composerError.value === defaultExternalAgentComposerError.value) {
    composerError.value = ''
  }
  defaultExternalAgentComposerError.value = ''
}

const activeModelReasoning = computed(() => activeModel.value?.reasoning)

const activeModelSupportsReasoning = computed(() => activeModelReasoning.value?.supported === true)

// A native composer with no chat model cannot answer, so the trigger says so
// ("None") instead of the old "Default" placeholder, which named a model that
// does not exist.
const composerHasNoModel = computed(() =>
  hasNoComposerModel(activeUsesExternalAgentComposer.value, overrideModelId.value),
)

const selectedModelLabel = computed(() => {
  const current = composerModels.value.find(model => model.id === overrideModelId.value)
  if (current?.name || current?.model_id) return current.name || current.model_id
  // A configured-but-missing id still shows the raw id rather than "None": the
  // model list can lag behind settings, and a transient gap must not read as
  // "unconfigured".
  if (overrideModelId.value) return overrideModelId.value
  return composerHasNoModel.value ? t('common.none') : composerDefaultModelLabel.value
})

const selectedReasoningLabel = computed(() => {
  if (activeUsesExternalAgentComposer.value) {
    const current = overrideReasoningEffort.value
    return composerReasoningOptions.value?.find(option => option.value === current)?.label || current
  }
  const v = overrideReasoningEffort.value
  return t(EFFORT_LABELS[v] ?? 'chat.modelDefault')
})

const reasoningActive = computed(() =>
  activeUsesExternalAgentComposer.value
    ? Boolean(
        overrideReasoningEffort.value
        && composerReasoningOptions.value?.some(option => option.value === overrideReasoningEffort.value),
      )
    : activeModelSupportsReasoning.value
      && Boolean(overrideReasoningEffort.value)
      && overrideReasoningEffort.value !== REASONING_EFFORT_DISABLE,
)

const modelTriggerLabel = computed(() =>
  reasoningActive.value
    ? `${selectedModelLabel.value} · ${selectedReasoningLabel.value}`
    : selectedModelLabel.value,
)

// A subagent runs on the model it was pinned to when it was spawned, recorded
// on its session at creation. The composer has to open on that model: it sends
// model_id with every message, so defaulting to the bot's chat model would move
// the agent onto another model the moment a human talks to it — silently, since
// the picker would still read as "the default".
const pinnedSubagentModelId = computed(() => resolvePinnedSubagentModelId(
  activeSession.value?.type,
  activeSessionMetadata.value,
  models.value
    .map(model => model.id)
    .filter((id): id is string => !!id),
))

function initFromBotSettings() {
  if (activeUsesExternalAgentComposer.value || !botSettings.value) return
  if (!overrideModelId.value) {
    overrideModelId.value = pinnedSubagentModelId.value || botSettings.value.chat_model_id || ''
  }
  if (!overrideReasoningEffort.value) {
    // reasoning_effort is the bot's whole reasoning decision now, including
    // "disable"; an empty value only means settings have not loaded a tier yet,
    // which resolves to medium the same way the backend does.
    overrideReasoningEffort.value = botSettings.value.reasoning_effort || 'medium'
  }
}

watch([botSettings, activeUsesExternalAgentComposer], () => initFromBotSettings(), { immediate: true })

// The session summary and the model list are both fetched, so the pinned model
// routinely lands after bot settings already seeded the default. Adopt it then
// too — but never over a model the user picked themselves.
watch(pinnedSubagentModelId, (pinned, previous) => {
  if (userPickedModel.value || activeUsesExternalAgentComposer.value) return
  if (pinned) {
    overrideModelId.value = pinned
    return
  }
  // Repointed off a subagent: hand the composer back to the bot's own default
  // rather than leaving the agent's pinned model selected for a plain chat.
  if (previous) overrideModelId.value = botSettings.value?.chat_model_id ?? ''
}, { immediate: true })

// Switching models can strand the composer's override on a tier the new model
// does not offer. An empty override is left alone: it means "inherit the bot's
// setting", not a stranded value.
watch(activeModelReasoning, (options) => {
  if (activeUsesExternalAgentComposer.value) return
  const current = overrideReasoningEffort.value
  if (!current || !options?.supported) return
  const next = reconcileStoredEffort(current, options)
  if (next && next !== current) overrideReasoningEffort.value = next
}, { immediate: true })

watch(currentBotId, () => {
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
  userPickedModel.value = false
})

// A pane can be repointed at another session without remounting, and the model
// a user picked belongs to the session they picked it in — clear the flag so the
// next session's pinned model can still seed the composer.
watch(() => paneTarget.value.sessionId, () => {
  userPickedModel.value = false
})

watch(activeUsesExternalAgentComposer, (usesExternalAgent, previouslyUsedExternalAgent) => {
  if (usesExternalAgent === previouslyUsedExternalAgent) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
  userPickedModel.value = false
  if (!usesExternalAgent) initFromBotSettings()
})

watch(activeACPAgentId, (agentID, previousAgentID) => {
  if (!activeUsesExternalAgentComposer.value || !previousAgentID || agentID === previousAgentID) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
})

const directSessionIdentity = computed(() => JSON.stringify([
  paneTarget.value.botId,
  paneTarget.value.sessionId,
  activeBotAgentID.value,
  activeDirectRuntime.value,
]))
watch(directSessionIdentity, (identity, previousIdentity) => {
  if (!activeUsesDirectRuntime.value || identity === previousIdentity) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
  userPickedModel.value = false
})

// ACP overrides describe one runtime. An ephemeral pane is repointed to a
// different session without remounting, so without this reset the previous
// session's selection would be pushed onto the next session's runtime by the
// scope watcher below (registration order guarantees this reset runs first)
// and by per-turn sends. Reconcile re-seeds the cleared values from the new
// runtime's own current state.
const acpSessionIdentity = computed(() => JSON.stringify([
  paneTarget.value.botId,
  paneTarget.value.sessionId,
  activeACPAgentId.value,
  activeACPProjectPath.value,
  activeACPProjectMode.value,
]))
watch(acpSessionIdentity, (identity, previousIdentity) => {
  if (!activeUsesACPRuntime.value || identity === previousIdentity) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
})

function reconcileACPComposerConfig() {
  const runtime = acpCapabilityRuntime.value
  if (!activeUsesACPRuntime.value || !runtime) return

  if (runtime.models !== undefined) {
    const availableModels = new Set(
      acpModels.value.map(model => model.id?.trim() ?? '').filter(Boolean),
    )
    const selectedModel = overrideModelId.value.trim()
    if (!selectedModel || !availableModels.has(selectedModel)) {
      const currentModel = currentACPModelId.value.trim()
      overrideModelId.value = availableModels.has(currentModel) ? currentModel : ''
    }
  }

  if (runtime.reasoning !== undefined) {
    const availableEfforts = new Set(
      acpReasoningEfforts.value.map(effort => effort.id?.trim() ?? '').filter(Boolean),
    )
    const selectedEffort = overrideReasoningEffort.value.trim()
    if (!selectedEffort || !availableEfforts.has(selectedEffort)) {
      const currentEffort = currentACPReasoningEffort.value.trim()
      overrideReasoningEffort.value = availableEfforts.has(currentEffort) ? currentEffort : ''
    }
  } else {
    overrideReasoningEffort.value = ''
  }
}

watch(acpCapabilityRuntime, () => {
  if (!acpConfigPreparing.value) reconcileACPComposerConfig()
}, { immediate: true })

watch(
  () => activeUsesACPRuntime.value && isVisible.value ? acpOperationScope.value : '',
  (scope) => {
    if (!scope || !activeACPAgentId.value) return
    void refreshACPComposerConfig().catch((error) => {
      composerError.value = resolveApiErrorMessage(error, t('chat.agentSwitchFailed'))
    })
  },
  { immediate: true },
)

async function refreshACPComposerConfig(): Promise<void> {
  if (!activeUsesACPRuntime.value) return
  const desiredModelId = overrideModelId.value.trim()
  const runtime = await ensureACPRuntime(true, desiredModelId)
  if (!runtime || !activeUsesACPRuntime.value) return
  reconcileACPComposerConfig()
}

async function refreshACPComposerConfigAfterSelectionError(result: SendMessageResult): Promise<void> {
  if (!shouldRefreshACPComposerConfig(result, activeUsesACPRuntime.value)) return

  const operationScope = acpOperationScope.value
  acpConfigChangeScope.value = operationScope
  try {
    await refreshACPComposerConfig()
  } catch {
    // Preserve the original selection error. This refresh is best-effort
    // recovery so a secondary failure must not replace the actionable cause.
  } finally {
    if (acpConfigChangeScope.value === operationScope) acpConfigChangeScope.value = ''
  }
}

function pendingMatchesDefaultExternalAgent(input: ExternalAgentSessionInput): boolean {
  return activeChatTarget.value.kind === 'draft-external-agent'
    && chatStore.pendingExternalAgentMatchesInput(input, paneTarget.value)
}

watch([defaultExternalAgentUnavailableMessage, defaultExternalAgentLoading, currentBotId, hasExplicitSessionSelection, isActive], ([message, loading, _bot, _explicit, focused]) => {
  if (!focused) return
  clearDefaultExternalAgentComposerError()
  if (!message || !currentBotId.value) return
  if (hasExplicitSessionSelection.value) return
  if (!loading) {
    chatStore.resetToEmptyComposer({}, paneTarget.value)
  }
  defaultExternalAgentComposerError.value = message
  composerError.value = message
}, { immediate: true })

watch([defaultExternalAgentSessionInput, defaultExternalAgentLoading, currentBotId, hasExplicitSessionSelection, activeChatTarget, isActive], ([input, loading, _bot, _explicit, _target, focused]) => {
  if (!focused) return
  if (!currentBotId.value) return
  if (!input) {
    if (!loading) {
      chatStore.cacheDefaultExternalAgentSession(null)
    }
    if (!loading && !hasExplicitSessionSelection.value && activeIsPendingExternalAgent.value) {
      chatStore.resetToEmptyComposer({}, paneTarget.value)
    }
    return
  }
  chatStore.cacheDefaultExternalAgentSession(input)
  if (hasExplicitSessionSelection.value) return
  clearDefaultExternalAgentComposerError()
  if (pendingMatchesDefaultExternalAgent(input)) return
  chatStore.stageDefaultExternalAgentSession(input, paneTarget.value)
}, { immediate: true })

watch([modelPopoverOpen, activeUsesACPRuntime, acpOperationScope], ([open, usesExternalAgent]) => {
  if (!open || !usesExternalAgent) return
  void refreshACPComposerConfig().catch((error) => {
    composerError.value = resolveApiErrorMessage(error, t('chat.agentSwitchFailed'))
  })
})

watch([slashPanelOpen, activeUsesACPRuntime, acpOperationScope], ([open, usesExternalAgent]) => {
  if (!open || !usesExternalAgent) return
  void ensureACPRuntime().catch((error) => {
    composerError.value = resolveApiErrorMessage(error, t('chat.agentSwitchFailed'))
  })
})

// Starting an ACP runtime (spawning the agent process + protocol handshake) has
// no server-side deadline, so a wedged agent would leave the composer spinning
// indefinitely — the user's only escape was a full page reload. Bound the switch
// on the client so the controls re-enable and a retry hint surfaces instead.
const AGENT_SWITCH_TIMEOUT_MS = 30_000

class AgentSwitchTimeout extends Error {}

function withAgentSwitchTimeout<T>(work: Promise<T>): Promise<T> {
  // Keep a detached handler so a late settle (after the race is decided) never
  // bubbles up as an unhandled rejection.
  void work.catch(() => {})
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new AgentSwitchTimeout()), AGENT_SWITCH_TIMEOUT_MS)
    work.then(
      (value) => { clearTimeout(timer); resolve(value) },
      (error) => { clearTimeout(timer); reject(error) },
    )
  })
}

function agentSwitchErrorMessage(error: unknown): string {
  return error instanceof AgentSwitchTimeout
    ? t('chat.agentSwitchTimeout')
    : resolveApiErrorMessage(error, t('chat.agentSwitchFailed'))
}

async function selectBotAgent(agent: BotagentsBotAgent) {
  const botAgentId = agent.id?.trim() ?? ''
  const agentId = botAgentProvider(agent)
  const runtime = normalizeBotAgentRuntime(agent.runtime) || BOT_AGENT_RUNTIME_ACP
  if (!botAgentId || !agentId || agentChanging.value || !canChangeAgent.value) return
  agentPopoverOpen.value = false
  if (activeUsesExternalAgentComposer.value && botAgentId === activeBotAgentID.value) return
  agentChanging.value = true
  composerError.value = ''
  try {
    if (paneTarget.value.sessionId) {
      await withAgentSwitchTimeout(chatStore.updateCurrentSessionAgent({
        botAgentId,
        runtime,
        agentId,
      }, paneTarget.value))
    } else {
      chatStore.stageExternalAgentSession({
        botAgentId,
        runtime,
        agentId,
      }, {}, paneTarget.value)
      if (runtime === BOT_AGENT_RUNTIME_ACP) {
        await withAgentSwitchTimeout(chatStore.ensurePendingACPRuntime(paneTarget.value))
      }
    }
  } catch (error) {
    composerError.value = agentSwitchErrorMessage(error)
  } finally {
    agentChanging.value = false
  }
}

async function selectMemohAgent() {
  if (agentChanging.value || !canChangeAgent.value) return
  agentPopoverOpen.value = false
  if (!paneTarget.value.sessionId) {
    chatStore.resetToEmptyComposer({ explicitSelection: true }, paneTarget.value)
    clearDefaultExternalAgentComposerError()
    composerError.value = ''
    pendingFiles.value = []
    return
  }
  if (!activeIsExternalAgent.value) return
  agentChanging.value = true
  composerError.value = ''
  try {
    await withAgentSwitchTimeout(chatStore.updateCurrentSessionToMemoh(paneTarget.value))
  } catch (error) {
    composerError.value = agentSwitchErrorMessage(error)
  } finally {
    agentChanging.value = false
  }
}

function onModelSelected() {
  // The popover stays open on selection (#899) — dismissal is outside click /
  // Esc. Here we only sanitise the effort when the new model can't reason.
  if (!activeModelSupportsReasoning.value) {
    overrideReasoningEffort.value = REASONING_EFFORT_DISABLE
  }
}

function reconcileDirectReasoningEffort() {
  if (!activeUsesDirectRuntime.value) return
  const available = new Set((composerReasoningOptions.value ?? []).map(option => option.value))
  if (overrideReasoningEffort.value && available.has(overrideReasoningEffort.value)) return
  const configured = composerModelCatalog.value.configuredReasoningEffort
  const fallback = composerModelCatalog.value.defaultReasoningEffort
  overrideReasoningEffort.value = available.has(configured)
    ? configured
    : available.has(fallback) ? fallback : ''
}

async function onComposerModelValueSelected(value: string) {
  if (activeUsesACPRuntime.value && acpConfigChanging.value) return
  const previousModel = overrideModelId.value
  const previousReasoningEffort = overrideReasoningEffort.value
  userPickedModel.value = true
  overrideModelId.value = value
  if (!activeUsesExternalAgentComposer.value) {
    onModelSelected()
    return
  }
  if (activeUsesDirectRuntime.value) {
    reconcileDirectReasoningEffort()
    return
  }

  const modelId = value.trim()
  if (!modelId) {
    overrideModelId.value = previousModel
    return
  }
  const operationScope = acpOperationScope.value
  acpConfigChangeScope.value = operationScope
  composerError.value = ''
  try {
    const runtime = await setACPModel(modelId)
    if (runtime && acpOperationScope.value === operationScope) reconcileACPComposerConfig()
  } catch (error) {
    if (
      activeUsesExternalAgentComposer.value
      && acpOperationScope.value === operationScope
      && overrideModelId.value === value
    ) {
      overrideModelId.value = previousModel
      overrideReasoningEffort.value = previousReasoningEffort
      composerError.value = resolveApiErrorMessage(error, t('chat.modelSwitchFailed'))
    }
  } finally {
    if (acpConfigChangeScope.value === operationScope) acpConfigChangeScope.value = ''
  }
}

async function onACPModeSelected(value: unknown) {
  if (typeof value !== 'string') return
  if (acpConfigChanging.value) return
  if (!value || value === currentACPModeId.value) return
  const previousMode = currentACPModeId.value
  const operationScope = acpOperationScope.value
  acpConfigChangeScope.value = operationScope
  composerError.value = ''
  try {
    const runtime = await setACPMode(value)
    if (
      runtime
      && acpOperationScope.value === operationScope
      && runtime.modes?.current_mode_id !== previousMode
    ) {
      toast.warning(t('chat.sessionModeChanged'))
    }
  } catch (error) {
    if (activeUsesExternalAgentComposer.value && acpOperationScope.value === operationScope) {
      composerError.value = resolveApiErrorMessage(error, t('chat.modeSwitchFailed'))
    }
  } finally {
    if (acpConfigChangeScope.value === operationScope) acpConfigChangeScope.value = ''
  }
}

async function onComposerReasoningEffortSelected(value: string) {
  if (activeUsesACPRuntime.value && acpConfigChanging.value) return
  const previousEffort = overrideReasoningEffort.value
  overrideReasoningEffort.value = value
  if (!activeUsesExternalAgentComposer.value) return
  if (activeUsesDirectRuntime.value) return

  const effort = value.trim()
  if (!effort) {
    overrideReasoningEffort.value = previousEffort
    return
  }
  const operationScope = acpOperationScope.value
  acpConfigChangeScope.value = operationScope
  composerError.value = ''
  try {
    const runtime = await setACPReasoning(effort)
    if (runtime && acpOperationScope.value === operationScope) reconcileACPComposerConfig()
  } catch (error) {
    if (
      activeUsesExternalAgentComposer.value
      && acpOperationScope.value === operationScope
      && overrideReasoningEffort.value === value
    ) {
      overrideReasoningEffort.value = previousEffort
      composerError.value = resolveApiErrorMessage(error, t('chat.reasoningSwitchFailed'))
    }
  } finally {
    if (acpConfigChangeScope.value === operationScope) acpConfigChangeScope.value = ''
  }
}

const {
  items: galleryItems,
  openIndex: galleryOpenIndex,
  setOpenIndex: gallerySetOpenIndex,
  openBySrc: galleryOpenBySrc,
} = useMediaGallery(messages)

const inputText = ref('')
watch(inputText, (text) => {
  const prefix = slashPanelSuppressedPrefix.value
  if (!prefix || text === prefix || text.startsWith(`${prefix} `)) return
  slashPanelSuppressedPrefix.value = ''
})
// Mirror of ComposerContinueOn's pill rule: only an explicit non-default
// selection expands the trigger (unset — including the pre-load window —
// renders the collapsed default circle; a missing/ghost selection resolves to
// null here exactly like the child's selectedTarget). The reservation must
// track which width the control is actually rendering, and on mobile the
// trigger never expands (see the child's header comment).
const isMobileShell = useIsMobile()
const continueOnExpanded = computed(() => (
  !!selectedWorkspaceTargetId.value
  && selectedWorkspaceTarget.value?.kind !== 'native'
  && !isMobileShell.value
))

const {
  textareaEl,
  composerEl,
  focusTextarea,
  modelTriggerMaxWidth,
} = useComposerLayout({
  continueOnVisible: showComputersMenu,
  continueOnExpanded,
})

const showSend = computed(() => Boolean(inputText.value.trim()) || pendingFiles.value.length > 0 || requestedSkills.value.length > 0)

// Whether the trailing slot shows the send button (vs. mic — see micVisible
// just below, its exact complement). Streaming always wins the slot for stop,
// same as before; unlike the old ring-era rule this no longer special-cases
// ACP, because mic — not a dimmed disabled send — is what now fills the slot
// on empty input in EVERY mode.
const sendButtonVisible = computed(() => showSend.value || streaming.value)

// Mic owns the trailing slot whenever send doesn't: nothing to send is
// exactly when voice input is the useful affordance there. Exact complement
// of sendButtonVisible so the two can never both show (or both hide).
const micVisible = computed(() => !sendButtonVisible.value)

// Voice input: MediaRecorder → the bot's configured transcription model →
// transcript appended into the draft. The recorder/stream live outside
// reactivity (plain module lets) because MediaRecorder is stateful and must
// never be proxied. voiceRequestVersion + voiceSourceBotId guard the async
// edges: a bot switch or cancel mid-record/mid-transcribe invalidates the
// in-flight request so a late transcript can't land in the wrong pane.
type VoiceInputState = 'idle' | 'recording' | 'transcribing'

const voiceInputState = ref<VoiceInputState>('idle')
const voiceInputLabel = computed(() => {
  if (voiceInputState.value === 'recording') return t('chat.voiceInput.stop')
  if (voiceInputState.value === 'transcribing') return t('chat.voiceInput.transcribing')
  return t('chat.voiceInput.start')
})
const voiceInputDisabled = computed(() =>
  !currentBotId.value
  || activeChatReadOnly.value
  || loadingMessages.value
  || streaming.value
  || botSettingsLoading.value
  || voiceInputState.value === 'transcribing',
)

let voiceRecorder: MediaRecorder | null = null
let voiceStream: MediaStream | null = null
let voiceChunks: Blob[] = []
let discardVoiceRecording = false
let voiceSourceBotId = ''
let voiceRequestVersion = 0

function releaseVoiceStream() {
  voiceStream?.getTracks().forEach(track => track.stop())
  voiceStream = null
}

function preferredVoiceMimeType(): string {
  if (typeof MediaRecorder === 'undefined') return ''
  const candidates = [
    'audio/webm;codecs=opus',
    'audio/webm',
    'audio/mp4',
    'audio/ogg;codecs=opus',
  ]
  return candidates.find(type => MediaRecorder.isTypeSupported(type)) ?? ''
}

function voiceFileExtension(mimeType: string): string {
  if (mimeType.includes('mp4')) return 'm4a'
  if (mimeType.includes('ogg')) return 'ogg'
  return 'webm'
}

function openTranscriptionSettings() {
  const botName = currentBot.value?.name || currentBot.value?.id || currentBotId.value
  if (!botName) {
    void router.push({ name: 'voice' })
    return
  }
  void router.push({
    name: 'bot-detail',
    params: { botName },
    query: { tab: 'general', section: 'multimedia' },
  })
}

async function transcribeVoiceInput(
  blob: Blob,
  mimeType: string,
  modelId: string,
  sourceBotId: string,
  requestVersion: number,
) {
  voiceInputState.value = 'transcribing'
  const file = new File(
    [blob],
    `voice-input.${voiceFileExtension(mimeType)}`,
    { type: mimeType || 'audio/webm' },
  )

  try {
    const { data } = await postTranscriptionModelsByIdTest({
      path: { id: modelId },
      body: { file },
      throwOnError: true,
    })
    if (voiceRequestVersion !== requestVersion || currentBotId.value !== sourceBotId) return
    const transcript = data?.text?.trim() ?? ''
    if (!transcript) {
      toast.error(t('chat.voiceInput.empty'))
      return
    }
    const draft = inputText.value.trimEnd()
    inputText.value = draft ? `${draft} ${transcript}` : transcript
    await nextTick()
    focusTextarea()
  } catch (error) {
    if (voiceRequestVersion !== requestVersion) return
    toast.error(resolveApiErrorMessage(error, t('chat.voiceInput.failed')))
  } finally {
    if (voiceRequestVersion === requestVersion) voiceInputState.value = 'idle'
  }
}

function stopVoiceInput() {
  if (voiceRecorder?.state !== 'recording') return
  voiceRecorder.stop()
}

function cancelVoiceInput() {
  voiceRequestVersion += 1
  discardVoiceRecording = true
  if (voiceRecorder?.state === 'recording') {
    voiceRecorder.stop()
  } else {
    voiceRecorder = null
    voiceChunks = []
    releaseVoiceStream()
    voiceInputState.value = 'idle'
  }
}

async function startVoiceInput() {
  const modelId = botSettings.value?.transcription_model_id?.trim() ?? ''
  if (!modelId) {
    toast.info(t('chat.voiceInput.notConfigured'))
    openTranscriptionSettings()
    return
  }
  if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') {
    toast.error(t('chat.voiceInput.unsupported'))
    return
  }

  const requestVersion = ++voiceRequestVersion
  try {
    const stream = await navigator.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
      },
    })
    if (voiceRequestVersion !== requestVersion) {
      stream.getTracks().forEach(track => track.stop())
      return
    }
    const mimeType = preferredVoiceMimeType()
    const recorder = mimeType
      ? new MediaRecorder(stream, { mimeType })
      : new MediaRecorder(stream)

    voiceStream = stream
    voiceRecorder = recorder
    voiceChunks = []
    discardVoiceRecording = false
    voiceSourceBotId = currentBotId.value ?? ''

    recorder.ondataavailable = (event) => {
      if (event.data.size > 0) voiceChunks.push(event.data)
    }
    recorder.onerror = () => {
      discardVoiceRecording = true
      toast.error(t('chat.voiceInput.failed'))
      if (recorder.state === 'recording') {
        recorder.stop()
      } else {
        voiceRecorder = null
        voiceChunks = []
        releaseVoiceStream()
        voiceInputState.value = 'idle'
      }
    }
    recorder.onstop = () => {
      const chunks = voiceChunks
      const shouldDiscard = discardVoiceRecording || voiceRequestVersion !== requestVersion
      const sourceBotId = voiceSourceBotId
      const recordedType = recorder.mimeType || mimeType || 'audio/webm'
      voiceRecorder = null
      voiceChunks = []
      releaseVoiceStream()
      if (shouldDiscard || !chunks.length) {
        voiceInputState.value = 'idle'
        return
      }
      const audio = new Blob(chunks, { type: recordedType })
      void transcribeVoiceInput(audio, recordedType, modelId, sourceBotId, requestVersion)
    }

    recorder.start()
    voiceInputState.value = 'recording'
  } catch (error) {
    if (voiceRequestVersion !== requestVersion) return
    releaseVoiceStream()
    voiceRecorder = null
    voiceInputState.value = 'idle'
    const denied = error instanceof DOMException
      && (error.name === 'NotAllowedError' || error.name === 'SecurityError')
    toast.error(denied ? t('chat.voiceInput.permissionDenied') : t('chat.voiceInput.failed'))
  }
}

function handleVoiceInput() {
  if (voiceInputState.value === 'recording') {
    stopVoiceInput()
    return
  }
  if (voiceInputState.value === 'idle') void startVoiceInput()
}

watch(currentBotId, cancelVoiceInput)
onBeforeUnmount(cancelVoiceInput)

const stopAuthSessionCleanup = onAuthSessionCleared(() => {
  clearAllDrafts()
  inputText.value = ''
  pendingFiles.value = []
  composerError.value = ''
})
const { inputDraftKey, saveInputDraft, clearAllDrafts } = useComposerDrafts({
  currentBotId,
  tabId: () => props.tabId,
  inputText,
})

// The dock owns ALL geometry/visibility orchestration (box-slot mutex,
// backdrop-mask height) — the pane only needs two readings back from it: the
// mask height for the full-width backdrop strip, and the dock's own height
// for the message column's bottom padding (so the last message can always
// scroll clear of whatever the dock currently shows — the static pb-28 this
// replaces only ever fit the bare composer).
const dockEl = useTemplateRef<InstanceType<typeof ComposerDock>>('dockEl')
const { height: dockHeight } = useElementSize(() => dockEl.value?.$el ?? null)
const dockMaskHeight = computed(() => dockEl.value?.maskHeight ?? `${COMPOSER_MASK_BELOW_PX}px`)

// Virtual-keyboard lift (iOS Safari; Android lifts via the resized layout
// viewport instead — see useVirtualKeyboard). The whole dock container rises
// by the keyboard height, and the message column's bottom padding grows by
// the same amount so follow-mode keeps the last message clear of the lifted
// composer. While the model picker is open its search field may itself hold
// focus: lifting then would yank the trigger out from under the popover and
// fight iOS's own scroll-to-caret, so the lift is suppressed until the picker
// closes (the plus menu has no focusable input and never hits this).
const virtualKeyboardHeight = useVirtualKeyboard()
const composerLiftPx = computed(() => (modelPopoverOpen.value ? 0 : virtualKeyboardHeight.value))
const messagesBottomPad = computed(() => `${dockHeight.value + COMPOSER_MASK_BELOW_PX + 24 + composerLiftPx.value}px`)

// The textarea belongs to the pane, so when the dock hands the input slot
// back after ask_user is resolved or canceled, it emits and we focus here.
function handleDockRevealComposer(opts: { focus?: boolean }) {
  if (opts.focus) void nextTick(focusTextarea)
}

watch([
  startupSendFailure,
  paneTarget,
  isVisible,
], ([failure]) => {
  if (!failure || !isVisible.value) return
  if (failure.botId && failure.botId !== paneTarget.value.botId) return
  const failureScope = failure.composerScope?.trim()
  const paneScope = inputDraftKey.value || 'chat'
  const renderedSessionId = paneTarget.value.sessionId ?? ''
  if (failureScope) {
    if (failureScope !== paneScope) return
    if (failure.sessionId) {
      if (renderedSessionId !== failure.sessionId) return
    } else if (renderedSessionId) return
  } else {
    if (failure.sessionId && failure.sessionId !== renderedSessionId) return
  }

  inputText.value = failure.restoreInput
  saveInputDraft(inputDraftKey.value, failure.restoreInput)
  pendingFiles.value = (failure.restoreAttachments ?? [])
    .map(attachmentToFile)
    .filter((file): file is File => file !== null)
  requestedSkills.value = skillSlashEnabled.value
    ? (failure.restoreRequestedSkills ?? []).map(skill => ({ ...skill }))
    : []
  composerError.value = failure.error || t('chat.sendFailed')
  chatStore.clearStartupSendFailure(failure.id)
}, { immediate: true })

const elNode = useTemplateRef('scrollContainer')
// Resolve the real scrollable viewport via data-slot to avoid coupling to the
// child-index DOM shape of @felinic/ui's ScrollArea (which wraps reka-ui).
const scrollEl = computed<HTMLElement | null>(() => {
  const root = elNode.value?.$el as HTMLElement | undefined
  if (!root) return null
  return root.querySelector('[data-slot="scroll-area-viewport"]') as HTMLElement | null
})
const descEl = computed<HTMLElement | null>(() => {
  return (scrollEl.value?.firstElementChild as HTMLElement | null) ?? null
})
const loadMoreSentinel = useTemplateRef<HTMLElement>('loadMoreSentinel')

// The last turn's container. A function ref because template refs inside
// v-for collect into arrays — bind just the pinnable (last) turn by hand.
const lastTurnEl = ref<HTMLElement | null>(null)
function setLastTurnEl(el: unknown) {
  lastTurnEl.value = el as HTMLElement | null
}

// The message list rendered as TURNS: a user message opens a turn that holds
// everything up to the next user message (leading assistant/system rows before
// the first user message form their own head turn). Keyed by the opening
// message's id, which never changes for a given turn — so sending a new
// message APPENDS a fresh container and no previous turn's DOM is ever
// re-parented. This is load-bearing for scroll stability: re-parenting (the
// earlier "split at the last prompt into two chunks" design) remounts the
// whole previous turn on every send — markdown re-renders, code re-highlights
// async, expanded tool groups collapse — and the transient height collapse
// showed up as a hard scroll jump when sending from the bottom.
// `start` is each turn's offset into the flat list, for the fork-source
// dividers whose positions are flat-list indexes.
const messageTurns = computed(() => {
  const turns: { id: string, start: number, messages: ChatMessage[] }[] = []
  messages.value.forEach((msg, index) => {
    const last = turns[turns.length - 1]
    if (msg.role === 'user' || !last) {
      turns.push({ id: msg.id, start: index, messages: [msg] })
    } else {
      last.messages.push(msg)
    }
  })
  return turns
})
const lastMessageId = computed(() => messages.value[messages.value.length - 1]?.id ?? '')

const {
  isScrolling,
  highlightedMessageId,
  showJumpToBottom: showJumpToBottomFromScroll,
  scrollToBottom,
  scrollToMessage,
  suppressAutoScrollForPrepend,
  markEscaped,
  pinAfterSend,
  onActivatedRestoreScroll,
  onDeactivatedResetScroll,
  onMessageActive,
  startScrollTween,
  findMessageElement,
  messageJumpTarget,
  turnReserveStyle,
} = useChatScroll({
  scrollEl,
  contentEl: descEl,
  lastTurnEl,
  messages,
  isActive: isVisible,
  sessionId: computed(() => paneTarget.value.sessionId ?? `draft:${paneTarget.value.viewId}`),
})
const showJumpToBottom = computed(() => showJumpToBottomFromScroll.value && !loadingChats.value)

// Rail navigation parks the reader on a chosen turn, so escape follow —
// otherwise the next streamed mutation would drag them back to the bottom.
// Landing uses the same rule as pin/entry/reply jumps (messageJumpTarget):
// the chosen turn arrives at the pin offset, identical to how it looked
// right after being sent.
function handleRailJump(seg: ScrollRailSegment) {
  void nextTick(() => {
    const root = scrollEl.value
    const target = findMessageElement(seg.id)
    if (!root || !target) return
    markEscaped()
    startScrollTween(root, () => messageJumpTarget(root, seg.id))
  })
}

onBeforeUnmount(() => {
  stopAuthSessionCleanup()
})

// Sentinel-based infinite scroll for older history. Position preservation
// across the prepend itself is owned by useChatScroll (see
// suppressAutoScrollForPrepend's doc comment for why no manual scrollTop
// correction is needed).
async function ensureOlderLoaded() {
  if (loadingOlder.value || !hasMoreOlder.value) return
  if (!messages.value.length) return
  suppressAutoScrollForPrepend()
  try {
    await chatStore.loadOlderMessages(paneTarget.value)
  } catch (error) {
    console.error('Failed to load older messages:', error)
  }
}

useIntersectionObserver(
  loadMoreSentinel,
  ([entry]) => {
    if (!isVisible.value) return
    if (!entry?.isIntersecting) return
    void ensureOlderLoaded()
  },
  {
    root: scrollEl,
    rootMargin: '200px 0px 0px 0px',
    threshold: 0,
  },
)

onActivated(() => {
  onActivatedRestoreScroll(loadingMessages)
})

onDeactivated(() => {
  onDeactivatedResetScroll()
})

async function handleReplyJump(messageId: string) {
  const target = messageId.trim()
  if (!target) return
  const localId = chatStore.findMessageIdByExternalId(target, paneTarget.value)
  if (localId && await scrollToMessage(localId)) return
  const locatedId = await chatStore.locateMessageByExternalId(target, paneTarget.value)
  if (locatedId) {
    await scrollToMessage(locatedId)
  }
}

async function handleForkSourceClick() {
  const source = forkSource.value
  const botId = currentBotId.value?.trim() ?? ''
  if (!source || !botId || openingForkSource.value) return
  const origin = { ...paneTarget.value }
  openingForkSource.value = true
  try {
    await fetchSession(botId, source.sessionId)
    workspaceTabs.openSessionChatFromView({
      viewId: origin.viewId,
      sessionId: source.sessionId,
      title: source.title,
      expectedSessionId: origin.sessionId,
      explicitSelection: true,
    })
  } catch {
    toast.error(t('chat.forkSourceUnavailable'))
  } finally {
    openingForkSource.value = false
  }
}

function handleForkMessage(turnId: string) {
  composerError.value = ''
  const id = turnId.trim()
  if (!id) return
  pendingForkTurnId.value = id
  forkDialogOpen.value = true
}

// Keyboard bridges into the two composer list surfaces (slash picker, command
// panel results). The composer textarea keeps focus the whole time — like
// reka's ListboxFilter, the bridge runs the listbox in virtual-highlight mode
// and the textarea forwards navigation keys to whichever surface is showing.
const slashPickerBridge = ref<InstanceType<typeof CommandKeyBridge> | null>(null)

function activeComposerListBridge() {
  if (slashPanelOpen.value && slashPanelHasResults.value) return slashPickerBridge.value
  if (commandPanelEvent.value && commandResultItems.value.length) return dockEl.value?.commandBridge ?? null
  return null
}

// The composer card's own padding/gaps are plain background, not covered by
// any child — @click.self missed them whenever a child element's box (even
// its invisible padding) sat on top of the pointer, which is most of the
// card. Focus the textarea for any click that isn't already on something
// interactive (button, link, form control, or a [role="button"] custom
// trigger like the model/agent pills) — mirrors a plain text field, where
// clicking anywhere in its box places the caret.
const COMPOSER_INTERACTIVE_SELECTOR = 'button, a, input, [role="button"], [contenteditable="true"]'
function handleComposerClick(e: MouseEvent) {
  const target = e.target as HTMLElement
  if (target.closest(COMPOSER_INTERACTIVE_SELECTOR)) return
  focusTextarea()
}

function handleComposerKeydown(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  if (e.key === 'Escape') {
    // Dismiss the command result panel; the slash picker is input-driven and
    // closes by editing the text, so Escape only targets the panel.
    if (!slashPanelOpen.value && commandPanelEvent.value) {
      e.preventDefault()
      clearCurrentCommandEvent()
    }
    return
  }
  const bridge = activeComposerListBridge()
  if (bridge && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
    e.preventDefault()
    bridge.navigate(e)
    return
  }
  if (e.key !== 'Enter' || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey) return
  if (bridge?.hasHighlight) {
    e.preventDefault()
    bridge.select(e)
    return
  }
  e.preventDefault()
  handleSend()
}

async function handleRetryMessage(turnId: string) {
  if (composerConfigPending.value) return
  composerError.value = ''
  const result = await chatStore.retryLatestAssistant(turnId, {
    target: paneTarget.value,
    modelId: overrideModelId.value,
    reasoningEffort: overrideReasoningEffort.value,
    workspaceTargetId: sendWorkspaceTargetId.value,
  })
  await refreshACPComposerConfigAfterSelectionError(result)
  if (!result.ok && result.error) {
    composerError.value = result.error
  }
}

async function handleEditMessage(turnId: string, text: string, done?: (started: boolean) => void) {
  if (composerConfigPending.value) {
    done?.(false)
    return
  }
  composerError.value = ''
  try {
    const result = await chatStore.editLatestUser(turnId, text, {
      target: paneTarget.value,
      modelId: overrideModelId.value,
      reasoningEffort: overrideReasoningEffort.value,
      workspaceTargetId: sendWorkspaceTargetId.value,
    })
    await refreshACPComposerConfigAfterSelectionError(result)
    if (!result.ok && result.error) {
      composerError.value = result.error
    }
    done?.(result.ok || result.stage === 'stream')
  } catch {
    done?.(false)
  }
}

async function handleSend() {
  if (!isActive.value) return
  if (!skillSlashEnabled.value && requestedSkills.value.length) {
    requestedSkills.value = []
  }
  const text = inputText.value.trim()
  const files = [...pendingFiles.value]
  const skills = [...requestedSkills.value]
  if (
    (!text && !files.length && !skills.length)
    || streaming.value
    || loadingMessages.value
    || activeChatReadOnly.value
    || composerConfigPending.value
    // Keyboard send bypasses the disabled button, so the no-model gate is
    // repeated here rather than living only on the control.
    || composerHasNoModel.value
  ) return
  const localAction = localQuickActionIDForSlash(text)
  if (localAction && localQuickActionBlocked()) return
  if (localAction && runLocalQuickAction(localAction, text)) {
    inputText.value = ''
    saveInputDraft(inputDraftKey.value, '')
    return
  }
  const isNewCommand = /^\/new(?:\s|$)/i.test(text)
  if (defaultExternalAgentComposerError.value && !hasExplicitSessionSelection.value && !isNewCommand) {
    composerError.value = defaultExternalAgentComposerError.value
    return
  }
  const sentDraftKey = inputDraftKey.value
  const sentContext = captureChatPaneSendContext(
    paneTarget.value,
    inputDraftKey.value || 'chat',
  )
  const sentModelId = overrideModelId.value
  const sentReasoningEffort = overrideReasoningEffort.value
  const sentWorkspaceTargetId = sendWorkspaceTargetId.value
  composerError.value = ''
  inputText.value = ''
  saveInputDraft(sentDraftKey, '')
  pendingFiles.value = []
  requestedSkills.value = []

  let attachments: ChatAttachment[] | undefined
  try {
    if (files.length) {
      attachments = await Promise.all(files.map(fileToAttachment))
    }
  } catch (error) {
    if (!matchesChatPaneSendContext(
      sentContext,
      paneTarget.value,
      inputDraftKey.value || 'chat',
    )) return
    inputText.value = text
    pendingFiles.value = files
    requestedSkills.value = skills
    composerError.value = error instanceof Error ? error.message : t('chat.sendFailed')
    return
  }

  // Arm the pin only once the store has passed command handling and session
  // setup and is about to start a real turn. Command-only sends therefore do
  // not leave a latent pin behind; startup failures roll the arm back.
  let rollbackPin: (() => void) | null = null
  const result = await chatStore.sendMessage(text, attachments, {
    target: sentContext.target,
    modelId: sentModelId,
    reasoningEffort: sentReasoningEffort,
    workspaceTargetId: sentWorkspaceTargetId,
    requestedSkills: skills,
    composerScope: sentContext.composerScope,
    onBeforeTurnAppend: () => {
      if (!matchesChatPaneSendContext(
        sentContext,
        paneTarget.value,
        inputDraftKey.value || 'chat',
      )) return
      rollbackPin = pinAfterSend()
    },
    onTurnAppendAborted: () => {
      rollbackPin?.()
      rollbackPin = null
    },
  })
  rollbackPin = null
  await refreshACPComposerConfigAfterSelectionError(result)
  if (!result.ok && result.stage === 'startup') {
    const restoreInput = result.restoreInput ?? text
    if (!matchesChatPaneSendContext(
      sentContext,
      paneTarget.value,
      inputDraftKey.value || 'chat',
    )) return
    inputText.value = restoreInput
    saveInputDraft(sentDraftKey, restoreInput)
    pendingFiles.value = files
    requestedSkills.value = skills
    if (commandPanelEvent.value?.type !== 'command_error') {
      composerError.value = result.error || t('chat.sendFailed')
    }
  }
}
</script>
