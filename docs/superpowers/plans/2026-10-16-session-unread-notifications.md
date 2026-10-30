# Session Unread Dots and System Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mark a user's own web sessions as needing attention when a run completes, fails or waits for a decision, and surface it as unread dots, a "Needs you" inbox, a title/dock count, and browser/desktop notifications.

**Architecture:** The server records `attention_at`/`attention_kind` on `bot_sessions` from the runtime's terminal and waiting-decision observers, keeps per-user read cursors in `session_reads`, and pushes `session_attention`/`session_read` events on a per-user topic of the existing in-process `event.Hub`. The web app keeps one cross-bot unread store fed by `GET /users/me/unread` plus a per-user SSE, marks the viewed session read, renders dots/inbox/counts, and raises notifications through the browser API or, on desktop, through the Electron main process.

**Tech Stack:** Go (Echo, sqlc, pgx, Uber FX), PostgreSQL, Vue 3 + Pinia, Vitest, Electron (electron-vite).

**Spec:** `docs/superpowers/specs/2026-10-14-session-unread-notifications-design.md`

## Global Constraints

- Eligible session: `visibility = 'user'`, `type IN ('chat','acp_agent')`, `channel_type` NULL / `''` / `'local'`, `created_by_user_id IS NOT NULL`, `deleted_at IS NULL`. Recipient = `created_by_user_id`.
- Kinds: terminal `completed` → `reply`; `failed` or `lost` → `failed`; entering `waiting_decision` → `needs_input`; `aborted` → nothing.
- Unread ⇔ `attention_at IS NOT NULL AND attention_at > COALESCE(read_at, '-infinity')`.
- Writing attention never changes `bot_sessions.updated_at`. `attention_at` only moves forward; `read_at` only moves forward (`GREATEST`).
- Events and APIs never carry message bodies.
- OSS is single-team: `internal/db/db.go` sets `memoh.team_id` on every connection, so background writers need no extra team context. New tables still follow the team-scoped pattern of migration `0162` (team_id default, composite team FKs `NOT VALID`, RLS enabled + forced, four policies) because `internal/db/team_*_integration_test.go` guard it.
- Migration number: next free (`0167` at the time of writing); update `0001_init.up.sql` to the same final state; run `mise run sqlc-generate`.
- After handler changes: `mise run swagger-generate` then `mise run sdk-generate`; never hand-edit `internal/db/postgres/sqlc/` or `packages/sdk/`.
- Inbox: hidden when empty; newest first; at most 5 rows, then a "Show all (n)" row.
- Notification body copy is fixed per kind (i18n en/zh/ja): replied / run failed / waiting for your confirmation. Title: `"<bot name> · <session title>"`.
- Web notifications default **off** (enabling is the permission gesture); desktop default **on**. Setting stored per device in localStorage key `memoh-system-notifications`.
- Before any UI edit read `packages/ui/AGENTS.md`; colors via tokens only (`bg-accent-red-fill` for reply/failed, `--accent-yellow` for needs_input); `mise run lint` includes the UI contract guard.
- After code: `pnpm run lint --fix`, `pnpm run typecheck`, the relevant `go test`, and commit per task.

## Review Focus

1. **Run finishes while the user is looking but the window is blurred** (other app focused) — must become unread and notify; covered in Task 7 viewing tests.
2. **Observer replays the same terminal run** (manager + reaper both observe) — must not notify twice; `Mark` returns `changed=false` on a non-advancing timestamp and publishes nothing (Task 2 test).
3. **Mark-read races a new attention** — a `through` older than the new `attention_at` keeps the session unread (Task 2 test).
4. **Session deleted, or the user lost bot access, after attention was recorded** — it must disappear from `/users/me/unread` and the SSE must stop delivering for that bot (Task 2 query test, Task 5 handler test).
5. **Several desktop windows receive the same event** — exactly one native notification (Task 9 dedup test).

---

### Task 1: Schema and queries

**Files:**
- Create: `db/postgres/migrations/0167_session_attention.up.sql`, `db/postgres/migrations/0167_session_attention.down.sql`
- Modify: `db/postgres/migrations/0001_init.up.sql` (bot_sessions columns, `session_reads` table, index, RLS, team FKs in the deferred block)
- Create: `db/postgres/queries/session_attention.sql`
- Generated: `internal/db/postgres/sqlc/*`

**Interfaces — Produces (sqlc names):**
- `MarkSessionAttention(ctx, {ID uuid, Kind text, At timestamptz}) (MarkSessionAttentionRow{ID, BotID, CreatedByUserID, Title, AttentionKind, AttentionAt}, error)` — single `UPDATE … RETURNING`; the WHERE clause contains the full eligibility rule and `(attention_at IS NULL OR attention_at < @at)`; `pgx.ErrNoRows` means "ineligible or not newer".
- `MarkSessionRead(ctx, {UserID, SessionID, Through}) (time, error)` — `INSERT … ON CONFLICT (team_id, user_id, session_id) DO UPDATE SET read_at = GREATEST(session_reads.read_at, EXCLUDED.read_at) RETURNING read_at`.
- `ListUnreadSessionsForUser(ctx, {UserID, Limit int32}) ([]Row{SessionID, BotID, Title, AttentionKind, AttentionAt}, error)` — eligible, not deleted, `created_by_user_id = @user_id`, unread rule, `ORDER BY attention_at DESC`.
- `ListSessionAttentionForUser(ctx, {UserID, SessionIds []uuid}) ([]Row{SessionID, AttentionKind, Unread bool}, error)` — only rows whose `created_by_user_id = @user_id` and `attention_at IS NOT NULL`.

- [ ] **Step 1:** Write the up migration: `ALTER TABLE public.bot_sessions ADD COLUMN IF NOT EXISTS attention_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS attention_kind TEXT` with check `attention_kind IS NULL OR attention_kind IN ('reply','failed','needs_input')`; `CREATE TABLE IF NOT EXISTS public.session_reads(team_id …, user_id UUID NOT NULL, session_id UUID NOT NULL, read_at TIMESTAMPTZ NOT NULL, PRIMARY KEY (team_id, user_id, session_id))`; composite FKs to `users(team_id,id)` and `bot_sessions(team_id,id)` `ON DELETE CASCADE NOT VALID`; index `idx_bot_sessions_attention ON bot_sessions (team_id, created_by_user_id, attention_at DESC) WHERE attention_at IS NOT NULL AND deleted_at IS NULL`; RLS + four policies as in `0162`. Down migration reverses in reverse order. Mirror the final state in `0001_init.up.sql`.
- [ ] **Step 2:** Write the four queries above in `session_attention.sql`; run `mise run sqlc-generate`. Expected: generates without errors.
- [ ] **Step 3: Postgres integration test** `internal/chat/attention/queries_postgres_integration_test.go` (`//go:build integration`, DSN from `TEST_POSTGRES_DSN`, same harness as `internal/chat/thread/service_postgres_integration_test.go`):
  - `TestPostgresMarkSessionAttentionEligibility`: chat/local session → row returned; `schedule`, `discuss`, `subagent`, `visibility='internal'`, `channel_type='telegram'`, NULL creator, deleted → `ErrNoRows`.
  - `TestPostgresMarkSessionAttentionKeepsUpdatedAt`: `updated_at` identical before/after.
  - `TestPostgresMarkSessionAttentionOnlyMovesForward`: second call with an older or equal `At` → `ErrNoRows`.
  - `TestPostgresUnreadRules`: attention T1, read through T1 → not listed; new attention T2 > T1 → listed; `MarkSessionRead` with `through` < current `read_at` keeps the larger value; deleted session not listed; another user's session not listed.
- [ ] **Step 4:** Run `TEST_POSTGRES_DSN=… TEST_POSTGRES_BOOTSTRAP_SCHEMA=1 go test -tags=integration -run 'TestPostgres(MarkSession|Unread)' ./internal/chat/attention/` and `go test -tags=integration ./internal/db/...` (team schema guards). Expected: PASS.
- [ ] **Step 5:** Commit `feat(db): add session attention and read cursors`.

### Task 2: Attention service and user topic events

**Files:**
- Create: `internal/chat/attention/service.go`, `internal/chat/attention/service_test.go`
- Modify: `internal/chat/event/hub.go` (user topic + event types)
- Modify: `internal/chat/thread/service.go` (two response fields on `Thread`)

**Interfaces — Produces:**
- `event.UserTopic(userID string) string` → `"user:" + userID`; `event.EventTypeSessionAttention = "session_attention"`, `event.EventTypeSessionRead = "session_read"`. Hub doc comment: the key is a topic (bot id or user topic).
- `attention.Kind` (`KindReply`, `KindFailed`, `KindNeedsInput`) and `attention.KindForTerminalState(state string) (Kind, bool)`.
- `attention.Attention{BotID, SessionID, Title string; Kind Kind; At time.Time}` (JSON: `bot_id, session_id, title, attention_kind, attention_at`).
- `attention.NewService(queries Queries, publisher event.Publisher, now func() time.Time) *Service`, where `Queries` is the four sqlc methods from Task 1.
- `(*Service).Mark(ctx, sessionID string, kind Kind) (changed bool, err error)` — publishes `session_attention` to `UserTopic(creator)` only when changed.
- `(*Service).MarkRead(ctx, userID, sessionID string, through time.Time) (time.Time, error)` — publishes `session_read {session_id, read_at}` to the user topic.
- `(*Service).ListUnread(ctx, userID string) ([]Attention, error)` — limit 200.
- `(*Service).Annotate(ctx, userID string, threads []thread.Thread) ([]thread.Thread, error)` — fills `AttentionKind`, `Unread`.
- `thread.Thread` gains `AttentionKind string \`json:"attention_kind,omitempty"\`` and `Unread bool \`json:"unread,omitempty"\``.

- [ ] **Step 1: Failing unit tests** (fake `Queries`, recording publisher):
  - `TestKindForTerminalState`: completed→reply, failed→failed, lost→failed, aborted→(_, false), running→(_, false).
  - `TestMarkPublishesToCreatorTopic`: event `BotID == "user:u1"`, type `session_attention`, payload has no `content`/`text` keys.
  - `TestMarkDoesNotPublishWhenUnchanged`: fake returns `pgx.ErrNoRows` → `changed=false`, `err=nil`, zero events.
  - `TestMarkReadPublishesSessionRead`.
  - `TestAnnotateSetsUnreadOnlyForReturnedRows`.
- [ ] **Step 2:** Run `go test ./internal/chat/attention/ ./internal/chat/event/`. Expected: FAIL (undefined).
- [ ] **Step 3:** Implement. `Mark` uses `now()` for `At`; ErrNoRows → `(false, nil)`.
- [ ] **Step 4:** Run the same tests. Expected: PASS.
- [ ] **Step 5:** Commit `feat(chat): record session attention and publish it per user`.

### Task 3: Runtime waiting-decision observer

**Files:**
- Modify: `internal/agent/runtime/session/activity.go` (setter next to `SetAdmissionObserver`), `internal/agent/runtime/session/manager.go` (`setWaitingDecision`)
- Test: `internal/agent/runtime/session/waiting_observer_test.go`

**Interfaces — Produces:** `(*Manager).SetWaitingDecisionObserver(func(ctx context.Context, botID, sessionID string))`, called synchronously after `runs.SetWaitingDecision` reports `applied`, never when not applied or on error.

- [ ] **Step 1: Failing test** `TestWaitingDecisionObserverFiresOnceWhenApplied` and `TestWaitingDecisionObserverSkipsUnapplied`, using the in-memory ledger fakes the package's existing tests use.
- [ ] **Step 2:** `go test ./internal/agent/runtime/session/ -run WaitingDecisionObserver` → FAIL.
- [ ] **Step 3:** Implement (copy the admission observer's lock pattern).
- [ ] **Step 4:** Same command → PASS; also `go test ./internal/agent/runtime/session/`.
- [ ] **Step 5:** Commit `feat(runtime): observe runs entering waiting_decision`.

### Task 4: Wire attention into the agent runtime

**Files:**
- Modify: `internal/agent/application/service.go` (`SetAttentionMarker`), `internal/agent/application/turn_admission.go` (terminal observer)
- Modify: `cmd/internal/core/providers.go` (`provideAgentService` sets it), `cmd/internal/core/session_runtime.go` (waiting observer), `cmd/internal/core/module.go` (provide `*attention.Service`)
- Test: `internal/agent/application/attention_observer_test.go`

**Interfaces — Consumes:** Task 2 `Mark`, `KindForTerminalState`; Task 3 setter. **Produces:** `type attentionMarker interface { Mark(ctx context.Context, sessionID string, kind attention.Kind) (bool, error) }` and `(*Service).SetAttentionMarker(attentionMarker)`.

- [ ] **Step 1: Failing test** `TestTerminalObserverMarksAttention`: applied `completed` → one `Mark(sid, reply)`; applied `aborted` → none; `Applied=false` → none; marker error is logged, not propagated.
- [ ] **Step 2:** `go test ./internal/agent/application/ -run TerminalObserverMarksAttention` → FAIL.
- [ ] **Step 3:** Add the call as the first statement of the existing terminal observer closure, guarded by `terminal.Applied`. In `provideSessionRuntimeManager` (or a new `fx.Invoke`) set `manager.SetWaitingDecisionObserver(func(ctx, _, sid) { _, _ = svc.Mark(ctx, sid, attention.KindNeedsInput) })`, logging errors. Provide `attention.NewService(queries, hub, time.Now)` in `module.go`.
- [ ] **Step 4:** Test → PASS; `go build ./...`; `go test ./cmd/...`.
- [ ] **Step 5:** Commit `feat(agent): mark session attention when runs finish or wait`.

### Task 5: HTTP API and user SSE

**Files:**
- Modify: `internal/handlers/message.go` (routes + `SetAttentionService`), create `internal/handlers/attention.go` (three handlers)
- Modify: `internal/handlers/session.go` (call `Annotate` on the list page before responding)
- Modify: `cmd/agent/http_providers.go` (inject into message and session handlers)
- Test: `internal/handlers/attention_test.go`
- Generated: `spec/`, `packages/sdk/`

**Interfaces — Produces (HTTP):**
- `PUT /bots/{bot_id}/sessions/{session_id}/read` body `{"through": RFC3339}` → `200 {"read_at": RFC3339}`; 403 unless the caller is the session creator with message access to the bot; 404 for unknown session.
- `GET /users/me/unread` → `200 {"items": [Attention…]}`, filtered to bots the caller can still access.
- `GET /users/me/attention/events` (SSE): first frame `{"type":"attention_ready"}`; `session_attention` frames (Attention fields + `type`); `session_read` frames; `ping` every `sseHeartbeatInterval`; `dropped` frame on buffer overflow. Before each frame, check bot access for `bot_id` (cache per bot for the stream's lifetime).
- Session list items carry `attention_kind`, `unread`.

- [ ] **Step 1: Failing handler tests** with fakes: `TestMarkReadRejectsNonCreator`, `TestUnreadListDropsBotsWithoutAccess`, `TestAttentionStreamDeliversOnlyOwnTopic` (publish to `UserTopic("u2")`, stream for `u1` sees nothing), `TestAttentionStreamStopsAfterAccessRevoked`, `TestListSessionsIncludesUnread`.
- [ ] **Step 2:** `go test ./internal/handlers/ -run 'MarkRead|UnreadList|AttentionStream|ListSessionsIncludesUnread'` → FAIL.
- [ ] **Step 3:** Implement with swaggo annotations (tags `sessions`), reusing `beginSSEResponse`, `writeSSEJSON`, `authorizeBotMessageAccess`.
- [ ] **Step 4:** Tests → PASS; `mise run swagger-generate && mise run sdk-generate`; `go test ./internal/handlers/`; `pnpm run typecheck`.
- [ ] **Step 5:** Commit `feat(api): expose unread sessions, mark-read and attention stream`.

### Task 6: Web unread store and transport

**Files:**
- Create: `apps/web/src/store/unread.ts`, `apps/web/src/store/unread.test.ts`
- Modify: `apps/web/src/composables/api/useChat.message-api.ts` (`streamUserAttentionEvents`), `apps/web/src/composables/api/useChat.types.ts` (event types)

**Interfaces — Produces:**
- `UnreadEntry = { sessionId: string, botId: string, title: string, kind: 'reply' | 'failed' | 'needs_input', attentionAt: string }`
- `useUnreadStore()` (Pinia): state `entries: Record<string, UnreadEntry>`; getters `isUnread(sessionId)`, `kindOf(sessionId)`, `countByBot: Record<string, number>`, `total: number`, `inbox: UnreadEntry[]` (newest first); actions `start()` / `stop()` (seed from `getUsersMeUnread`, then a `useRetryingStream` over `streamUserAttentionEvents`, refetch on `dropped` and reconnect), `markRead(sessionId)` (calls `putBotsByBotIdSessionsBySessionIdRead` with `through = entry.attentionAt`, removes the entry on success), `seedFromSessionList(botId, items)`, and `onAttention(cb: (e: UnreadEntry) => void): () => void` for live events only.
- `streamUserAttentionEvents(signal, onEvent)` mirrors `streamBotSessionsActivityEvents`.

- [ ] **Step 1: Failing Vitest** `unread.test.ts`: seed → `total`; `session_attention` adds and fires `onAttention`; seeding never fires `onAttention`; `session_read` removes; `dropped` triggers a refetch; `markRead` sends `through` equal to the entry's `attentionAt`; `inbox` order newest first.
- [ ] **Step 2:** `pnpm --filter @memohai/web exec vitest run src/store/unread.test.ts` → FAIL.
- [ ] **Step 3:** Implement; start the store once after login (where the chat store bootstraps) and stop it on logout.
- [ ] **Step 4:** Same command → PASS; `pnpm run lint --fix && pnpm run typecheck`.
- [ ] **Step 5:** Commit `feat(web): track unread sessions across bots`.

### Task 7: Viewing detection and mark-read

**Files:**
- Create: `apps/web/src/composables/useSessionViewing.ts`, `apps/web/src/composables/useSessionViewing.test.ts`
- Modify: the component that hosts the active chat panel (`apps/web/src/pages/home/components/dockview/panel-chat.vue`) to call it

**Interfaces — Produces:** `useSessionViewing(sessionId: Ref<string | null>, isActivePanel: Ref<boolean>): { viewing: Ref<boolean> }` — `viewing` = active panel ∧ `document.visibilityState === 'visible'` ∧ `document.hasFocus()`, re-evaluated on `visibilitychange`, window `focus`/`blur`, and ref changes. While `viewing` and `unread.isUnread(id)`, call `unread.markRead(id)`. Also exports `isSessionViewed(sessionId: string): boolean` for the notifier.

- [ ] **Step 1: Failing Vitest**: active+visible+focused marks read; blurred window does not; hidden tab does not; regaining focus marks read; a new attention arriving while viewing marks read immediately.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run → PASS; lint + typecheck.
- [ ] **Step 5:** Commit `feat(web): mark the viewed session as read`.

### Task 8: Dots, inbox, switcher and title count

Read `packages/ui/AGENTS.md` and the sidebar ownership notes first.

**Files:**
- Modify: `apps/web/src/components/sidebar/session-item.vue` (dot in the overlay slot when not streaming and not hovered; semibold title when unread; dot color by kind)
- Create: `apps/web/src/components/sidebar/needs-you-inbox.vue`, `apps/web/src/components/sidebar/needs-you-inbox.test.ts`
- Modify: `apps/web/src/components/sidebar/panel-sessions.vue` (inbox above folders/Recents)
- Modify: `apps/web/src/components/sidebar/bot-switcher.vue` (trigger dot when another bot has unread; `BadgeCount` per bot row)
- Create: `apps/web/src/composables/useOpenSessionAnywhere.ts` — `openSessionAnywhere({ botId, sessionId, title }): Promise<void>`: if `botId` differs, `await chatStore.selectBot(botId)` and `router.push({ name: 'bot', params: { botName } })` like `bot-switcher.vue`'s `handleSelect`; then `workspaceTabs.openSessionChat({ sessionId, title })`.
- Modify: `apps/web/src/App.vue` (or the shell) — `useTitle` prefix `(${total}) ` when `total > 0`
- Modify: `apps/web/src/i18n/locales/{en,zh,ja}.json` — `unread.needsYou`, `unread.showAll`, `unread.kind.reply|failed|needs_input`, `unread.dot`

**Interfaces — Consumes:** Task 6 store getters; Task 7 nothing.

- [ ] **Step 1: Failing Vitest** `needs-you-inbox.test.ts`: hidden when empty; header count; 6 entries → 5 rows + "Show all (6)"; clicking a row calls `openSessionAnywhere` with that entry's bot and session; kind labels use the i18n keys.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement components. **Step 4:** Run → PASS; `pnpm run lint --fix && pnpm run typecheck && mise run lint`.
- [ ] **Step 5:** Commit `feat(web): show unread dots and the needs-you inbox`.

### Task 9: Notifications (web + desktop) and setting

**Files:**
- Create: `apps/web/src/composables/useAttentionNotifications.ts`, `.test.ts`
- Modify: `apps/web/src/pages/appearance/index.vue` (a `SettingsRow` "System notifications" with `Switch`, under the interface section), i18n keys `settings.notifications.*`
- Modify: `apps/web/type.d.ts` (`window.api.desktop.notify`, `setBadgeCount`, `onNotificationClick`)
- Modify: `apps/desktop/src/preload/index.ts`; create `apps/desktop/src/main/attention-notifications.ts`, `.test.ts`; modify `apps/desktop/src/main/index.ts` (IPC handlers)

**Interfaces — Produces:**
- Web: `useAttentionNotifications()` installed once in the shell. Subscribes `unread.onAttention`; skips when `isSessionViewed(e.sessionId)` or the setting is off. Desktop bridge present → `window.api.desktop.notify({ id: e.sessionId + '@' + e.attentionAt, botId, sessionId, title, body })`; else `new Notification(title, { body, tag: e.sessionId })` whose `onclick` focuses and calls `openSessionAnywhere`. Watches `unread.total` → `window.api.desktop.setBadgeCount(total)` when present. Setting: `useLocalStorage('memoh-system-notifications', isDesktop)`; turning it on in web calls `Notification.requestPermission()` and reverts the switch when not `granted`.
- Preload: `desktop.notify(payload: DesktopAttentionNotification): Promise<void>`, `desktop.setBadgeCount(n: number): Promise<void>`, `desktop.onNotificationClick(cb: (p: { botId: string, sessionId: string, title: string }) => void): () => void`. Channels `desktop:attention-notify`, `desktop:set-badge-count`, `desktop:attention-notification-click`.
- Main: `createAttentionNotifier({ show, focusWindow, send }): { notify(payload, sender), setBadge(n) }` — remembers delivered ids (bounded, last 200) so duplicate ids from other windows are ignored; on click focuses the sender's window (fallback: main window) and sends the click channel to it. Both IPC handlers call `assertTrustedRenderer(event)` and validate payload shape.

- [ ] **Step 1: Failing tests**: web — viewed session does not notify; setting off does not notify; desktop bridge path calls `notify` with the composite id; web path uses `tag = sessionId`; badge follows `total`. Desktop main — two `notify` calls with the same id show once; click sends `desktop:attention-notification-click` with the payload; invalid payload ignored.
- [ ] **Step 2:** `pnpm --filter @memohai/web exec vitest run src/composables/useAttentionNotifications.test.ts` and `pnpm --filter @memohai/desktop exec vitest run src/main/attention-notifications.test.ts` → FAIL.
- [ ] **Step 3:** Implement. **Step 4:** Both → PASS; `pnpm run lint --fix && pnpm run typecheck`.
- [ ] **Step 5:** Commit `feat: raise browser and desktop notifications for attention`.

### Task 10: Real-environment verification and PR

- [ ] **Step 1:** `mise run dev`; confirm services healthy and the web build includes the change.
- [ ] **Step 2:** In the in-app browser, run the spec §8 scenarios 1–5 (two bots, two tabs; an approval-requiring tool call for `needs_input`); capture a screenshot of each state (dot, inbox, cross-bot open, title count, notification, read in second tab, no dot while viewing).
- [ ] **Step 3:** Desktop via the electron-vite dev command: native notification once with two windows, click navigation, dock badge; screenshots.
- [ ] **Step 4:** `go test ./...` (unit), the integration commands from Task 1, `pnpm run lint`, `pnpm run typecheck`, `mise run lint`.
- [ ] **Step 5:** Push the branch, open the PR from `.github/pull_request_template.md` (title `feat: 会话未读红点与系统通知`, body links MEMOH-146, Agent identity, screenshots via the gh-pr-images skill, Human QA unchecked); move MEMOH-146 to In Review.
