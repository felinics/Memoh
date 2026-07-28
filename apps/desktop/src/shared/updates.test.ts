import { describe, expect, it } from 'vitest'
import {
  createInitialDesktopUpdateState,
  reduceDesktopUpdateState,
} from './updates'

describe('desktop update state', () => {
  it('marks builds without a configured feed as unavailable', () => {
    expect(createInitialDesktopUpdateState('1.2.3', false)).toEqual({
      status: 'unavailable',
      currentVersion: '1.2.3',
      latestVersion: null,
      progress: null,
      error: 'No update feed URL is configured.',
    })
  })

  it('tracks the manual download lifecycle and clamps progress', () => {
    const initial = createInitialDesktopUpdateState('1.2.3')
    const available = reduceDesktopUpdateState(initial, {
      type: 'available',
      latestVersion: '1.3.0',
    })
    const downloading = reduceDesktopUpdateState(available, {
      type: 'download-progress',
      percent: 105.4,
    })
    const downloaded = reduceDesktopUpdateState(downloading, {
      type: 'downloaded',
    })

    expect(available).toMatchObject({
      status: 'available',
      latestVersion: '1.3.0',
    })
    expect(downloading).toMatchObject({
      status: 'downloading',
      progress: 100,
    })
    expect(downloaded).toMatchObject({
      status: 'downloaded',
      latestVersion: '1.3.0',
      progress: 100,
    })
  })

  it('normalizes update errors for the renderer', () => {
    const state = reduceDesktopUpdateState(
      createInitialDesktopUpdateState('1.2.3'),
      { type: 'error', error: new Error('feed unavailable') },
    )
    expect(state).toMatchObject({
      status: 'error',
      error: 'feed unavailable',
    })
  })
})
