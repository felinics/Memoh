#!/usr/bin/env node

import { formatCLIError, runCLI } from './cli-main'

runCLI(process.argv.slice(2))
  .then(code => { process.exitCode = code })
  .catch((error: unknown) => {
    console.error(formatCLIError(error))
    process.exitCode = 1
  })
