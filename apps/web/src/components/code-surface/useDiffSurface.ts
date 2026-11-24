import type { DiffSurfaceController, DiffSurfaceInput } from 'stream-diffs'
import { onBeforeUnmount, shallowRef } from 'vue'

export type CodeSurfaceController = DiffSurfaceController<unknown, undefined>

/**
 * Owns one `stream-diffs` surface inside a host element.
 *
 * Every operation is serialized through `run()`: `stream-diffs` tears the
 * native surface down on update, so overlapping mounts/updates would race and
 * an older render could land after a newer one. The controller is loaded
 * lazily, so read-only trees never pay for the editor chunk.
 */
export function useDiffSurface() {
  const host = shallowRef<HTMLElement | null>(null)
  let controller: CodeSurfaceController | null = null
  let queue: Promise<void> = Promise.resolve()
  let disposed = false

  function run(task: () => Promise<void>): Promise<void> {
    queue = queue
      .then(async () => {
        if (!disposed)
          await task()
      })
      .catch((error) => {
        console.error('[code-surface] failed to render', error)
      })
    return queue
  }

  /** Mounts or updates the surface. Must be called from inside `run()`. */
  async function sync(input: DiffSurfaceInput): Promise<void> {
    if (!host.value || disposed)
      return
    if (!controller) {
      const { createDiffSurface } = await import('stream-diffs')
      if (!host.value || disposed)
        return
      const created = createDiffSurface(input) as CodeSurfaceController
      controller = created
      await created.mount(host.value)
      return
    }
    await controller.update(input)
  }

  function dispose(): void {
    disposed = true
    controller?.dispose()
    controller = null
  }

  /** Template-ref callback. `:ref="host"` would pass the unwrapped value. */
  function setHost(element: unknown): void {
    host.value = element instanceof HTMLElement ? element : null
  }

  onBeforeUnmount(dispose)

  return {
    host,
    setHost,
    run,
    sync,
    dispose,
    controller: () => controller,
  }
}
