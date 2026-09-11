// PowerShell 7 only fixes PSModulePath when it launches powershell.exe directly.
// Node is an intermediate process: let Windows PowerShell rebuild its own paths
// so built-in modules cannot resolve to incompatible PowerShell 7 versions.
export function windowsPowerShellEnv(env: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(env).filter(([key]) => key.toUpperCase() !== 'PSMODULEPATH'))
}
