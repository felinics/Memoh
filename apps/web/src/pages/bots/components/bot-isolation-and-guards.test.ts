// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
// Failure Modes Covered:
// 1. `profile/index.vue` initial GET 500 overwrite hazard: if `GET /users/me`
//    fails on initial mount, rendering editable account controls with empty
//    `display_name`/`avatar_url`/`timezone` allows timezone/model selection or
//    auto-save to `PUT /users/me` with blank values and clobber server state.
//    Must render error + Retry, block all writes until initial load succeeds,
//    and restore full editable form after Retry succeeds.
// 2. `bot-access.vue` initial GET 500 false-allow & ACL clobber hazard: if any
//    required ACL endpoint (`default-effect`, `rules`, `channel-managers`)
//    fails on initial mount, falling back to `defaultEffectDraft = "allow"`
//    with empty rules misrepresents a locked-down bot as open and lets mode or
//    member controls overwrite server ACLs. Must render error + Retry, withhold
//    ACL controls/writes, and recover cleanly upon Retry.
// 3. `bot-settings.vue` cross-bot autosave leak, stale `form.name` retention,
//    and broken backup-import navigation:
//    - Triggering an autosave on `bot-1` and immediately switching `props.botId`
//      to `bot-2` (either mid-flight or in the same synchronous tick) must still
//      send the `PUT` to `bot-1`, reset `form.name` / settings for `bot-2`, and
//      prevent `bot-1`'s late completion from corrupting `bot-2`'s `synced` state.
//    - `handleBackupImported(importedBotId)` must resolve `botName` via `getBots`
//      and push `{ name: "bot-detail", params: { botName } }`, falling back to
//      `{ name: "bots" }` when the bot cannot be resolved.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

const sdkMocks = vi.hoisted(() => ({
  // Profile endpoints
  getUsersMe: vi.fn(),
  putUsersMe: vi.fn(),
  putUsersMePassword: vi.fn(),
  // Shared catalog endpoints
  getModels: vi.fn(),
  getProviders: vi.fn(),
  getChannels: vi.fn(),
  // Bot access endpoints
  getBotsByBotIdAclDefaultEffect: vi.fn(),
  getBotsByBotIdAclRules: vi.fn(),
  getBotsByBotIdChannelManagers: vi.fn(),
  getBotsByBotIdAclChannelIdentities: vi.fn(),
  getBotsByBotIdAclChannelTypesByChannelTypeConversations: vi.fn(),
  putBotsByBotIdAclDefaultEffect: vi.fn(),
  postBotsByBotIdAclRules: vi.fn(),
  putBotsByBotIdAclRulesByRuleId: vi.fn(),
  deleteBotsByBotIdAclRulesByRuleId: vi.fn(),
  postBotsByBotIdChannelManagers: vi.fn(),
  deleteBotsByBotIdChannelManagersByChannelIdentityId: vi.fn(),
  // Bot settings endpoints
  getBots: vi.fn(),
  getBotsById: vi.fn(),
  putBotsById: vi.fn(),
  deleteBotsById: vi.fn(),
  getBotsNameAvailability: vi.fn(),
  getBotsByBotIdSettings: vi.fn(),
  putBotsByBotIdSettings: vi.fn(),
  getBotsByBotIdAgents: vi.fn(),
  getAcpProfiles: vi.fn(),
  getSearchProviders: vi.fn(),
  getFetchProviders: vi.fn(),
  getSpeechProviders: vi.fn(),
  getSpeechModels: vi.fn(),
  getTranscriptionProviders: vi.fn(),
  getTranscriptionModels: vi.fn(),
  getVideoProviders: vi.fn(),
  getVideoModels: vi.fn(),
}))

const toastMocks = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@memohai/sdk', () => ({ ...sdkMocks }))
vi.mock('@memohai/sdk/colada', () => ({ getBotsQueryKey: () => ['bots'] }))

vi.mock('@/store/user', () => ({
  useUserStore: () => ({
    userInfo: { id: 'u-cached', username: 'cached' },
    exitLogin: vi.fn(),
    patchUserInfo: vi.fn(),
  }),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({
    refreshBots: vi.fn().mockResolvedValue(undefined),
  }),
}))

vi.mock('@/lib/query-cache-persistence', () => ({
  saveQueryCacheToDiskNow: vi.fn(),
}))

// Child component stand-ins expose deterministic DOM hooks so tests can drive
// real parent event handlers without pulling unrelated 3rd-party popovers.
vi.mock('@/components/timezone-select/index.vue', () => ({
  default: defineComponent({
    props: { modelValue: { type: String, default: '' } },
    emits: ['update:model-value'],
    setup: (props, { emit }) => () => h('button', {
      'type': 'button',
      'data-testid': 'timezone-select',
      'data-value': props.modelValue,
      'onClick': (event: Event) => {
        const next = (event.currentTarget as HTMLElement).dataset.nextTimezone || 'America/New_York'
        emit('update:model-value', next)
      },
    }, props.modelValue),
  }),
}))

vi.mock('./model-select.vue', () => ({
  default: defineComponent({
    props: { modelValue: { type: String, default: '' } },
    emits: ['update:model-value'],
    setup: (props, { emit }) => () => h('button', {
      'type': 'button',
      'data-testid': 'model-select',
      'onClick': () => emit('update:model-value', 'model-2'),
    }, props.modelValue),
  }),
}))

vi.mock('@/pages/profile/components/profile-identity.vue', () => ({
  default: defineComponent({
    props: { displayName: { type: String, default: '' } },
    emits: ['update:display-name', 'save'],
    setup: (props) => () => h('div', { 'data-testid': 'profile-identity' }, props.displayName),
  }),
}))

vi.mock('@/pages/profile/components/password-section.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))

vi.mock('@/pages/profile/components/connected-accounts-section.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))

vi.mock('./bot-user-access.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))

vi.mock('@/components/channel-icon/index.vue', () => ({
  default: defineComponent({ setup: () => () => h('span') }),
}))

vi.mock('@/components/searchable-select-popover/index.vue', () => ({
  default: defineComponent({ setup: () => () => h('div', { 'data-testid': 'member-picker' }) }),
}))

vi.mock('./settings-interaction-card.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))

vi.mock('./settings-context-card.vue', () => ({
  default: defineComponent({
    props: { form: { type: Object, required: true } },
    setup: props => () => h('button', {
      'type': 'button',
      'data-testid': 'toggle-memory',
      'data-memory': String(Boolean((props.form as { memory_enabled?: boolean }).memory_enabled)),
      'onClick': () => {
        const target = props.form as { memory_enabled: boolean }
        target.memory_enabled = !target.memory_enabled
      },
    }, 'toggle-memory'),
  }),
}))

vi.mock('./settings-multimedia-card.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))

vi.mock('./settings-danger-zone.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))

vi.mock('./bot-backup-actions.vue', () => ({
  default: defineComponent({
    emits: ['imported'],
    setup: (_, { emit }) => () => h('button', {
      'type': 'button',
      'data-testid': 'backup-import-trigger',
      'onClick': (event: Event) => {
        const importedId = (event.currentTarget as HTMLElement).dataset.importedId ?? ''
        emit('imported', importedId)
      },
    }, 'import'),
  }),
}))

vi.mock('@felinic/ui', async () => {
  const { defineComponent, h } = await import('vue')
  const passthrough = (tag = 'div') => defineComponent({
    inheritAttrs: false,
    props: {
      label: { type: String, default: '' },
      description: { type: String, default: '' },
      title: { type: String, default: '' },
    },
    setup: (props, { attrs, slots }) => () => h(tag, attrs, [
      props.title ? h('h2', props.title) : null,
      props.label ? h('span', { 'data-row-label': props.label }, props.label) : null,
      props.description ? h('span', props.description) : null,
      slots.trigger?.(),
      slots.content?.(),
      slots.default?.(),
    ]),
  })
  const Input = defineComponent({
    inheritAttrs: false,
    props: { modelValue: { type: [String, Number], default: '' } },
    emits: ['update:modelValue'],
    setup: (props, { attrs, emit }) => () => h('input', {
      ...attrs,
      'data-testid': 'bot-name-input',
      'value': props.modelValue ?? '',
      'onInput': (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
    }),
  })
  const SegmentedControl = defineComponent({
    inheritAttrs: false,
    props: {
      modelValue: { type: String, default: '' },
      items: { type: Array, default: () => [] },
    },
    emits: ['update:model-value'],
    setup: (props, { emit }) => () => h('div', {
      'data-testid': 'acl-mode-control',
      'data-mode': props.modelValue,
    }, (props.items as Array<{ value: string; label: string }>).map(item => h('button', {
      'type': 'button',
      'data-mode-option': item.value,
      'onClick': () => emit('update:model-value', item.value),
    }, item.label))),
  })
  return {
    ActionCard: passthrough(),
    AutoHeight: passthrough(),
    Avatar: passthrough(),
    AvatarFallback: passthrough('span'),
    AvatarImage: passthrough('span'),
    Badge: passthrough('span'),
    Button: passthrough('button'),
    Checkbox: passthrough('input'),
    ConfirmPopover: passthrough(),
    Dialog: passthrough(),
    DialogBody: passthrough(),
    DialogPanel: passthrough(),
    DialogViewHeader: passthrough(),
    Empty: passthrough(),
    EmptyContent: passthrough(),
    EmptyDescription: passthrough(),
    EmptyHeader: passthrough(),
    EmptyTitle: passthrough(),
    InlineLoadingRow: passthrough(),
    Input,
    InputGroup: passthrough(),
    InputGroupAddon: passthrough(),
    InputGroupInput: Input,
    Label: passthrough('label'),
    PageShell: passthrough('section'),
    Popover: passthrough(),
    PopoverContent: passthrough(),
    PopoverTrigger: passthrough(),
    SectionGroup: passthrough(),
    SegmentedControl,
    SettingsRow: passthrough(),
    SettingsSection: passthrough(),
    Skeleton: passthrough(),
    Tabs: passthrough(),
    TabsContent: passthrough(),
    TabsList: passthrough(),
    TabsTrigger: passthrough('button'),
    toast: toastMocks,
    useClipboard: () => ({ copyText: vi.fn().mockResolvedValue(true) }),
  }
})

import ProfilePage from '@/pages/profile/index.vue'
import BotAccess from './bot-access.vue'
import BotSettings from './bot-settings.vue'

function deferred<T = void>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function flushAll() {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('initial-load failure guards & cross-bot settings isolation', () => {
  let app: App | undefined
  let root: HTMLDivElement
  let router: Router

  beforeEach(async () => {
    vi.clearAllMocks()
    const pinia = createPinia()
    setActivePinia(pinia)
    router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/bots', name: 'bots', component: { render: () => null } },
        { path: '/bots/:botName', name: 'bot-detail', component: { render: () => null } },
        { path: '/profile', name: 'profile', component: { render: () => null } },
      ],
    })
    await router.push('/bots/alpha-bot')
    await router.isReady()

    // Default benign responses for shared catalog queries
    sdkMocks.getModels.mockResolvedValue({ data: [] })
    sdkMocks.getProviders.mockResolvedValue({ data: [] })
    sdkMocks.getChannels.mockResolvedValue({ data: [] })
    sdkMocks.getBotsByBotIdAgents.mockResolvedValue({ data: { items: [] } })
    sdkMocks.getAcpProfiles.mockResolvedValue({ data: { items: [] } })
    sdkMocks.getSearchProviders.mockResolvedValue({ data: [] })
    sdkMocks.getFetchProviders.mockResolvedValue({ data: [] })
    sdkMocks.getSpeechProviders.mockResolvedValue({ data: [] })
    sdkMocks.getSpeechModels.mockResolvedValue({ data: [] })
    sdkMocks.getTranscriptionProviders.mockResolvedValue({ data: [] })
    sdkMocks.getTranscriptionModels.mockResolvedValue({ data: [] })
    sdkMocks.getVideoProviders.mockResolvedValue({ data: [] })
    sdkMocks.getVideoModels.mockResolvedValue({ data: [] })
    sdkMocks.getBotsByBotIdAclChannelIdentities.mockResolvedValue({ data: { items: [] } })
  })

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
  })

  function mountWithPlugins(component: ReturnType<typeof defineComponent>) {
    const pinia = createPinia()
    setActivePinia(pinia)
    root = document.createElement('div')
    document.body.appendChild(root)
    app = createApp(component)
    app.config.globalProperties.$t = (key: string) => key
    app.use(pinia)
    app.use(PiniaColada)
    app.use(router)
    app.mount(root)
  }

  it('profile/index.vue: shows error + Retry on initial 500, blocks writes while unloaded, and saves normally after Retry', async () => {
    sdkMocks.getUsersMe
      .mockRejectedValueOnce(Object.assign(new Error('Internal error'), { status: 500, code: 'http.internal_error' }))
      .mockResolvedValueOnce({
        data: {
          id: 'u-1',
          username: 'alice',
          role: 'owner',
          display_name: 'Alice',
          avatar_url: 'https://example.com/a.png',
          timezone: 'Asia/Tokyo',
          title_model_id: 'model-1',
        },
      })
    sdkMocks.putUsersMe.mockResolvedValue({ data: {} })

    mountWithPlugins(defineComponent({ setup: () => () => h(ProfilePage) }))
    await flushAll()

    // Error state rendered; editable profile controls withheld; zero PUT requests
    expect(root.textContent).toContain('settings.loadUserFailed')
    expect(root.querySelector('[data-testid="timezone-select"]')).toBeNull()
    expect(root.querySelector('[data-testid="profile-identity"]')).toBeNull()
    expect(sdkMocks.putUsersMe).not.toHaveBeenCalled()

    const retryBtn = [...root.querySelectorAll('button')].find(btn => btn.textContent?.includes('common.retry'))
    expect(retryBtn).toBeDefined()
    retryBtn!.click()
    await flushAll()

    // Hydrated controls appear after Retry succeeds
    const tzBtn = root.querySelector<HTMLButtonElement>('[data-testid="timezone-select"]')
    expect(tzBtn).not.toBeNull()
    expect(tzBtn!.dataset.value).toBe('Asia/Tokyo')
    expect(root.querySelector('[data-testid="profile-identity"]')?.textContent).toBe('Alice')

    // Changing timezone now persists full hydrated profile instead of blank fields
    tzBtn!.dataset.nextTimezone = 'America/New_York'
    tzBtn!.click()
    await flushAll()

    expect(sdkMocks.putUsersMe).toHaveBeenCalledTimes(1)
    expect(sdkMocks.putUsersMe).toHaveBeenCalledWith(expect.objectContaining({
      body: {
        display_name: 'Alice',
        avatar_url: 'https://example.com/a.png',
        timezone: 'America/New_York',
        title_model_id: 'model-1',
      },
    }))
  })

  it('bot-access.vue: withholds Channel ACL controls & mutations on initial 500 instead of falling back to allow, then recovers on Retry', async () => {
    sdkMocks.getBotsByBotIdAclDefaultEffect.mockResolvedValue({ data: { default_effect: 'deny' } })
    sdkMocks.getBotsByBotIdChannelManagers.mockResolvedValue({ data: { items: [] } })
    sdkMocks.getBotsByBotIdAclRules
      .mockRejectedValueOnce(Object.assign(new Error('DB down'), { status: 500 }))
      .mockResolvedValueOnce({ data: { items: [] } })
    sdkMocks.putBotsByBotIdAclDefaultEffect.mockResolvedValue({ data: { default_effect: 'allow' } })

    mountWithPlugins(defineComponent({ setup: () => () => h(BotAccess, { botId: 'bot-1' }) }))
    await flushAll()

    // Error banner + Retry button shown; ACL mode segmented control & member picker NOT rendered
    expect(root.textContent).toContain('common.loadFailed')
    expect(root.querySelector('[data-testid="acl-mode-control"]')).toBeNull()
    expect(root.querySelector('[data-testid="member-picker"]')).toBeNull()
    expect(sdkMocks.putBotsByBotIdAclDefaultEffect).not.toHaveBeenCalled()
    expect(sdkMocks.postBotsByBotIdAclRules).not.toHaveBeenCalled()
    expect(sdkMocks.postBotsByBotIdChannelManagers).not.toHaveBeenCalled()

    const retryBtn = [...root.querySelectorAll('button')].find(btn => btn.textContent?.includes('common.retry'))
    expect(retryBtn).toBeDefined()
    retryBtn!.click()
    await flushAll()

    // Recovered: renders real server `deny` (whitelist) mode and permits deliberate mode change
    const modeControl = root.querySelector<HTMLElement>('[data-testid="acl-mode-control"]')
    expect(modeControl).not.toBeNull()
    expect(modeControl!.dataset.mode).toBe('deny')

    const allowOption = modeControl!.querySelector<HTMLButtonElement>('[data-mode-option="allow"]')!
    allowOption.click()
    await flushAll()

    expect(sdkMocks.putBotsByBotIdAclDefaultEffect).toHaveBeenCalledTimes(1)
    expect(sdkMocks.putBotsByBotIdAclDefaultEffect).toHaveBeenCalledWith({
      path: { bot_id: 'bot-1' },
      body: { default_effect: 'allow' },
      throwOnError: true,
    })
  })

  it('bot-settings.vue: pins in-flight and same-tick autosaves to original targetBotId, resets form.name on bot switch, and resolves botName on backup import', async () => {
    const bot2FetchGate = deferred<{ data: Record<string, unknown> }>()
    sdkMocks.getBotsById.mockImplementation(({ path }: { path: { id: string } }) => {
      if (path.id === 'bot-1') {
        return Promise.resolve({
          data: { id: 'bot-1', name: 'alpha-bot', display_name: 'Alpha', timezone: 'UTC' },
        })
      }
      return bot2FetchGate.promise
    })
    sdkMocks.getBotsByBotIdSettings.mockImplementation(async ({ path }: { path: { bot_id: string } }) => ({
      data: path.bot_id === 'bot-1'
        ? { memory_enabled: false, chat_model_id: 'gpt-4' }
        : { memory_enabled: false, chat_model_id: 'claude-3' },
    }))

    const saveGate = deferred<{ data: Record<string, unknown> }>()
    sdkMocks.putBotsByBotIdSettings.mockImplementation(() => saveGate.promise)
    sdkMocks.putBotsById.mockResolvedValue({ data: {} })

    const activeBotId = ref('bot-1')
    mountWithPlugins(defineComponent({
      setup: () => () => h(BotSettings, { botId: activeBotId.value }),
    }))
    await flushAll()

    const nameInput = () => root.querySelector<HTMLInputElement>('[data-testid="bot-name-input"]')!
    const memoryToggle = () => root.querySelector<HTMLButtonElement>('[data-testid="toggle-memory"]')!

    expect(nameInput().value).toBe('alpha-bot')
    expect(memoryToggle().dataset.memory).toBe('false')

    // User types an unsaved draft name AND toggles memory_enabled on bot-1
    nameInput().value = 'alpha-unsaved-draft'
    nameInput().dispatchEvent(new Event('input'))
    memoryToggle().click()
    await nextTick()

    // Autosave dispatched to bot-1 and is currently in-flight
    expect(sdkMocks.putBotsByBotIdSettings).toHaveBeenCalledTimes(1)
    expect(sdkMocks.putBotsByBotIdSettings).toHaveBeenCalledWith({
      path: { bot_id: 'bot-1' },
      body: { memory_enabled: true },
      throwOnError: true,
    })

    // Immediately switch to bot-2 while bot-1's save is still in-flight!
    activeBotId.value = 'bot-2'
    await nextTick()
    // Before bot-2's fetch even resolves, bot-1's unsaved name draft & toggle are cleared
    expect(nameInput().value).toBe('')
    expect(memoryToggle().dataset.memory).toBe('false')

    bot2FetchGate.resolve({
      data: { id: 'bot-2', name: 'beta-bot', display_name: 'Beta', timezone: 'Asia/Tokyo' },
    })
    await flushAll()

    // bot-2 hydrates cleanly without inheriting bot-1's draft name or in-flight memory_enabled
    expect(nameInput().value).toBe('beta-bot')
    expect(memoryToggle().dataset.memory).toBe('false')

    // Complete bot-1's slow save: must NOT write to bot-2 or trigger a bogus follow-up PUT on bot-2
    saveGate.resolve({ data: {} })
    await flushAll()
    expect(sdkMocks.putBotsByBotIdSettings).toHaveBeenCalledTimes(1)
    expect(memoryToggle().dataset.memory).toBe('false')

    // Same-tick edit + immediate botId switch: timezone edit on bot-2 flushed to bot-2, not bot-1
    const tzBtn = root.querySelector<HTMLButtonElement>('[data-testid="timezone-select"]')!
    tzBtn.dataset.nextTimezone = 'Europe/London'
    tzBtn.click()
    activeBotId.value = 'bot-1'
    await flushAll()

    expect(sdkMocks.putBotsById).toHaveBeenCalledTimes(1)
    expect(sdkMocks.putBotsById).toHaveBeenCalledWith({
      path: { id: 'bot-2' },
      body: { timezone: 'Europe/London' },
      throwOnError: true,
    })

    // Backup import navigation: resolves imported bot's `name` slug for `params: { botName }`
    sdkMocks.getBots.mockResolvedValueOnce({
      data: { items: [{ id: 'bot-imported-9', name: 'imported-bot-slug' }] },
    })
    const importBtn = root.querySelector<HTMLButtonElement>('[data-testid="backup-import-trigger"]')!
    importBtn.dataset.importedId = 'bot-imported-9'
    importBtn.click()
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe('bot-detail')
      expect(router.currentRoute.value.params.botName).toBe('imported-bot-slug')
    })

    // Backup import fallback: missing bot falls back safely to `{ name: 'bots' }`
    sdkMocks.getBots.mockResolvedValueOnce({ data: { items: [] } })
    importBtn.dataset.importedId = 'unknown-bot-id'
    importBtn.click()
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe('bots')
    })
  })
})
