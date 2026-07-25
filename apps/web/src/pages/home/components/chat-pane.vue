<template>
  <div class="flex-1 flex flex-col h-full min-w-0 relative">
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

              <!-- Read-only sessions (subagent / system / synced channel
                   threads) can't take new input, so an empty one states why it
                   has nothing. A fresh, writable chat instead gets the centered
                   welcome composer below, never a stray line in a blank pane. -->
              <div
                v-if="messages.length === 0 && !loadingChats && !loadingMessages && activeChatReadOnly"
                class="flex items-center justify-center min-h-75"
              >
                <p
                  v-if="activeSession?.type === 'subagent'"
                  class="text-muted-foreground text-xs"
                >
                  {{ $t('chat.emptySubagent') }}
                </p>
                <p
                  v-else
                  class="text-muted-foreground text-xs"
                >
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
                      :session-type="activeSession?.type"
                      :bot-id="currentBotId"
                      :channel-thread="isChannelThread"
                      :channel-platform="channelPlatform"
                      :bot-name="currentBot?.name"
                      :bot-avatar-url="currentBot?.avatar_url"
                      :on-open-media="galleryOpenBySrc"
                      :on-reply-click="handleReplyJump"
                      :on-retry-message="handleRetryMessage"
                      :can-retry-latest-assistant="latestRetryableAssistantId === ((msg.serverId ?? msg.id).trim())"
                      :can-edit-latest-user="latestEditableUserId === ((msg.serverId ?? msg.id).trim())"
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
        :message-id="pendingForkMessageId"
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
        :class="isWelcome
          ? 'inset-0 flex flex-col items-center justify-start pt-[38dvh]'
          : 'inset-x-0 bottom-0 pt-2 pb-8'"
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
             edge stay pinned at pt-[38dvh], so a growing composer (multiline
             text or attachments) only extends downward and never pushes the
             greeting up; normal: display:contents removes this from layout. -->
        <div :class="isWelcome ? 'flex flex-col items-center gap-10 w-full' : 'contents'">
          <div
            v-if="isWelcome"
            class="w-full max-w-[840px] mx-auto px-4 text-center sm:px-6 lg:px-10"
          >
            <h1 class="text-balance text-2xl font-semibold tracking-tight text-foreground">
              {{ welcomeGreeting }}
            </h1>
          </div>
          <!-- Mirror the message column's padding (px-4 sm:px-6 lg:px-10) exactly
               so the composer and the chat body always share one width — the inner
               gutter still relaxes on a cramped pane, but both edges move together. -->
          <div class="pointer-events-auto relative w-full max-w-[840px] mx-auto px-4 sm:px-6 lg:px-10">
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
                    <CommandSeparator v-if="visibleSlashQuickActions.length && visibleSlashSkills.length" />
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
              <!--
              Compact uses a concrete 1.75rem radius (= half the compact height:
              button 2.25rem + py-2.5 ×2 = 3.5rem), so a short composer still reads as
              a perfect pill — but, unlike rounded-full (9999px), the value can be
              animated. Multiline shrinks the corners to 1.25rem; transitioning
              between two concrete radii interpolates smoothly, whereas animating
              out of 9999px snapped mid-way (the value stayed clamped-round until
              it crossed half-height, then jumped the corner in one step).
            -->
              <div
                ref="composerEl"
                data-slot="input-group"
                role="group"
                class="chat-composer-edge relative flex w-full flex-wrap items-center gap-1 bg-surface-composer px-2.5 py-2.5 transition-[border-radius] motion-reduce:transition-none"
                :class="(isMultiline || showAttachmentGrid) ? 'chat-composer-radius-multiline' : 'chat-composer-radius-compact'"
                :style="{ transitionDuration: `${composerRadiusMs}ms`, transitionTimingFunction: composerRadiusEase }"
                @click.self="focusTextarea"
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
                  :disabled="!currentBotId || activeChatReadOnly || loadingMessages"
                  class="field-sizing-content resize-none break-words bg-transparent text-base leading-[var(--chat-leading)] text-foreground outline-none placeholder:text-[var(--field-placeholder)] disabled:cursor-not-allowed"
                  :class="isMultiline
                    ? 'order-none w-full basis-full pl-2 pr-1 pt-2 pb-1.5 max-h-52'
                    : 'order-2 min-w-0 flex-1 self-center overflow-hidden whitespace-nowrap pl-1 pr-1 py-1 max-h-32'"
                  @keydown="handleComposerKeydown"
                  @paste="handlePaste"
                  @input="syncMultiline"
                />

                <DropdownMenu v-model:open="agentPopoverOpen">
                  <DropdownMenuTrigger as-child>
                    <Button
                      type="button"
                      variant="ghost"
                      :disabled="!currentBotId || activeChatReadOnly || composerConfigPending"
                      :title="$t('chat.composerActions')"
                      class="order-1 size-9 rounded-full text-foreground/85"
                      :class="isMultiline ? 'self-end' : 'self-center'"
                      :aria-label="$t('chat.composerActions')"
                    >
                      <Spinner
                        v-if="agentChanging"
                        class="size-4"
                      />
                      <Plus
                        v-else
                        class="size-[22px]"
                        :stroke-width="1.75"
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
                    <template v-if="canChangeAgent && enabledACPProfiles.length">
                      <DropdownMenuLabel>{{ $t('chat.agent') }}</DropdownMenuLabel>
                      <DropdownMenuItem @select="selectMemohAgent">
                        <img
                          src="/logo.svg"
                          alt=""
                          class="size-4 shrink-0"
                        >
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.agentMemoh') }}</span>
                        <Check
                          v-if="!activeIsACP"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-for="profile in enabledACPProfiles"
                        :key="profile.id"
                        @select="selectACPAgent(profile)"
                      >
                        <component :is="acpAgentIcon(profile.id, true)" />
                        <span class="min-w-0 flex-1 truncate">{{ profile.display_name || profile.id }}</span>
                        <Check
                          v-if="activeACPAgentId === normalizedProfileID(profile.id)"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                    </template>
                    <template v-if="showComputersMenu">
                      <DropdownMenuSeparator v-if="canChangeAgent && enabledACPProfiles.length" />
                      <DropdownMenuLabel>{{ $t('chat.computers') }}</DropdownMenuLabel>
                      <DropdownMenuItem
                        v-if="workspaceTargetsInitialLoading"
                        disabled
                      >
                        <Spinner />
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.computerLoading') }}</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-else-if="workspaceTargetsLoadFailed"
                        disabled
                      >
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.computerLoadFailed') }}</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-else-if="!workspaceTargets.length"
                        disabled
                      >
                        <span class="min-w-0 flex-1 truncate">{{ $t('chat.computerNone') }}</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-if="selectedWorkspaceTargetMissing"
                        disabled
                      >
                        <Monitor class="size-4 shrink-0" />
                        <span class="min-w-0 flex-1 truncate">
                          {{ workspaceTargetSelection.snapshot?.name || $t('chat.computerUnavailable') }}
                        </span>
                        <span class="shrink-0 text-caption text-muted-foreground">{{ $t('chat.computerUnavailable') }}</span>
                        <Check class="ml-auto" />
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-for="target in workspaceTargets"
                        :key="target.target_id"
                        :disabled="computerSwitchLocked || !workspaceTargetAvailable(target)"
                        @select="selectWorkspaceTarget(target)"
                      >
                        <component
                          :is="target.kind === 'native' ? Server : Monitor"
                          class="size-4 shrink-0"
                        />
                        <span class="min-w-0 flex-1 truncate">{{ workspaceTargetName(target) }}</span>
                        <span class="shrink-0 text-caption text-muted-foreground">
                          {{ target.primary
                            ? (workspaceTargetAvailable(target)
                              ? $t('chat.computerDefault')
                              : $t('chat.computerDefaultStatus', { status: workspaceTargetStatusLabel(target) }))
                            : workspaceTargetStatusLabel(target) }}
                        </span>
                        <Check
                          v-if="selectedWorkspaceTargetId === target.target_id"
                          class="ml-auto"
                        />
                      </DropdownMenuItem>
                    </template>
                    <DropdownMenuSeparator v-if="(canChangeAgent && enabledACPProfiles.length) || showComputersMenu" />
                    <DropdownMenuItem
                      :disabled="!currentBotId || activeChatReadOnly || streaming || loadingMessages"
                      @select="fileInput?.click()"
                    >
                      <Paperclip />
                      <span class="min-w-0 flex-1 truncate">{{ $t('chat.attachFiles') }}</span>
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>

                <!-- Compact: content-sized and pushed right (ml-auto) so the
                     textarea (flex-1) owns the slack. Multiline: grows to fill the
                     controls row (flex-1) and right-aligns, so a long model name
                     truncates within the row instead of overflowing it. -->
                <div
                  class="order-3 flex min-w-0 items-center gap-2"
                  :class="isMultiline ? 'flex-1 justify-end self-end' : 'ml-auto self-center'"
                >
                  <Popover v-model:open="modelPopoverOpen">
                    <PopoverTrigger as-child>
                      <Button
                        type="button"
                        variant="ghost"
                        :disabled="!currentBotId || activeChatReadOnly || composerConfigPending"
                        class="composer-pill-press h-9 min-w-0 gap-1 rounded-full px-3 text-muted-foreground"
                        :style="{ maxWidth: `${modelTriggerMaxWidth}px` }"
                      >
                        <Spinner
                          v-if="composerConfigPending || acpModelsLoading"
                          class="size-3.5 shrink-0"
                        />
                        <span
                          ref="modelLabelEl"
                          class="min-w-0 truncate text-label"
                        >{{ modelTriggerLabel }}</span>
                        <ChevronDown class="size-3.5 shrink-0 opacity-50" />
                      </Button>
                    </PopoverTrigger>
                    <PopoverContent
                      class="w-80 overflow-hidden p-0"
                      align="end"
                      side="top"
                      :side-offset="4"
                    >
                      <InlineLoadingRow
                        v-if="composerModelsLoading"
                        class="px-2 py-3"
                      >
                        {{ $t('common.loading') }}
                      </InlineLoadingRow>
                      <ChatModelPicker
                        v-else
                        :model-value="overrideModelId"
                        :reasoning-effort="overrideReasoningEffort"
                        :reasoning-options="composerReasoningOptions"
                        :models="composerModels"
                        :providers="composerModelProviders"
                        model-type="chat"
                        :open="modelPopoverOpen"
                        @update:model-value="onComposerModelValueSelected"
                        @update:reasoning-effort="onComposerReasoningEffortSelected"
                        @close="modelPopoverOpen = false"
                      />
                    </PopoverContent>
                  </Popover>

                  <Button
                    v-if="activeIsACP"
                    type="button"
                    variant="ghost"
                    class="h-9 min-w-0 max-w-40 gap-1 rounded-full px-3 text-muted-foreground"
                    disabled
                  >
                    <FolderOpen class="size-3.5 shrink-0" />
                    <span
                      ref="acpProjectLabelEl"
                      class="min-w-0 truncate text-label"
                    >{{ activeACPProjectLabel }}</span>
                  </Button>

                  <div class="relative size-9 shrink-0">
                    <SessionInfoRing
                      v-if="!activeIsACP"
                      :override-model-id="overrideModelId"
                      :fallback-context-window="activeModel?.config?.context_window ?? null"
                      class="absolute inset-0 size-9 transition-[opacity,scale] duration-200 ease-out motion-reduce:transition-none"
                      :class="(!showSend && !streaming) ? 'scale-100 opacity-100' : 'pointer-events-none scale-75 opacity-0'"
                    />
                    <!-- Send and stop are one brand circle: the surface never
                         changes between the two states, only the glyph cross-fades
                         (arrow ⇄ stop square), so the button can't blink color or
                         shape mid-turn. While streaming it stays clickable to abort. -->
                    <Button
                      type="button"
                      variant="brand"
                      :disabled="streaming ? false : (!showSend || !currentBotId || activeChatReadOnly || loadingMessages || composerConfigPending)"
                      :aria-label="streaming ? 'Stop generating response' : 'Send message'"
                      class="absolute inset-0 size-9 rounded-full transition-[opacity,scale] duration-200 ease-[cubic-bezier(0.34,1.56,0.64,1)] motion-reduce:transition-none"
                      :class="(sendButtonVisible || streaming) ? 'scale-100 opacity-100' : 'pointer-events-none scale-0 opacity-0'"
                      @click="streaming ? chatStore.abort(paneTarget) : handleSend()"
                    >
                      <span
                        class="grid size-[20px] shrink-0 place-items-center"
                        aria-hidden="true"
                      >
                        <svg
                          viewBox="0 0 24 24"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="2.75"
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          class="col-start-1 row-start-1 size-[20px] transition-opacity duration-200 ease-out motion-reduce:transition-none"
                          :class="streaming ? 'opacity-0' : 'opacity-100'"
                        >
                          <path d="M12 19.5 V5" />
                          <path d="M6 10.5 L12 4.5 L18 10.5" />
                        </svg>
                        <svg
                          viewBox="0 0 24 24"
                          fill="currentColor"
                          class="col-start-1 row-start-1 size-[18px] transition-opacity duration-200 ease-out motion-reduce:transition-none"
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
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onBeforeUnmount, useTemplateRef, watch, nextTick, onActivated, onDeactivated } from 'vue'
import {
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
  Monitor,
  Server,
} from 'lucide-vue-next'
import { ScrollArea, Button, Popover, PopoverContent, PopoverTrigger, DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuLabel, DropdownMenuItem, DropdownMenuSeparator, Dialog, DialogContent, DialogHeader, DialogTitle, Command, CommandGroup, CommandItem, CommandKeyBridge, CommandList, CommandSeparator, Spinner, toast } from '@felinic/ui'
import { useChatStore, type ACPAgentSessionInput, type ChatMessage, type ChatWorkspaceTargetSnapshot, type SendMessageResult } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { storeToRefs } from 'pinia'
import { useElementSize, useIntersectionObserver } from '@vueuse/core'
import { useQuery } from '@pinia/colada'
import { getAcpProfiles, getModels, getProviders, getBotsByBotIdSettings, getBotsByBotIdWorkspaceTargets } from '@memohai/sdk'
import type { AcpprofilePublicProfile, ModelsGetResponse, ProvidersGetResponse, WorkspaceWorkspaceTarget } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import MessageItem from './message-item.vue'
import ChatAttachmentCard from './chat-attachment-card.vue'
import PanePlaceholder from '@/components/pane-placeholder/index.vue'
import InlineLoadingRow from '@/components/inline-loading-row/index.vue'
import { useChatScroll } from '../composables/useChatScroll'
import BgTaskPill from './bg-task-pill.vue'
import ForkSourceDivider from './fork-source-divider.vue'
import ChatForkDialog from './chat-fork-dialog.vue'
import ComposerDock from './composer-dock.vue'
import { usePendingApprovals } from '../composables/usePendingApprovals'
import ChatScrollRail, { type ScrollRailSegment } from './chat-scroll-rail.vue'
import { provideBgTaskBeacons } from '../composables/useBgTaskBeacons'
import MediaGalleryLightbox from './media-gallery-lightbox.vue'
import SessionInfoRing from './session-info-ring.vue'
import { useSessionInfo } from '../composables/useSessionInfo'
import ChatModelPicker from './chat-model-picker.vue'
import { EFFORT_LABELS, REASONING_EFFORT_DISABLE, availableEffortsForMode, resolveEffortLevels, resolveThinkingMode } from '@/pages/bots/components/reasoning-effort'
import { useMediaGallery } from '../composables/useMediaGallery'
import { ATTACHMENT_ANIM_MS, attachmentToFile, fileToAttachment, useComposerAttachments } from '../composables/useComposerAttachments'
import { useComposerDrafts } from '../composables/useComposerDrafts'
import { COMPOSER_MASK_BELOW_PX, useComposerLayout } from '../composables/useComposerLayout'
import { provideChatViewTarget } from '../composables/useChatViewContext'
import { fetchSafeSkillCatalog, fetchSession, type ChatAttachment, type CommandActionError, type CommandActionListItem, type RequestedSkillSelection, type UIUserInput } from '@/composables/api/useChat'
import { commandResultQuickActionText, isCommandResultItemSelectable } from './slash-command-result'
import { captureChatPaneSendContext, matchesChatPaneSendContext, shouldRefreshACPComposerConfig } from './chat-pane-send'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { useACPRuntime } from '@/composables/useACPRuntime'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH, acpAgentIcon, findMissingRequiredManagedField, isACPAgentEnabled, isACPNoProject, normalizeACPAgentID, readACPAgentConfig } from '@/utils/acp'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { hasBotPermission } from '@/utils/bot-permissions'
import { findLatestPendingChatDecision } from './chat-pending-decision'

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
const pendingForkMessageId = ref('')
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
const paneView = computed(() => chatStore.chatView(paneTarget.value))
const messages = computed(() => paneView.value.transcript.messages)
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
// it. Read-only sessions (subagent / system / synced channel threads) hide the
// composer entirely, so they never reach this state.
const isWelcome = computed(() =>
  !!currentBotId.value
  && !hasRenderedSession.value
  && !activeChatReadOnly.value
  && !loadingChats.value
  && messages.value.length === 0,
)

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
const welcomeGreeting = computed(() =>
  t(WELCOME_GREETING_KEYS[welcomeGreetingIndex.value] ?? WELCOME_GREETING_KEYS[0]),
)
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
  && activeChatCanFork.value,
)

const latestRetryableAssistantId = computed(() => {
  if (streaming.value || loadingMessages.value || activeChatReadOnly.value) return ''
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const message = messages.value[i]
    if (message?.role === 'assistant' && !message.streaming && !message.__optimistic) {
      return (message.serverId ?? message.id).trim()
    }
  }
  return ''
})

const latestEditableUserId = computed(() => {
  if (streaming.value || loadingMessages.value || activeChatReadOnly.value) return ''
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const message = messages.value[i]
    if (message?.role === 'user' && !message.streaming && !message.__optimistic) {
      return (message.serverId ?? message.id).trim()
    }
  }
  return ''
})

const { data: modelData } = useQuery({
  key: ['models'],
  query: async () => {
    const { data } = await getModels({ throwOnError: true })
    return data
  },
})

const { data: providerData } = useQuery({
  key: ['providers'],
  query: async () => {
    const { data } = await getProviders({ throwOnError: true })
    return data
  },
})

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
const enabledACPProfiles = computed(() =>
  acpProfiles.value.filter(profile => isACPAgentEnabled(currentBotMetadata.value, profile.id)),
)

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
const activeIsPendingACP = computed(() => activeChatTarget.value.isPendingACP)
const activeIsACP = computed(() => activeChatTarget.value.isACP)
const activeUsesACPComposer = computed(() => activeIsPendingACP.value || activeIsACP.value)
const showComputersMenu = computed(() => (
  !activeIsACP.value
  && !activeIsPendingACP.value
  && canWorkspaceRead.value
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

function workspaceTargetName(target: Pick<WorkspaceWorkspaceTarget, 'kind' | 'name'>): string {
  if (target.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return target.name || t('bots.remoteRuntime.unknownComputer')
}

function workspaceTargetStatus(target: WorkspaceWorkspaceTarget): string {
  if (target.kind === 'native') return 'online'
  return target.status || (target.online ? 'online' : 'offline')
}

function workspaceTargetStatusLabel(target: WorkspaceWorkspaceTarget): string {
  const status = workspaceTargetStatus(target)
  const key = `runtimes.status.${status}`
  const label = t(key)
  return label === key ? status : label
}

function workspaceTargetAvailable(target: WorkspaceWorkspaceTarget): boolean {
  return target.kind === 'native'
    || (workspaceTargetStatus(target) === 'online' && target.online !== false)
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
  activeIsACP,
  activeIsPendingACP,
  activeSessionMetadata,
  workspaceTargets,
  paneTarget,
], () => {
  if (activeIsACP.value || activeIsPendingACP.value) {
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
const activeACPProjectLabel = computed(() => {
  if (isACPNoProject(activeSessionMetadata.value)) return t('chat.noProject')
  const path = activeACPProjectPath.value
  const parts = path.split('/').filter(Boolean)
  return path ? parts[parts.length - 1] ?? path : t('chat.noProject')
})
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
const skillSlashEnabled = computed(() => !activeIsACP.value && !activeIsPendingACP.value)
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
  ...(!activeIsACP.value && !activeIsPendingACP.value
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
const slashPanelHasResults = computed(() =>
  visibleSlashQuickActions.value.length > 0 || visibleSlashSkills.value.length > 0,
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
  !!activeSessionId.value && !activeIsACP.value && sessionUsedTokens.value > 0 && !isCompactingSession.value,
)

// Client-side quick actions run an existing UI affordance directly instead of
// round-tripping text through send: /compact triggers the session-info
// panel's compaction, /model opens the composer's model picker. Everything
// else keeps the type-and-send flow (the store intercepts /new; /help and
// /skill list execute server-side).
function runLocalQuickAction(id: string): boolean {
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
  return false
}

function selectSlashQuickAction(action: { id: string, label: string }) {
  if (runLocalQuickAction(action.id)) {
    inputText.value = ''
    saveInputDraft(inputDraftKey.value, '')
    void nextTick(focusTextarea)
    return
  }
  inputText.value = action.label
  saveInputDraft(inputDraftKey.value, action.label)
  void nextTick(() => {
    focusTextarea()
    void handleSend()
  })
}

// Typed forms of the client-side quick actions ("/compact", "/model") — must
// be intercepted before the store send path, which would otherwise classify
// them as skill activation and fail with requested_skill_not_found.
function localQuickActionIDForSlash(text: string): string {
  switch (text.trim().toLowerCase()) {
    case '/compact':
      return 'compact'
    case '/model':
    case '/models':
      return 'model'
    default:
      return ''
  }
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
const commandPanelTitle = computed(() => {
  if (commandError.value) return t('chat.slash.commandError')
  return commandResult.value?.title || t('chat.slash.commandResult')
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

const commandPanelText = computed(() => commandError.value ? localizedCommandErrorMessage(commandError.value) : commandResult.value?.text || '')
const commandResultItems = computed(() =>
  (commandResult.value?.items ?? []).filter(item => isCommandResultItemSelectable(item, commandPanelActionID.value)),
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
  const kind = item.kind?.trim().toLowerCase()
  if (kind === 'quick_action') {
    const label = commandResultQuickActionText(item, commandPanelActionID.value)
    if (!label) return
    clearCurrentCommandEvent()
    selectSlashQuickAction({
      id: item.id?.trim() || label,
      label,
    })
    return
  }
  if (!skillSlashEnabled.value) return
  if (kind !== 'skill' || !item.id?.trim() || !item.title.trim()) return
  addRequestedSkill({
    name: item.id.trim(),
    display_name: item.title,
    description: item.description,
  })
}
const {
  runtime: acpCapabilityRuntime,
  models: acpModels,
  currentModelId: currentACPModelId,
  reasoningEfforts: acpReasoningEfforts,
  currentReasoningEffort: currentACPReasoningEffort,
  isEnsuring: acpRuntimeEnsuring,
  isPreparing: acpConfigPreparing,
  ensure: ensureACPRuntime,
  setModel: setACPModel,
  setReasoning: setACPReasoning,
} = useACPRuntime({
  target: paneTarget,
  pending: activeIsPendingACP,
  enabled: computed(() => activeUsesACPComposer.value && !!currentBotId.value),
  agentId: activeACPAgentId,
  projectPath: activeACPProjectPath,
})

const models = computed<ModelsGetResponse[]>(() => modelData.value ?? [])
const providers = computed<ProvidersGetResponse[]>(() => providerData.value ?? [])
const acpModelsLoading = computed(() =>
  activeUsesACPComposer.value
  && !acpCapabilityRuntime.value?.models
  && (agentChanging.value || acpRuntimeEnsuring.value),
)
const composerConfigPending = computed(() => activeUsesACPComposer.value && (
  agentChanging.value || acpConfigChanging.value
))
const canChangeAgent = computed(() => !streaming.value
  && !creatingSession.value
  && !composerConfigPending.value
  && messages.value.length === 0)

const acpModelPickerModels = computed<ModelsGetResponse[]>(() => {
  const adapted: ModelsGetResponse[] = []
  for (const model of acpModels.value) {
    const value = model.id?.trim() ?? ''
    if (!value) continue
    adapted.push({
      id: value,
      model_id: value,
      name: model.name?.trim() || value,
      provider_id: '',
      type: 'chat',
      config: {
        description: model.description?.trim() || undefined,
      },
    })
  }
  return adapted
})

// Normalize runtime-specific model metadata into the one contract consumed by
// Memoh's existing picker. The template stays runtime-agnostic; only this
// adapter knows whether the values came from a native model or an ACP session.
const composerModels = computed(() =>
  activeUsesACPComposer.value ? acpModelPickerModels.value : models.value,
)
const composerModelProviders = computed(() =>
  activeUsesACPComposer.value ? [] : providers.value,
)
const composerReasoningOptions = computed(() => {
  if (!activeUsesACPComposer.value) return undefined
  return acpReasoningEfforts.value.flatMap((effort) => {
    const value = effort.id?.trim() ?? ''
    if (!value) return []
    return [{
      value,
      label: effort.name?.trim() || value,
      description: effort.description?.trim() || undefined,
    }]
  })
})
const composerModelsLoading = computed(() =>
  activeUsesACPComposer.value && (acpModelsLoading.value || acpConfigPreparing.value),
)

const activeModel = computed(() => {
  const id = overrideModelId.value || botSettings.value?.chat_model_id || ''
  return models.value.find((m) => m.id === id)
})

type DefaultACPSettings = {
  chat_runtime?: string
  chat_acp_agent_id?: string
  chat_acp_project_path?: string
  chat_acp_project_mode?: string
}

type DefaultACPAvailability = {
  input: ACPAgentSessionInput | null
  messageKey: string
  loading: boolean
}

const defaultACPAvailability = computed<DefaultACPAvailability>(() => {
  const settings = botSettings.value as (DefaultACPSettings | undefined)
  if (!settings) {
    return { input: null, messageKey: '', loading: !!currentBotId.value && botSettingsLoading.value }
  }
  if (settings.chat_runtime !== 'acp_agent') return { input: null, messageKey: '', loading: false }
  if (!hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_exec')) {
    return { input: null, messageKey: 'chat.defaultACPNoWorkspaceExec', loading: false }
  }
  const agentId = normalizeACPAgentID(settings.chat_acp_agent_id)
  if (!agentId) return { input: null, messageKey: 'chat.defaultACPAgentMissing', loading: false }
  if (!acpProfileData.value) {
    return {
      input: null,
      messageKey: acpProfilesLoading.value ? 'chat.defaultACPLoading' : 'chat.defaultACPAgentUnavailable',
      loading: acpProfilesLoading.value,
    }
  }
  const profile = acpProfiles.value.find(item => normalizeACPAgentID(item.id) === agentId)
  if (!profile) return { input: null, messageKey: 'chat.defaultACPAgentUnavailable', loading: false }
  if (!isACPAgentEnabled(currentBotMetadata.value, profile.id)) {
    return { input: null, messageKey: 'chat.defaultACPAgentDisabled', loading: false }
  }
  const config = readACPAgentConfig(currentBotMetadata.value, profile.id)
  if (config.setupModeSet && findMissingRequiredManagedField(profile, config.managed, config.setupMode)) {
    return { input: null, messageKey: 'chat.defaultACPAgentNotConfigured', loading: false }
  }
  return {
    input: {
      agentId,
      projectPath: settings.chat_acp_project_path?.trim() || ACP_DEFAULT_PROJECT_PATH,
      projectMode: settings.chat_acp_project_mode?.trim() || ACP_DEFAULT_PROJECT_MODE,
    },
    messageKey: '',
    loading: false,
  }
})
const defaultACPSessionInput = computed(() => defaultACPAvailability.value.input)
const defaultACPUnavailableMessage = computed(() =>
  defaultACPAvailability.value.messageKey ? t(defaultACPAvailability.value.messageKey) : '',
)
const defaultACPLoading = computed(() => defaultACPAvailability.value.loading)
const defaultACPComposerError = ref('')

function clearDefaultACPComposerError() {
  if (defaultACPComposerError.value && composerError.value === defaultACPComposerError.value) {
    composerError.value = ''
  }
  defaultACPComposerError.value = ''
}

const activeThinkingMode = computed(() => resolveThinkingMode(activeModel.value?.config))

const activeModelSupportsReasoning = computed(() => activeThinkingMode.value !== 'none')

const activeModelClientType = computed(() =>
  providers.value.find((p) => p.id === activeModel.value?.provider_id)?.client_type,
)

const availableReasoningEfforts = computed(() =>
  availableEffortsForMode(activeThinkingMode.value, resolveEffortLevels(activeModel.value?.config, activeModelClientType.value)),
)

const selectedModelLabel = computed(() => {
  const current = composerModels.value.find(model => model.id === overrideModelId.value)
  return current?.name || current?.model_id || overrideModelId.value || t('chat.modelDefault')
})

const selectedReasoningLabel = computed(() => {
  if (activeUsesACPComposer.value) {
    const current = overrideReasoningEffort.value
    return composerReasoningOptions.value?.find(option => option.value === current)?.label || current
  }
  const v = overrideReasoningEffort.value
  return t(EFFORT_LABELS[v] ?? 'chat.modelDefault')
})

const reasoningActive = computed(() =>
  activeUsesACPComposer.value
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

function initFromBotSettings() {
  if (activeUsesACPComposer.value || !botSettings.value) return
  if (!overrideModelId.value) {
    overrideModelId.value = botSettings.value.chat_model_id ?? ''
  }
  if (!overrideReasoningEffort.value) {
    if (botSettings.value.reasoning_enabled && botSettings.value.reasoning_effort) {
      overrideReasoningEffort.value = botSettings.value.reasoning_effort
    } else {
      overrideReasoningEffort.value = REASONING_EFFORT_DISABLE
    }
  }
}

watch([botSettings, activeUsesACPComposer], () => initFromBotSettings(), { immediate: true })

watch(availableReasoningEfforts, (efforts) => {
  if (activeUsesACPComposer.value) return
  const current = overrideReasoningEffort.value
  if (!current || current === REASONING_EFFORT_DISABLE || efforts.includes(current)) return
  overrideReasoningEffort.value = efforts.includes('medium') ? 'medium' : efforts[0] ?? REASONING_EFFORT_DISABLE
}, { immediate: true })

watch(currentBotId, () => {
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
})

watch(activeUsesACPComposer, (usesACP, previouslyUsedACP) => {
  if (usesACP === previouslyUsedACP) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
  if (!usesACP) initFromBotSettings()
})

watch(activeACPAgentId, (agentID, previousAgentID) => {
  if (!activeUsesACPComposer.value || !previousAgentID || agentID === previousAgentID) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
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
  if (!activeUsesACPComposer.value || identity === previousIdentity) return
  overrideModelId.value = ''
  overrideReasoningEffort.value = ''
})

function reconcileACPComposerConfig() {
  const runtime = acpCapabilityRuntime.value
  if (!activeUsesACPComposer.value || !runtime) return

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
  () => activeUsesACPComposer.value && isVisible.value ? acpOperationScope.value : '',
  (scope) => {
    if (!scope || !activeACPAgentId.value) return
    void refreshACPComposerConfig().catch((error) => {
      composerError.value = resolveApiErrorMessage(error, t('chat.agentSwitchFailed'))
    })
  },
  { immediate: true },
)

async function refreshACPComposerConfig(): Promise<void> {
  if (!activeUsesACPComposer.value) return
  const desiredModelId = overrideModelId.value.trim()
  const runtime = await ensureACPRuntime(true, desiredModelId)
  if (!runtime || !activeUsesACPComposer.value) return
  reconcileACPComposerConfig()
}

async function refreshACPComposerConfigAfterSelectionError(result: SendMessageResult): Promise<void> {
  if (!shouldRefreshACPComposerConfig(result, activeUsesACPComposer.value)) return

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

function pendingMatchesDefaultACP(input: ACPAgentSessionInput): boolean {
  const metadata = activeChatTarget.value.metadata
  return activeChatTarget.value.kind === 'draft-acp'
    && metadata?.acp_agent_id === input.agentId
    && metadata?.project_path === (input.projectPath || ACP_DEFAULT_PROJECT_PATH)
    && metadata?.acp_project_mode === (input.projectMode || ACP_DEFAULT_PROJECT_MODE)
}

watch([defaultACPUnavailableMessage, defaultACPLoading, currentBotId, hasExplicitSessionSelection, isActive], ([message, loading, _bot, _explicit, focused]) => {
  if (!focused) return
  clearDefaultACPComposerError()
  if (!message || !currentBotId.value) return
  if (hasExplicitSessionSelection.value) return
  if (!loading) {
    chatStore.resetToEmptyComposer({}, paneTarget.value)
  }
  defaultACPComposerError.value = message
  composerError.value = message
}, { immediate: true })

watch([defaultACPSessionInput, defaultACPLoading, currentBotId, hasExplicitSessionSelection, activeChatTarget, isActive], ([input, loading, _bot, _explicit, _target, focused]) => {
  if (!focused) return
  if (!currentBotId.value) return
  if (!input) {
    if (!loading) {
      chatStore.cacheDefaultACPSession(null)
    }
    if (!loading && !hasExplicitSessionSelection.value && activeIsPendingACP.value) {
      chatStore.resetToEmptyComposer({}, paneTarget.value)
    }
    return
  }
  chatStore.cacheDefaultACPSession(input)
  if (hasExplicitSessionSelection.value) return
  clearDefaultACPComposerError()
  if (pendingMatchesDefaultACP(input)) return
  chatStore.stageDefaultACPSession(input, paneTarget.value)
}, { immediate: true })

watch([modelPopoverOpen, activeUsesACPComposer, acpOperationScope], ([open, usesACP]) => {
  if (!open || !usesACP) return
  void refreshACPComposerConfig().catch((error) => {
    composerError.value = resolveApiErrorMessage(error, t('chat.agentSwitchFailed'))
  })
})

function normalizedProfileID(value: unknown): string {
  return normalizeACPAgentID(value)
}

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

async function selectACPAgent(profile: AcpprofilePublicProfile) {
  const agentId = normalizeACPAgentID(profile.id)
  if (!agentId || agentChanging.value || !canChangeAgent.value) return
  agentPopoverOpen.value = false
  if (activeUsesACPComposer.value && agentId === activeACPAgentId.value) return
  agentChanging.value = true
  composerError.value = ''
  try {
    if (paneTarget.value.sessionId) {
      await withAgentSwitchTimeout(chatStore.updateCurrentSessionAgent({
        agentId,
      }, paneTarget.value))
    } else {
      chatStore.stageACPSession({
        agentId,
      }, {}, paneTarget.value)
      await withAgentSwitchTimeout(chatStore.ensurePendingACPRuntime(paneTarget.value))
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
    clearDefaultACPComposerError()
    composerError.value = ''
    pendingFiles.value = []
    return
  }
  if (!activeIsACP.value) return
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
  // The picker drives dismissal via @close (so opening a model's reasoning
  // options can adopt it without collapsing the menu); here we only sanitise
  // the effort when the new model can't reason.
  if (!activeModelSupportsReasoning.value) {
    overrideReasoningEffort.value = REASONING_EFFORT_DISABLE
  }
}

async function onComposerModelValueSelected(value: string) {
  if (activeUsesACPComposer.value && acpConfigChanging.value) return
  const previousModel = overrideModelId.value
  const previousReasoningEffort = overrideReasoningEffort.value
  overrideModelId.value = value
  if (!activeUsesACPComposer.value) {
    onModelSelected()
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
      activeUsesACPComposer.value
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

async function onComposerReasoningEffortSelected(value: string) {
  if (activeUsesACPComposer.value && acpConfigChanging.value) return
  const previousEffort = overrideReasoningEffort.value
  overrideReasoningEffort.value = value
  if (!activeUsesACPComposer.value) return

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
      activeUsesACPComposer.value
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
const {
  textareaEl,
  composerEl,
  modelLabelEl,
  acpProjectLabelEl,
  isMultiline,
  composerRadiusMs,
  composerRadiusEase,
  focusTextarea,
  modelTriggerMaxWidth,
  snapComposerNext,
} = useComposerLayout({
  inputText,
  isActive,
  showAttachmentGrid,
  modelTriggerLabel,
  activeIsACP,
  activeACPProjectLabel,
})

const showSend = computed(() => Boolean(inputText.value.trim()) || pendingFiles.value.length > 0 || requestedSkills.value.length > 0)

// Whether the trailing slot shows the send button at all. In standard chat the
// SessionInfoRing fills that slot while idle and the send button only reveals
// once there's content (showSend). ACP sessions have no ring, so without this
// the slot would sit empty on empty input — the button must stay put and just
// fall to its disabled (dimmed brand) state instead of vanishing.
const sendButtonVisible = computed(() => showSend.value || activeIsACP.value)

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
  onDraftKeySwap: snapComposerNext,
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
const messagesBottomPad = computed(() => `${dockHeight.value + COMPOSER_MASK_BELOW_PX + 24}px`)

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

function handleForkMessage(messageId: string) {
  composerError.value = ''
  const id = messageId.trim()
  if (!id) return
  pendingForkMessageId.value = id
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

async function handleRetryMessage(messageId: string) {
  if (composerConfigPending.value) return
  composerError.value = ''
  const result = await chatStore.retryLatestAssistant(messageId, {
    target: paneTarget.value,
    modelId: overrideModelId.value,
    reasoningEffort: overrideReasoningEffort.value,
    workspaceTargetId: selectedWorkspaceTargetId.value,
  })
  await refreshACPComposerConfigAfterSelectionError(result)
  if (!result.ok && result.error) {
    composerError.value = result.error
  }
}

async function handleEditMessage(messageId: string, text: string, done?: (started: boolean) => void) {
  if (composerConfigPending.value) {
    done?.(false)
    return
  }
  composerError.value = ''
  try {
    const result = await chatStore.editLatestUser(messageId, text, {
      target: paneTarget.value,
      modelId: overrideModelId.value,
      reasoningEffort: overrideReasoningEffort.value,
      workspaceTargetId: selectedWorkspaceTargetId.value,
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
  ) return
  if (text.startsWith('/') && files.length) {
    composerError.value = ''
    chatStore.showCommandError('slash_attachments_unsupported', t('chat.slash.attachmentsUnsupported'), {
      botId: currentBotId.value ?? undefined,
      sessionId: activeSessionId.value || undefined,
      composerScope: inputDraftKey.value || 'chat',
    })
    return
  }
  const localAction = localQuickActionIDForSlash(text)
  if (localAction && runLocalQuickAction(localAction)) {
    inputText.value = ''
    saveInputDraft(inputDraftKey.value, '')
    return
  }
  const isNewCommand = /^\/new(?:\s|$)/i.test(text)
  if (defaultACPComposerError.value && !hasExplicitSessionSelection.value && !isNewCommand) {
    composerError.value = defaultACPComposerError.value
    return
  }
  const sentDraftKey = inputDraftKey.value
  const sentContext = captureChatPaneSendContext(
    paneTarget.value,
    inputDraftKey.value || 'chat',
  )
  const sentModelId = overrideModelId.value
  const sentReasoningEffort = overrideReasoningEffort.value
  const sentWorkspaceTargetId = selectedWorkspaceTargetId.value
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
