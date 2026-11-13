import { describe, expect, it, vi } from 'vitest'
import {
  applyPostConnectSessionCleanup,
  decidePostConnectNavigation,
  isCurrentServerProbe,
  shouldRedirectOnUnauthorized,
} from './connection-navigation'

describe('startup server probe', () => {
  it('ignores a failed probe after the user has switched servers', () => {
    expect(isCurrentServerProbe(
      'https://old.memoh.example.com',
      'https://new.memoh.example.com',
    )).toBe(false)
  })

  it('handles a failed probe while its server is still configured', () => {
    expect(isCurrentServerProbe(
      'https://memoh.example.com',
      'https://memoh.example.com',
    )).toBe(true)
  })
})

describe('post-connect navigation', () => {
  it('clears authentication and opens login after switching servers', () => {
    expect(decidePostConnectNavigation({
      changed: true,
      hasToken: true,
      returnTo: '/settings/about',
    })).toEqual({
      clearAuth: true,
      animateLogin: true,
      destination: { name: 'Login' },
    })
  })

  it('keeps authentication and returns to the requested page on the same server', () => {
    expect(decidePostConnectNavigation({
      changed: false,
      hasToken: true,
      returnTo: '/settings/about',
    })).toEqual({
      clearAuth: false,
      animateLogin: false,
      destination: '/settings/about',
    })
  })

  it('opens login without clearing state when the same server has no token', () => {
    expect(decidePostConnectNavigation({
      changed: false,
      hasToken: false,
      returnTo: '/settings/about',
    })).toEqual({
      clearAuth: false,
      animateLogin: true,
      destination: { name: 'Login' },
    })
  })

  it('falls back to home for an invalid return path', () => {
    expect(decidePostConnectNavigation({
      changed: false,
      hasToken: true,
      returnTo: '//example.com',
    }).destination).toBe('/')
  })
})

describe('server-switch session cleanup & unauthorized route preservation', () => {
  // Failure modes:
  // 1. Switching servers (`clearAuth: true`) only removed `localStorage['token']`
  //    without calling `userStore.exitLogin()`, leaving previous server's `userInfo`,
  //    `queryCache`, `user`, and `sessionStorage` alive across hosts.
  // 2. Re-probing the same server (`clearAuth: false`) accidentally wiped an active session.
  // 3. Background 401 from old server while on `/connect` or `/oauth/*` hijacked
  //    Desktop away from `/connect` into `/login`.
  it('runs exitLogin(), removes persisted user record, and clears sessionStorage when switching servers', () => {
    const exitLogin = vi.fn()
    const removeItem = vi.fn()
    const clear = vi.fn()

    applyPostConnectSessionCleanup(true, {
      exitLogin,
      localStorage: { removeItem },
      sessionStorage: { clear },
    })

    expect(exitLogin).toHaveBeenCalledTimes(1)
    expect(removeItem).toHaveBeenCalledWith('user')
    expect(clear).toHaveBeenCalledTimes(1)
  })

  it('preserves active session state when reconnecting to the same server', () => {
    const exitLogin = vi.fn()
    const removeItem = vi.fn()
    const clear = vi.fn()

    applyPostConnectSessionCleanup(false, {
      exitLogin,
      localStorage: { removeItem },
      sessionStorage: { clear },
    })

    expect(exitLogin).not.toHaveBeenCalled()
    expect(removeItem).not.toHaveBeenCalled()
    expect(clear).not.toHaveBeenCalled()
  })

  it('suppresses /login redirect on /connect and /oauth/* while redirecting normal routes', () => {
    expect(shouldRedirectOnUnauthorized('/connect')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/oauth/mcp-callback')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/oauth/provider')).toBe(false)
    expect(shouldRedirectOnUnauthorized('/')).toBe(true)
    expect(shouldRedirectOnUnauthorized('/bot/kitty-3')).toBe(true)
    expect(shouldRedirectOnUnauthorized(undefined)).toBe(true)
  })
})
