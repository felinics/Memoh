// Single source of truth for desktop settings routes. The renderer mounts
// these real settings components under `/settings/*` while keeping chat
// mounted. Add current reusable settings pages here when desktop needs to
// expose them; do not mirror retired web redirects as new desktop routes.

import { h, type Component } from 'vue'
import { RouterView, type RouteRecordRaw } from 'vue-router'
import { i18nRef } from '@memohai/web/i18n'

export interface SettingsRouteSpec {
  name?: string
  path: string
  loader?: () => Promise<Component | { default: Component }>
  meta?: {
    breadcrumb?: string | { value: string } | ((route: { params: Record<string, string | string[] | undefined> }) => string | undefined)
    adminOnly?: boolean
  }
  children?: SettingsRouteSpec[]
}

export const SETTINGS_ROUTE_SPECS: SettingsRouteSpec[] = [
  {
    path: '/settings/bots',
    meta: { breadcrumb: i18nRef('sidebar.bots') },
    children: [
      {
        name: 'bots',
        path: '',
        loader: () => import('@memohai/web/pages/bots/index.vue'),
      },
      {
        name: 'bot-new',
        path: 'new',
        loader: () => import('@memohai/web/pages/bots/new.vue'),
        meta: { breadcrumb: i18nRef('bots.createBot') }
      },
      {
        name: 'bot-create-progress',
        path: 'new/progress',
        loader: () => import('@memohai/web/pages/bots/new-progress.vue'),
        meta: { breadcrumb: i18nRef('bots.createBot') }
      },
      {
        name: 'bot-detail',
        path: ':botName',
        loader: () => import('@memohai/web/pages/bots/detail.vue'),
        meta: { breadcrumb: (route: { params: { botName?: string } }) => route.params.botName }
      },
    ]
  },
  {
    name: 'providers',
    path: '/settings/providers',
    loader: () => import('@memohai/web/pages/providers/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.providers') }
  },
  {
    name: 'runtimes',
    path: '/settings/runtimes',
    loader: () => import('@memohai/web/pages/runtimes/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.runtimes') }
  },
  {
    name: 'people',
    path: '/settings/people',
    loader: () => import('@memohai/web/pages/people/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.people'), adminOnly: true }
  },
  {
    name: 'web-search',
    path: '/settings/web-search',
    loader: () => import('@memohai/web/pages/web-search/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.webSearch') }
  },
  {
    name: 'memory',
    path: '/settings/memory',
    loader: () => import('@memohai/web/pages/memory/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.memory') }
  },
  {
    name: 'voice',
    path: '/settings/voice',
    loader: () => import('@memohai/web/pages/voice/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.voice') }
  },
  {
    name: 'email',
    path: '/settings/email',
    loader: () => import('@memohai/web/pages/email/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.email') }
  },
  {
    name: 'usage',
    path: '/settings/usage',
    loader: () => import('@memohai/web/pages/usage/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.usage') }
  },
  {
    name: 'appearance',
    path: '/settings/appearance',
    loader: () => import('@memohai/web/pages/appearance/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.appearance') }
  },
  {
    name: 'keyboard',
    path: '/settings/keyboard',
    loader: () => import('@memohai/web/pages/keyboard-shortcuts/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.keyboard') }
  },
  {
    name: 'profile',
    path: '/settings/profile',
    loader: () => import('@memohai/web/pages/profile/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.profile') }
  },
  {
    path: '/settings/supermarket',
    meta: { breadcrumb: i18nRef('sidebar.supermarket') },
    children: [
      {
        name: 'supermarket',
        path: '',
        loader: () => import('@memohai/web/pages/supermarket/index.vue'),
      },
      {
        name: 'supermarket-package-detail',
        path: 'skills/:registryId/:packageId',
        loader: () => import('@memohai/web/pages/supermarket/package-detail.vue'),
        meta: {
          breadcrumb: (route) => String(route.params.packageId ?? ''),
        },
      },
    ],
  },
  {
    name: 'about',
    path: '/settings/about',
    loader: () => import('@memohai/web/pages/about/index.vue'),
    meta: { breadcrumb: i18nRef('sidebar.about') }
  },
]

// Default landing path used by the chat router's `/settings` redirect and by
// generic desktop settings open requests.
export const SETTINGS_DEFAULT_PATH = '/settings/bots'

export function mapSettingsSpecToRoute(spec: SettingsRouteSpec): RouteRecordRaw {
  return {
    path: spec.path,
    component: spec.loader ?? { render: () => h(RouterView) },
    ...(spec.name ? { name: spec.name } : {}),
    ...(spec.meta ? { meta: spec.meta } : {}),
    ...(spec.children ? { children: spec.children.map(mapSettingsSpecToRoute) } : {}),
  } satisfies RouteRecordRaw
}
