import type { AcpagentRuntimeStatus } from '@memohai/sdk'

export function acpRuntimeMatchesConfiguration(
  runtime: AcpagentRuntimeStatus | undefined,
  agentId: string,
  projectPath: string,
): boolean {
  const expectedAgent = agentId.trim()
  const expectedProjectPath = projectPath.trim()
  return Boolean(
    runtime
    && expectedAgent
    && runtime.agent_id?.trim() === expectedAgent
    && (!expectedProjectPath || runtime.project_path?.trim() === expectedProjectPath),
  )
}
