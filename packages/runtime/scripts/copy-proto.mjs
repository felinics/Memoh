import { copyFile, mkdir } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const source = resolve(packageRoot, '../../domains/runtime/bridge/pb/bridge.proto')
const destination = resolve(packageRoot, 'dist/bridge.proto')

await mkdir(dirname(destination), { recursive: true })
await copyFile(source, destination)
