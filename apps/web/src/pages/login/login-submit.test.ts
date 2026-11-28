import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { resolveLoginErrorNotification, submitLogin, type SubmitLoginDependencies } from './login-submit'

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
    notifyLoginError: vi.fn(),
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

  it('notifies and releases the form when authentication fails with empty payload', async () => {
    const isSubmitting = ref(false)
    const dependencies = createDependencies({
      authenticate: vi.fn().mockResolvedValue({ data: null }),
    })

    await expect(submitLogin({ username: 'alice', password: 'bad' }, isSubmitting, dependencies)).resolves.toBe(false)

    expect(dependencies.applyLogin).not.toHaveBeenCalled()
    expect(dependencies.navigateHome).not.toHaveBeenCalled()
    expect(dependencies.notifyLoginError).toHaveBeenCalledWith(null)
    expect(isSubmitting.value).toBe(false)
  })

  it('forwards thrown authentication errors without collapsing them', async () => {
    const isSubmitting = ref(false)
    const authError = { code: 'auth.account_inactive', status: 403 }
    const dependencies = createDependencies({
      authenticate: vi.fn().mockRejectedValue(authError),
    })

    await expect(submitLogin({ username: 'alice', password: 'secret' }, isSubmitting, dependencies)).resolves.toBe(false)

    expect(dependencies.applyLogin).not.toHaveBeenCalled()
    expect(dependencies.navigateHome).not.toHaveBeenCalled()
    expect(dependencies.notifyLoginError).toHaveBeenCalledWith(authError)
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
    expect(dependencies.notifyLoginError).not.toHaveBeenCalled()
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
    expect(dependencies.notifyLoginError).not.toHaveBeenCalled()
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

describe('resolveLoginErrorNotification', () => {
  it('shows invalid credentials copy with retry hint for auth.invalid_credentials', () => {
    expect(
      resolveLoginErrorNotification(
        { code: 'auth.invalid_credentials', status: 401 },
        'Login Failed',
        'Check your credentials',
      ),
    ).toEqual({
      message: 'Incorrect username or password.',
      description: 'Check your credentials',
    })
  })

  it('shows deactivated account copy without retry hint for auth.account_inactive', () => {
    expect(
      resolveLoginErrorNotification(
        { code: 'auth.account_inactive', status: 403 },
        'Login Failed',
        'Check your credentials',
      ),
    ).toEqual({
      message: 'This account is inactive. Contact an administrator.',
    })
  })

  it('shows server error copy instead of invalid credentials for 500 internal errors', () => {
    expect(
      resolveLoginErrorNotification(
        { code: 'internal', status: 500 },
        'Login Failed',
        'Check your credentials',
      ),
    ).toEqual({
      message: 'Something went wrong on the server. Please try again.',
    })
  })

  it('returns an empty message for canceled 499 requests so the UI skips toasting', () => {
    expect(
      resolveLoginErrorNotification(
        { status: 499 },
        'Login Failed',
        'Check your credentials',
      ),
    ).toEqual({
      message: '',
    })
  })

  it('falls back to Login Failed when no structured error is available', () => {
    expect(
      resolveLoginErrorNotification(
        null,
        'Login Failed',
        'Check your credentials',
      ),
    ).toEqual({
      message: 'Login Failed',
    })
  })
})
