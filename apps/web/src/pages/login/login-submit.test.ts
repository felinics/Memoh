import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { submitLogin, type SubmitLoginDependencies } from './login-submit'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function createDependencies(overrides: Partial<SubmitLoginDependencies> = {}): SubmitLoginDependencies {
  return {
    authenticate: vi.fn().mockResolvedValue({
      data: {
        access_token: 'token',
        user_id: 'user-1',
        username: 'alice',
        display_name: 'Alice',
        role: 'admin',
        avatar_url: '',
        timezone: 'UTC',
      },
    }),
    applyLogin: vi.fn(),
    navigateHome: vi.fn().mockResolvedValue(undefined),
    notifyInvalidCredentials: vi.fn(),
    ...overrides,
  }
}

describe('submitLogin', () => {
  it('ignores a submission while another login is already in progress', async () => {
    const isSubmitting = ref(true)
    const dependencies = createDependencies()

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(false)

    expect(dependencies.authenticate).not.toHaveBeenCalled()
    expect(dependencies.applyLogin).not.toHaveBeenCalled()
    expect(isSubmitting.value).toBe(true)
  })

  it('keeps the form busy until the successful navigation settles', async () => {
    const isSubmitting = ref(false)
    const navigation = deferred<void>()
    const dependencies = createDependencies({
      navigateHome: vi.fn(() => navigation.promise),
    })

    const firstSubmission = submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)
    await vi.waitFor(() => expect(dependencies.navigateHome).toHaveBeenCalledTimes(1))

    expect(isSubmitting.value).toBe(true)
    expect(dependencies.authenticate).toHaveBeenCalledTimes(1)
    expect(dependencies.applyLogin).toHaveBeenCalledTimes(1)
    expect(dependencies.navigateHome).toHaveBeenCalledTimes(1)

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(false)
    expect(dependencies.authenticate).toHaveBeenCalledTimes(1)

    navigation.resolve()
    await expect(firstSubmission).resolves.toBe(true)
    expect(isSubmitting.value).toBe(false)
  })

  it('notifies and releases the form when authentication fails', async () => {
    const isSubmitting = ref(false)
    const dependencies = createDependencies({
      authenticate: vi.fn().mockResolvedValue({ data: null }),
    })

    await expect(submitLogin({ username: 'alice', password: 'bad' }, isSubmitting, dependencies)).resolves.toBe(false)

    expect(dependencies.applyLogin).not.toHaveBeenCalled()
    expect(dependencies.navigateHome).not.toHaveBeenCalled()
    expect(dependencies.notifyInvalidCredentials).toHaveBeenCalledTimes(1)
    expect(isSubmitting.value).toBe(false)
  })

  it('reports the browser timezone after login and applies the server-confirmed value', async () => {
    const isSubmitting = ref(false)
    const callOrder: string[] = []
    const dependencies = createDependencies({
      applyLogin: vi.fn(() => callOrder.push('applyLogin')),
      syncTimezone: vi.fn(async () => {
        callOrder.push('syncTimezone')
        return { timezone: 'Asia/Shanghai' }
      }),
      navigateHome: vi.fn(async () => {
        callOrder.push('navigateHome')
      }),
      readBrowserTimezone: () => 'Asia/Shanghai',
      applySyncedTimezone: vi.fn(),
    })

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(true)

    // Credentials must be ready (token applied) before the profile request,
    // and the store only updates after the server confirms.
    expect(callOrder).toEqual(['applyLogin', 'syncTimezone', 'navigateHome'])
    expect(dependencies.syncTimezone).toHaveBeenCalledWith('Asia/Shanghai')
    expect(dependencies.applySyncedTimezone).toHaveBeenCalledWith('Asia/Shanghai')
    expect(dependencies.notifyInvalidCredentials).not.toHaveBeenCalled()
    expect(isSubmitting.value).toBe(false)
  })

  it('skips the sync request when the browser timezone already matches the server', async () => {
    const isSubmitting = ref(false)
    const dependencies = createDependencies({
      readBrowserTimezone: () => 'UTC',
      syncTimezone: vi.fn(),
      applySyncedTimezone: vi.fn(),
    })

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(true)

    expect(dependencies.syncTimezone).not.toHaveBeenCalled()
    expect(dependencies.applySyncedTimezone).not.toHaveBeenCalled()
    expect(dependencies.navigateHome).toHaveBeenCalledTimes(1)
  })

  it('skips the sync when the browser timezone cannot be read', async () => {
    const isSubmitting = ref(false)
    const dependencies = createDependencies({
      readBrowserTimezone: () => null,
      syncTimezone: vi.fn(),
      applySyncedTimezone: vi.fn(),
    })

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(true)

    expect(dependencies.syncTimezone).not.toHaveBeenCalled()
    expect(dependencies.navigateHome).toHaveBeenCalledTimes(1)
  })

  it('treats a timezone sync failure as non-fatal and still navigates home', async () => {
    const isSubmitting = ref(false)
    const syncError = new Error('network down')
    const dependencies = createDependencies({
      readBrowserTimezone: () => 'Asia/Shanghai',
      syncTimezone: vi.fn().mockRejectedValue(syncError),
      applySyncedTimezone: vi.fn(),
      notifyTimezoneSyncFailed: vi.fn(),
    })

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(true)

    // A failed profile write must not be reported as a credential failure.
    expect(dependencies.notifyInvalidCredentials).not.toHaveBeenCalled()
    expect(dependencies.notifyTimezoneSyncFailed).toHaveBeenCalledWith(syncError)
    expect(dependencies.applySyncedTimezone).not.toHaveBeenCalled()
    expect(dependencies.navigateHome).toHaveBeenCalledTimes(1)
    expect(isSubmitting.value).toBe(false)
  })

  it('keeps the form busy while the timezone sync is in flight, blocking repeat submits', async () => {
    const isSubmitting = ref(false)
    const sync = deferred<{ timezone: string }>()
    const dependencies = createDependencies({
      readBrowserTimezone: () => 'Asia/Shanghai',
      syncTimezone: vi.fn(() => sync.promise),
      applySyncedTimezone: vi.fn(),
    })

    const submission = submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)
    await Promise.resolve()

    expect(isSubmitting.value).toBe(true)
    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(false)
    expect(dependencies.syncTimezone).toHaveBeenCalledTimes(1)

    sync.resolve({ timezone: 'Asia/Shanghai' })
    await expect(submission).resolves.toBe(true)
    expect(isSubmitting.value).toBe(false)
  })
})
