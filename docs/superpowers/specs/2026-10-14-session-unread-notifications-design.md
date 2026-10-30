# Session Unread Dots and System Notifications — Design (DRAFT)

> Status: **draft, pending review by Rizumu.** Nothing here is approved for
> implementation yet. Linear: [MEMOH-146](https://linear.app/felinic/issue/MEMOH-146).
> Related: [MEMOH-119](https://linear.app/felinic/issue/MEMOH-119) (schedules run
> inside one conversation). Researched on `origin/main` `d06cba896`.

## 1. Problem

When an agent finishes a long run, fails, or stops to wait for an approval, the
user gets no signal unless they are looking at that conversation. Users without
an IM channel (Cloud, Desktop) have no reminder path at all.

Today the codebase has no read state, no browser notifications, no desktop
native notifications and no dock badge. The bot-wide activity SSE
(`/bots/{bot_id}/sessions/events`) is subscribed only for the current bot, so
activity in other bots is invisible.

## 2. Goals

For conversations the user created on the web or desktop, when they need
attention:

1. **In-app unread dots** on the session row and the bot switcher.
2. **Desktop (Electron)**: native notification and dock badge.
3. **Web**: browser `Notification` while a tab is open.

### Non-goals

- Schedule sessions. They will get a neutral (grey) dot driven by the run
  outcome grading (`report_run`) listed as a MEMOH-119 follow-up; until then they
  produce neither dots nor notifications.
- discuss, subagent and IM-originated sessions. IM users already saw the reply
  in IM.
- Push while no tab is open (Service Worker / Web Push).
- "Mark all as read".
- Redesigning the bot switcher into an always-visible icon row (Arc style).
  That is a separate design; this work exposes per-bot unread counts it can use.
- Aggregation across teams. "All bots" means all bots of the current team,
  because every table is team-scoped by RLS.

## 3. Definitions

**Eligible session**: all of
- `visibility = 'user'`
- `type IN ('chat', 'acp_agent')` (covers native, ACP, Codex and Claude Code
  runtimes; excludes `schedule`, `discuss`, `subagent`)
- `channel_type` is NULL, empty or `'local'`
- `created_by_user_id IS NOT NULL`

**Recipient**: the session's `created_by_user_id`. Managers who can list other
members' sessions do not receive dots for them.

**Attention kinds**

| Runtime event | `attention_kind` |
|---|---|
| Run terminal `completed` | `reply` |
| Run terminal `failed` or `lost` | `failed` |
| Run enters `waiting_decision` | `needs_input` |
| Run terminal `aborted` | none (the user stopped it) |

**Unread**: `attention_at IS NOT NULL AND attention_at > COALESCE(read_at, '-infinity')`.
Existing sessions start with `attention_at = NULL`, so the rollout does not
light up old conversations.

## 4. Data model

Next migration number (currently `0167`), plus the same final state in
`0001_init.up.sql`.

```sql
ALTER TABLE public.bot_sessions
  ADD COLUMN IF NOT EXISTS attention_at   TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS attention_kind TEXT
    CHECK (attention_kind IS NULL OR attention_kind IN ('reply', 'failed', 'needs_input'));

CREATE TABLE IF NOT EXISTS public.session_reads (
  team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                         REFERENCES public.teams(id) ON DELETE RESTRICT,
  user_id    UUID        NOT NULL,
  session_id UUID        NOT NULL,
  read_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (team_id, user_id, session_id)
);
-- Composite team FKs to users and bot_sessions (ON DELETE CASCADE, NOT VALID),
-- RLS enabled + forced, and the four team policies, following 0162.
```

- Writing `attention_*` must **not** touch `bot_sessions.updated_at`; sidebar
  recency is driven by messages, not by this bookkeeping (same rule as the
  model-preference columns).
- An index for the summary query:
  `(team_id, created_by_user_id, attention_at DESC) WHERE attention_at IS NOT NULL AND deleted_at IS NULL`.

## 5. Backend

### 5.1 Writing attention

One small application-layer unit, `sessionattention` (name tentative), owns the
eligibility rule and the write:

```go
// Mark records that a session needs its creator's attention and publishes the
// change to that user. Ineligible sessions and duplicate replays are no-ops.
Mark(ctx context.Context, botID, sessionID string, kind Kind, at time.Time) error
```

Hook points:
- **Terminal**: the existing `manager.SetTerminalObserver` callback in
  `internal/agent/application/turn_admission.go`. The observer may replay the
  same run; `Mark` is idempotent because it only moves `attention_at` forward
  (`WHERE attention_at IS NULL OR attention_at < $at`).
- **Waiting decision**: a new `Manager.SetWaitingDecisionObserver(func(botID,
  sessionID string))`, called from `setWaitingDecision` after the ledger write
  applies, wired the same way as `SetAdmissionObserver`.

The observers run outside a request. The write must carry the session's team
in its context for RLS; verify how existing background writers (for example
`logRunResult`) establish it, and reuse that mechanism.

### 5.2 API

| Method & path | Purpose |
|---|---|
| `PUT /bots/{bot_id}/sessions/{session_id}/read` body `{ "through": "<RFC3339>" }` | `read_at = GREATEST(read_at, through)`. `through` is the attention time the client actually showed, so a reply that lands while the request is in flight stays unread. Returns the stored `read_at`. Only the session creator may call it. |
| Session list items (existing endpoint) | add `attention_kind` and `unread` (computed for the requester) so the first sidebar paint has dots without another request. |
| `GET /users/me/unread` | Unread eligible sessions of the current user across all bots of the current team: `bot_id, session_id, title, attention_kind, attention_at`. Capped at 200, newest first. Seeds bot-switcher dots, title count and dock badge. |
| `GET /users/me/attention/events` (SSE) | Live per-user stream, see 5.3. |

Swagger annotations, `mise run swagger-generate`, `mise run sdk-generate`.

### 5.3 User-scoped event stream

- Reuse the in-process `event.Hub`; it already fans out by string key. Add
  `event.UserTopic(userID)` so user-scoped events cannot collide with bot IDs,
  and document that the hub key is a topic, not necessarily a bot.
- Events (no message bodies, matching the activity stream's rule):
  - `session_attention`: `{bot_id, session_id, title, attention_kind, attention_at}`
  - `session_read`: `{session_id, read_at}` — clears dots in other tabs and
    devices.
- Same envelope as the activity stream: `ready` frame, heartbeat `ping`,
  `dropped` frame when the subscriber buffer overflowed (the client refetches
  `/users/me/unread`).
- Authorization: authenticated user at connect time; before each frame,
  re-check bot access with the same helper the activity stream uses
  (`canDeliverSessionActivity`), so revoked access stops delivery.
- Known limit, same as the existing activity SSE: the hub is per process, so a
  multi-replica deployment only delivers to subscribers on the publishing
  replica. Documented, not solved here.

## 6. Web frontend

### 6.1 State

`useUnreadStore` (Pinia), keyed by session id: `{ botId, title, kind, attentionAt }`.
- Seeded from `/users/me/unread`, updated by the user SSE, entry removed on
  `session_read` or after a successful mark-read.
- Refetched on reconnect and on `dropped`.
- Exposes `isUnread(sessionId)`, `unreadCountByBot`, `totalCount`.
- The session list's `unread` field seeds entries for the current bot only, so
  the first paint does not wait for the summary request.

### 6.2 "Viewing" and mark-read

A session is being viewed when **all** hold:
1. it is the session of the active workspace tab;
2. `document.visibilityState === 'visible'`;
3. `document.hasFocus()`.

While viewed, the client calls mark-read with `through = attentionAt`. It
re-evaluates on `visibilitychange`, window `focus`, active tab change, and when
a `session_attention` arrives for the viewed session. Waiting in the
conversation for a run to finish therefore never produces a dot or a
notification.

### 6.3 Where dots appear

Chosen direction: **variant C, the "Needs you" inbox** from the interactive
demo (https://claude.ai/artifact/4wxR22qBb9FkHjCSy32ZSR). Variants A
(switcher dot only) and B (always-visible bot row) were compared and not
chosen; B's idea returns in the bot switcher redesign follow-up.

- **Session row** (`session-item.vue`): in the existing trailing overlay slot.
  While running it shows the spinner; when the run ends and the session is
  unread it shows a dot; on hover the actions button takes the slot as today.
  The title of an unread row is set in the semibold weight so it reads as
  unread even before the eye reaches the dot.
- **Dot color by kind**: `reply` and `failed` use the red alert fill
  (`bg-accent-red-fill`, the `BadgeCount` destructive fill); `needs_input`
  uses the yellow accent (`--accent-yellow`), so "the agent is blocked on you"
  is distinguishable from "there is something to read". The two must also
  differ in lightness, not hue alone. Check both against `packages/ui/AGENTS.md`
  and the UI contract guard during implementation.
- **Bot switcher** (`bot-switcher.vue`): a dot on the trigger when any *other*
  bot has unread sessions; an unread count on each bot in the dropdown list.
- **Counts**: web prefixes `document.title` with `(n) `; desktop sets the dock
  badge to `n`. `n` = total unread sessions. Rows show dots, not numbers.

### 6.4 "Needs you" inbox

A section at the top of the sessions panel, above folders and Recents, that
lists every unread session of the current user across all bots of the team.
It is the main cross-bot surface: the user does not need to open the switcher
to find out where the attention is.

- **Visibility**: rendered only while the unread store is non-empty; it
  disappears when the last entry is read, so the resting sidebar is unchanged.
- **Header**: "Needs you" plus the total count (`BadgeCount` destructive).
- **Row**: the bot's avatar (small), the session title, and the kind as a
  short colored label — replied (muted), run failed (red), waiting for you
  (yellow). Sorted by `attention_at`, newest first.
- **Overflow**: at most 5 rows; a "Show all (n)" row expands the rest in
  place. No separate page.
- **Click**: switches to the row's bot when it differs, opens the session, and
  therefore marks it read (6.2). The row leaves the inbox; the same session's
  dot in Recents clears at the same time.
- **Data**: the unread store (6.1) already holds `botId`, `title`, `kind` and
  `attentionAt` for every bot, so the inbox needs no new API. Bot avatars
  come from the existing bot list query.
- **Schedules later**: the inbox is the intended home for schedule runs that
  need attention once `report_run` grading lands (see 10). Its row model is
  kind-driven, so a schedule kind adds a label and color, not a new surface.

## 7. Notifications

### 7.1 When

Fire only when all hold:
1. a live `session_attention` arrived (the initial summary never notifies);
2. the session is not being viewed (6.2);
3. the device's notification setting is on.

Content: title `"<bot name> · <session title>"`; body is fixed text per kind
(i18n): replied / run failed / waiting for your confirmation. No reply text.

### 7.2 Web (tier 3)

- `new Notification(title, { body, tag: sessionId })`. The shared `tag` makes
  the OS keep one notification per session across tabs.
- Click: `window.focus()`, navigate to the bot and session, close the
  notification.

### 7.3 Desktop (tier 2)

- Preload adds `api.desktop.notify(payload)` and
  `api.desktop.setBadgeCount(n)`.
- The main process shows an Electron `Notification`, de-duplicated by
  `sessionId + attentionAt` across windows (every window's renderer receives the
  same SSE event).
- Click: show and focus the main window, then send the existing
  `renderer:navigate` with the session route.
- Badge: `app.setBadgeCount(n)` (macOS dock; Linux where supported; a no-op on
  Windows is acceptable).
- In the desktop renderer the web `Notification` path is disabled so the main
  process is the only source.

### 7.4 Setting

A "System notifications" row in settings, stored per device in localStorage,
because permission is per device anyway.
- Web: off by default. Turning it on is the user gesture that calls
  `Notification.requestPermission()`. If permission is denied, the row explains
  how to re-enable it in the browser.
- Desktop: on by default; macOS asks for system permission on first use.
- Dots, title count and badge are independent of the setting.

## 8. Testing

Failing tests first, then the implementation.

- **Go unit**: eligibility (chat / acp_agent / local yes; schedule / discuss /
  subagent / IM / internal no); terminal-state → kind mapping including
  `aborted` → none; `Mark` only moves forward; mark-read only moves forward.
- **Go postgres integration**: migration up/down; summary returns only the
  caller's unread sessions within the team; session list `unread`;
  `updated_at` unchanged by `Mark`.
- **Go handler**: user SSE delivers only to the recipient and stops after bot
  access is revoked; mark-read rejects non-creators.
- **Vitest (web)**: unread store transitions (seed, attention, read, dropped);
  viewing detection (tab switch, blur, hidden); notification gating (viewed
  session, setting off, initial summary); inbox ordering, overflow and
  cross-bot open.
- **Vitest (desktop main)**: notification de-duplication, click navigation,
  badge count.
- **Real UI verification** (mandatory, `mise run dev` + in-app browser, with
  screenshots on the PR):
  1. Send a message, switch to another session; on completion the row shows a
     dot, the title shows `(1)`, and a browser notification appears.
  2. Open the session: the dot clears, also in a second tab.
  3. Stay in the session while it runs: no dot, no notification.
  4. In bot B, an unread session in bot A shows on the switcher trigger, in
     the switcher list, and in the "Needs you" inbox; clicking the inbox row
     switches to bot A and opens the session.
  5. Trigger a tool approval: "waiting for your confirmation".
  6. Desktop via electron-vite dev: native notification, click navigation, dock
     badge.

## 9. Risks and open points

- **Team context in observers** (5.1) is the main implementation unknown.
- **Multi-replica delivery** (5.3) inherits the current hub limitation.
- **`lost` runs** are produced by the reaper after a restart; marking them
  `failed` may notify about runs that died long ago. Acceptable for v1; revisit
  if it is noisy.
- **Repeated `waiting_decision`** in one run notifies once per decision, which
  is intended.

## 10. Follow-ups (out of scope)

- Schedule sessions: grey dot plus `report_run` outcome grading (MEMOH-119
  follow-up). Runs graded as needing the user enter the "Needs you" inbox
  (6.4) through a new `attention_kind`; runs graded as nothing to report get
  only the grey dot and never enter the inbox or notify.
- Always-visible bot switcher (Arc-style icon row) using `unreadCountByBot`.
- Web Push for closed tabs.
- Per-session mute and "mark all as read".
