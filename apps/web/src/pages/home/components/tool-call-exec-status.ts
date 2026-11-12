// A command an external agent left running when its turn ended normally is
// recorded with this status instead of an exit: Codex had returned it to the
// model and kept its process in the background, and that is all Memoh knew
// when the turn ended. It is not a failure and not an interruption.
const LEFT_RUNNING_STATUS = 'running_in_background'

export interface ExecLeftRunning {
  processId: string
}

export function execLeftRunning(result: Record<string, unknown> | null | undefined): ExecLeftRunning | null {
  if (result?.status !== LEFT_RUNNING_STATUS) return null
  const processId = result.process_id ?? result.processId
  return { processId: typeof processId === 'string' ? processId : '' }
}
