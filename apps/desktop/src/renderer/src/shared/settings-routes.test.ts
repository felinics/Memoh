import { describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { SettingsRouteSpec } from './settings-routes'

vi.mock('@memohai/web/i18n', () => ({
  i18nRef: (key: string) => ({ value: key }),
}))

function findRoute(path: string, routes: SettingsRouteSpec[]): SettingsRouteSpec | undefined {
  for (const route of routes) {
    if (route.path === path) return route
    const child: SettingsRouteSpec | undefined = findRoute(path, route.children ?? [])
    if (child) return child
  }
  return undefined
}

describe('desktop settings routes', () => {
  it('only registers the current voice settings route', async () => {
    const { SETTINGS_ROUTE_SPECS } = await import('./settings-routes')

    expect(findRoute('/settings/voice', SETTINGS_ROUTE_SPECS)?.name).toBe('voice')
    expect(findRoute('/settings/speech', SETTINGS_ROUTE_SPECS)).toBeUndefined()
    expect(findRoute('/settings/transcription', SETTINGS_ROUTE_SPECS)).toBeUndefined()
  })

  it('registers supermarket detail routes used by reused cards', async () => {
    const { mapSettingsSpecToRoute, SETTINGS_ROUTE_SPECS } = await import('./settings-routes')
    const supermarket = findRoute('/settings/supermarket', SETTINGS_ROUTE_SPECS)

    expect(supermarket?.children).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ name: 'supermarket', path: '' }),
        expect.objectContaining({ name: 'supermarket-package-detail', path: 'skills/:registryId/:packageId' }),
      ]),
    )

    const router = createRouter({
      history: createMemoryHistory(),
      routes: SETTINGS_ROUTE_SPECS.map(mapSettingsSpecToRoute),
    })

    expect(router.resolve({ name: 'supermarket' }).path).toBe('/settings/supermarket')
    expect(router.resolve({
      name: 'supermarket-package-detail',
      params: { registryId: 'registry-id', packageId: 'package-id' },
    }).path).toBe('/settings/supermarket/skills/registry-id/package-id')
  })
})
