// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type RouteRecordRaw } from 'vue-router'
import { createAppRoutes } from '@/routes'
import {
  buildLoginRedirect,
  buildOnboardingRedirect,
  guardAuthenticatedRoute,
  loginRedirectForCurrentRoute,
  normalizeRoutePath,
  onboardingRedirectForCurrentRoute,
  sanitizeReturnTo,
  shouldRedirectOnUnauthorized,
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
    const path = normalizeRoutePath(to.path)
    if (platform === 'desktop' && path === '/connect') return true
    return guardAuthenticatedRoute(to, deps)
  })
  return router
}

describe('sanitizeReturnTo & redirect builders', () => {
  it('preserves valid in-app deep links including query parameters and hashes', () => {
    expect(sanitizeReturnTo('/settings/providers?tab=voice')).toBe('/settings/providers?tab=voice')
    expect(sanitizeReturnTo('/bot/my-bot?agent=codex#composer')).toBe('/bot/my-bot?agent=codex#composer')
    expect(sanitizeReturnTo('/bot/my-bot#payload')).toBe('/bot/my-bot#payload')
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
    '/Login/',
    '/login?return_to=%2Fsettings%2Fproviders',
    '/settings/../login',
    '/settings/%2e%2e%2flogin',
    '/%2flogin',
    '/oauth/mcp/callback?code=1&state=2',
    '/OAuth/mcp/callback',
    '/connect?returnTo=%2Fsettings%2Fabout',
    '/Connect/',
    '/onboarding?step=2',
    '/Onboarding/',
    '/providers/connect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/Providers/Connect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/providers/connect/#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/%50roviders/connect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/providers//connect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/providers%2fconnect#payload=eyJhcGlfa2V5Ijoic2stc2VjcmV0In0',
    '/api/providers/1/oauth/authorize',
    '/API/users/me',
    '/dev/components',
    `/${'a'.repeat(2050)}`,
  ])('rejects unsafe, looping, or secret-bearing return_to input %o back to /', (input) => {
    expect(sanitizeReturnTo(input)).toBe('/')
    expect(toReturnToParam(input)).toBeUndefined()
    expect(buildLoginRedirect(input)).toEqual({ path: '/login' })
  })

  it('omits redundant ?return_to=/ while encoding non-root targets with path /login', () => {
    expect(buildLoginRedirect('/')).toEqual({ path: '/login' })
    expect(buildLoginRedirect('/settings/providers?tab=voice')).toEqual({
      path: '/login',
      query: { return_to: '/settings/providers?tab=voice' },
    })
    expect(buildOnboardingRedirect('/')).toEqual({ path: '/onboarding' })
    expect(buildOnboardingRedirect('/settings/providers?tab=voice')).toEqual({
      path: '/onboarding',
      query: { return_to: '/settings/providers?tab=voice' },
    })
  })

  it('matches auth boundary paths case-insensitively and ignores trailing slashes', () => {
    expect(shouldRedirectOnUnauthorized('/login')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/Login/')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/Connect/')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/OAuth/mcp/callback/')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/settings/providers')).toBe(true)
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

    token = 'jwt-token'
    await router.replace(sanitizeReturnTo(router.currentRoute.value.query.return_to))
    expect(router.currentRoute.value.path).toBe('/settings/providers')
    expect(router.currentRoute.value.query).toEqual({ tab: 'voice' })
    expect(router.currentRoute.value.hash).toBe('#models')
  })

  it('handles case and trailing-slash variants on /login and /onboarding boundaries', async () => {
    const router = makeGuardedRouter(platform, {
      readToken: () => 'jwt-token',
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => true,
    })

    await router.push('/Login/?return_to=%2Fsettings%2Fproviders%3Ftab%3Dvoice')
    expect(router.currentRoute.value.fullPath).toBe('/settings/providers?tab=voice')

    await router.push('/Onboarding/?return_to=%2Fsettings%2Fappearance')
    expect(router.currentRoute.value.fullPath).toBe('/settings/appearance')

    await router.push('/login?return_to=https%3A%2F%2Fevil.example')
    expect(router.currentRoute.value.fullPath).toBe('/')
  })

  it('carries return_to into /onboarding and preserves nested return_to on logged-out onboarding reloads', async () => {
    let token: string | null = null
    let onboardingCompleted = false
    const router = makeGuardedRouter(platform, {
      readToken: () => token,
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => onboardingCompleted,
    })

    // Logged-out visit to /onboarding?step=2&return_to=... unwraps the nested target.
    await router.push('/onboarding?step=2&return_to=%2Fsettings%2Fappearance')
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query).toEqual({
      return_to: '/settings/appearance',
    })

    token = 'jwt-token'
    await router.replace(sanitizeReturnTo(router.currentRoute.value.query.return_to))
    expect(router.currentRoute.value.path).toBe('/onboarding')
    expect(router.currentRoute.value.query).toEqual({
      return_to: '/settings/appearance',
    })

    onboardingCompleted = true
    await router.replace('/onboarding?step=1&return_to=%2Fsettings%2Fappearance')
    expect(router.currentRoute.value.fullPath).toBe('/settings/appearance')
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

  it.each([
    '/providers/connect#payload=sk-live-secret',
    '/Providers/Connect#payload=sk-live-secret',
    '/providers/connect/#payload=sk-live-secret',
    '/%50roviders/connect#payload=sk-live-secret',
    '/providers//connect#payload=sk-live-secret',
  ])('never leaks fragment secrets from %s into login query', async (url) => {
    const router = makeGuardedRouter(platform, {
      readToken: () => null,
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => true,
    })

    await router.push(url)
    expect(router.currentRoute.value.fullPath).toBe('/login')
    expect(router.currentRoute.value.query).toEqual({})
  })

  it('redirects unknown routes and removed settings subpaths to home instead of rendering blank overlays', async () => {
    const router = makeGuardedRouter(platform, {
      readToken: () => 'jwt-token',
      readUserRole: () => 'admin',
      hasOnboardingInProgress: () => false,
      ensureOnboarding: async () => true,
    })

    await router.push('/unknown-legacy-route')
    expect(router.currentRoute.value.path).toBe('/')

    await router.push('/settings/removed-platform-page')
    expect(router.currentRoute.value.path).toBe('/')
  })
})

describe('current-route redirect unwrapping', () => {
  it('unwraps nested return_to from /onboarding and /login on committed routes and cold-start URLs', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: stubComponents(createAppRoutes('web')),
    })

    // Cold-start (uncommitted START_LOCATION): reads window.location and unwraps /onboarding?return_to=...
    window.history.replaceState({}, '', '/onboarding?step=2&return_to=%2Fsettings%2Fappearance')
    expect(loginRedirectForCurrentRoute(router)).toEqual({
      path: '/login',
      query: { return_to: '/settings/appearance' },
    })

    // Committed /login?return_to=... during login transition: unwraps return_to for onboarding redirect.
    await router.push('/login?return_to=%2Fsettings%2Fproviders%3Ftab%3Dvoice')
    expect(onboardingRedirectForCurrentRoute(router)).toEqual({
      path: '/onboarding',
      query: { return_to: '/settings/providers?tab=voice' },
    })

    // Committed /onboarding?step=2&return_to=...: unwraps return_to on 401.
    await router.push('/onboarding?step=2&return_to=%2Fsettings%2Fproviders%3Ftab%3Dvoice')
    expect(loginRedirectForCurrentRoute(router)).toEqual({
      path: '/login',
      query: { return_to: '/settings/providers?tab=voice' },
    })
  })
})
