import { describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type RouteRecordRaw } from 'vue-router'
import { createAppRoutes, settingsPageLoaders } from './routes'

vi.mock('./i18n', () => ({ i18nRef: (key: string) => ({ value: key }) }))

// Exercise navigation and redirects without mounting pages or contacting a server.
// Production builds validate the real lazy imports for both hosts.
function navigationRoutes(routes: RouteRecordRaw[]): RouteRecordRaw[] {
  return routes.map(route => ({
    ...route,
    ...('component' in route ? { component: { render: () => null } } : {}),
    ...(route.children ? { children: navigationRoutes(route.children) } : {}),
  } as RouteRecordRaw))
}

function makeRouter(platform: 'web' | 'desktop') {
  return createRouter({
    history: createMemoryHistory(),
    routes: navigationRoutes(createAppRoutes(platform)),
  })
}

describe.each(['web', 'desktop'] as const)('%s shared routes', (platform) => {
  it('opens App details from the marketplace and follows dependency links', async () => {
    const router = makeRouter(platform)
    await router.push({ name: 'supermarket' })
    await router.push({
      name: 'supermarket-app-detail',
      params: { registryId: 'memoh', appId: 'github' },
      query: { botId: 'my-bot' },
    })
    expect(router.currentRoute.value.path).toBe('/settings/supermarket/memoh/github')
    expect(router.currentRoute.value.params.appId).toBe('github')
    await router.push({
      name: 'supermarket-app-detail',
      params: { registryId: 'memoh', appId: 'git' },
      query: router.currentRoute.value.query,
    })
    expect(router.currentRoute.value.fullPath).toBe('/settings/supermarket/memoh/git?botId=my-bot')
    await router.push({ name: 'supermarket-category', params: { categoryId: 'developer-tools' } })
    expect(router.currentRoute.value.path).toBe('/settings/supermarket/category/developer-tools')
  })

  it('preserves settings metadata and redirects legacy voice links', async () => {
    const router = makeRouter(platform)
    expect(router.resolve({ name: 'people' }).meta.adminOnly).toBe(true)
    await router.push('/settings/speech')
    expect(router.currentRoute.value.name).toBe('voice')
    await router.push('/settings/transcription')
    expect(router.currentRoute.value.name).toBe('voice')
  })
})

it('keeps the Web mobile settings index while Desktop opens Bots', async () => {
  const web = makeRouter('web')
  const desktop = makeRouter('desktop')
  await web.push('/settings')
  await desktop.push('/settings')
  expect(web.currentRoute.value.path).toBe('/settings')
  expect(desktop.currentRoute.value.name).toBe('bots')
})

it('preserves Desktop session links through the legacy chat redirect', async () => {
  const router = makeRouter('desktop')
  await router.push('/chat/my-bot/my-session')
  expect(router.currentRoute.value.path).toBe('/bot/my-bot/my-session')
  expect(router.currentRoute.value.params.sessionId).toBe('my-session')
})

// The prefetch warmup list must stay in lockstep with the settings route table:
// adding a settings page without adding its loader (or vice versa) fails here.
// Plain-object components (render stubs) and redirects have nothing to warm.
it('prefetch loaders cover exactly the settings lazy components', () => {
  const components = new Set<unknown>()
  const collect = (list: RouteRecordRaw[]) => {
    for (const route of list) {
      if (typeof route.component === 'function') components.add(route.component)
      if (route.children) collect(route.children)
    }
  }
  const routes = createAppRoutes('web')
  const settings = routes.find(route => route.path === '/settings')
  expect(settings).toBeTruthy()
  if (typeof settings?.component === 'function') components.add(settings.component)
  collect(settings?.children ?? [])
  expect(new Set(settingsPageLoaders)).toEqual(components)
})
