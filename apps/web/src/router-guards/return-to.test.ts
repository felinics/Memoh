// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type RouteRecordRaw } from 'vue-router'
import { createAppRoutes } from '@/routes'
import {
  buildLoginRedirect,
  buildOnboardingRedirect,
  currentReturnToTarget,
  guardAuthenticatedRoute,
  sanitizeReturnTo,
  toReturnToParam,
  type AuthRouteGuardDeps,
} from './return-to'

vi.mock('@/i18n', () => ({ i18nRef: (key: string) => ({ value: key }) }))

function stubComponents(routes: RouteRecordRaw[]): RouteRecordRaw[] {
  return routes.map(route => ({
    ...route,
    ...('component' in route ? { component: { render: () => null } } : {}),
    ...(route.children ? { children: stubComponents(route.children) } : {}),
  } as RouteRecordRaw))
}

function makeGuardedRouter(platform: 'web' | 'desktop', deps: AuthRouteGuardDeps) {
  const extra: RouteRecordRaw[] = platform === 'desktop'
    ? [{ path: '/connect', name: 'ConnectServer', component: { render: () => null } }]
    : []
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [...extra, ...stubComponents(createAppRoutes(platform))],
  })
  router.beforeEach(async (to) => {
    if (platform === 'desktop' && to.path === '/connect') return true
    return guardAuthenticatedRoute(to, deps)
  })
  return router
}

describe('sanitizeReturnTo & redirect builders', () => {
  it('preserves valid in-app deep links including query parameters and hashes', () => {
    expect(sanitizeReturnTo('/settings/providers?tab=voice')).toBe('/settings/providers?tab=voice')
    expect(sanitizeReturnTo('/bot/my-bot?agent=codex#composer')).toBe('/bot/my-bot?agent=codex#composer')
    expect(sanitizeReturnTo(['/settings/appearance?theme=dark', '/ignored'])).toBe('/settings/appearance?theme=dark')
    expect(sanitizeReturnTo('/?welcome=1')).toBe('/?welcome=1')
  })

  it.each([
    undefined,
    null,
    '',
    'settings/providers',
    ' /settings/providers',
    'https://evil.example/settings',
    'http://evil.example',
    'javascript:alert(1)',
    '//evil.example/path',
    '///evil.example',
    '/..//evil.example',
    '/.//evil.example',
    '/foo/..//evil.example',
    '/%2f/evil.example',
    '/..%2f/evil.example',
    '/%5cevil.example',
    '/\\evil.example',
    '/settings\\evil.example',
    '/\t/evil.example',
    '/\n/evil.example',
    '/settings\r\nInjected:1',
    '/login',
    '/login?return_to=%2Fsettings%2Fproviders',
    '/settings/../login',
    '/settings/%2e%2e%2flogin',
    '/%2flogin',
    '/oauth/mcp/callback?code=1&state=2',
    '/connect?returnTo=%2Fsettings%2Fabout',
    '/onboarding?step=2',
    '/providers/connect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/providers%2fconnect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/api/providers/1/oauth/authorize',
    `/${'a'.repeat(2050)}`,
  ])('rejects unsafe, looping, or secret-bearing return_to input %o back to /', (input) => {
    expect(sanitizeReturnTo(input)).toBe('/')
    expect(toReturnToParam(input)).toBeUndefined()
    expect(buildLoginRedirect(input)).toEqual({ name: 'Login' })
  })

  it('omits redundant ?return_to=/ while encoding non-root targets', () => {
    expect(buildLoginRedirect('/')).toEqual({ name: 'Login' })
    expect(buildLoginRedirect('/settings/providers?tab=voice')).toEqual({
      name: 'Login',
      query: { return_to: '/settings/providers?tab=voice' },
    })
    expect(buildOnboardingRedirect('/')).toEqual({ path: '/onboarding' })
    expect(buildOnboardingRedirect('/settings/providers?tab=voice')).toEqual({
      path: '/onboarding',
      query: { return_to: '/settings/providers?tab=voice' },
    })
  })
})

describe.each(['web', 'desktop'] as const)('%s auth route guard return_to flow', (platform) => {
  it('redirects unauthenticated deep links to /login?return_to=... and restores query + hash after login', async () => {
    let token: string | null = null
    const router = makeGuardedRouter(platform, {
      readToken: () => token,
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => true,
    })

    await router.push('/settings/providers?tab=voice#models')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query).toEqual({
      return_to: '/settings/providers?tab=voice#models',
    })

    // Simulate successful credential sign-in followed by login page's replace().
    token = 'jwt-token'
    await router.replace(sanitizeReturnTo(router.currentRoute.value.query.return_to))
    expect(router.currentRoute.value.path).toBe('/settings/providers')
    expect(router.currentRoute.value.query).toEqual({ tab: 'voice' })
    expect(router.currentRoute.value.hash).toBe('#models')
  })

  it('bounces an already-authenticated visit to /login?return_to=... straight to the sanitized destination', async () => {
    const router = makeGuardedRouter(platform, {
      readToken: () => 'jwt-token',
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => true,
    })

    await router.push('/login?return_to=%2Fsettings%2Fproviders%3Ftab%3Dvoice')
    expect(router.currentRoute.value.fullPath).toBe('/settings/providers?tab=voice')

    await router.push('/login?return_to=https%3A%2F%2Fevil.example')
    expect(router.currentRoute.value.fullPath).toBe('/')
  })

  it('carries return_to into /onboarding when first-run setup is incomplete', async () => {
    let token: string | null = null
    let onboardingCompleted = false
    const router = makeGuardedRouter(platform, {
      readToken: () => token,
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => onboardingCompleted,
    })

    await router.push('/settings/providers?tab=voice')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query).toEqual({
      return_to: '/settings/providers?tab=voice',
    })

    token = 'jwt-token'
    await router.replace(sanitizeReturnTo(router.currentRoute.value.query.return_to))
    expect(router.currentRoute.value.path).toBe('/onboarding')
    expect(router.currentRoute.value.query).toEqual({
      return_to: '/settings/providers?tab=voice',
    })

    // Visiting /onboarding?return_to=... after completion forwards to return_to.
    onboardingCompleted = true
    await router.replace('/onboarding?step=1&return_to=%2Fsettings%2Fproviders%3Ftab%3Dvoice')
    expect(router.currentRoute.value.fullPath).toBe('/settings/providers?tab=voice')
  })

  it('preserves return_to when an expired token is cleared by a 401 during ensureOnboarding', async () => {
    let token: string | null = 'expired-jwt'
    const router = makeGuardedRouter(platform, {
      readToken: () => token,
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => {
        token = null
        return true
      },
    })

    await router.push('/settings/providers?tab=voice')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query).toEqual({
      return_to: '/settings/providers?tab=voice',
    })
  })

  it('never leaks /providers/connect#payload=... fragment secrets into login query', async () => {
    const router = makeGuardedRouter(platform, {
      readToken: () => null,
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => true,
    })

    await router.push('/providers/connect#payload=sk-live-secret')
    expect(router.currentRoute.value.fullPath).toBe('/login')
    expect(router.currentRoute.value.query).toEqual({})
  })
})

describe('currentReturnToTarget', () => {
  it('extracts nested return_to when a 401 fires mid-onboarding', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: stubComponents(createAppRoutes('web')),
    })
    await router.push('/onboarding?step=2&return_to=%2Fsettings%2Fproviders%3Ftab%3Dvoice')
    expect(currentReturnToTarget(router)).toBe('/settings/providers?tab=voice')
  })
})
