import { describe, expect, it } from 'vitest'
import {
  createInitialDesktopUpdateState,
  reduceDesktopUpdateState,
} from './updates'

describe('desktop update state', () => {
  it('marks builds without a configured feed as unavailable', () => {
    expect(createInitialDesktopUpdateState('1.2.3', false)).toEqual({
      status: 'unavailable',
      autoUpdate: true,
      currentVersion: '1.2.3',
      latestVersion: null,
      progress: null,
      error: 'No update feed URL is configured.',
      releaseNotes: null,
    })
  })

  it('tracks the automatic download lifecycle and clamps progress', () => {
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
      status: 'downloading',
      progress: null,
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

  it('carries release notes from available into downloaded and clears them when up-to-date', () => {
    const initial = createInitialDesktopUpdateState('1.2.3')
    const available = reduceDesktopUpdateState(initial, {
      type: 'available',
      latestVersion: '1.3.0',
      releaseNotes: '### Fixes\n- something',
    })
    expect(available.releaseNotes).toBe('### Fixes\n- something')

    // Downloaded without fresh notes keeps what `available` carried.
    const downloaded = reduceDesktopUpdateState(available, { type: 'downloaded' })
    expect(downloaded.releaseNotes).toBe('### Fixes\n- something')

    // A check that finds nothing new clears notes when no download is ready.
    const upToDate = reduceDesktopUpdateState(available, {
      type: 'not-available',
      latestVersion: '1.3.0',
    })
    expect(upToDate.releaseNotes).toBeNull()
  })

  it('keeps a ready update stable during same-version rechecks', () => {
    const downloaded = reduceDesktopUpdateState(createInitialDesktopUpdateState('1.2.3'), {
      type: 'downloaded',
      latestVersion: '1.3.0',
      releaseNotes: '### Fixes',
    })

    expect(reduceDesktopUpdateState(downloaded, { type: 'checking' })).toBe(downloaded)
    expect(reduceDesktopUpdateState(downloaded, {
      type: 'available',
      latestVersion: '1.3.0',
      releaseNotes: '### Fixes',
    })).toBe(downloaded)
    expect(reduceDesktopUpdateState(downloaded, {
      type: 'not-available',
      latestVersion: '1.2.3',
    })).toBe(downloaded)

    const newer = reduceDesktopUpdateState(downloaded, {
      type: 'available',
      latestVersion: '1.4.0',
    })
    expect(newer).toMatchObject({ status: 'downloading', latestVersion: '1.4.0', progress: null })
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
