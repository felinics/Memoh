import { defineConfig } from '@hey-api/openapi-ts';

// Operations that answer with text/event-stream. Keep in sync with the
// handlers that call beginSSEResponse / setSSEHeaders and declare
// `@Produce text/event-stream`.
const sseOperations = new Set([
  'post /bots/{bot_id}/container/display/prepare',
  'post /bots/{bot_id}/dependencies/{dep_id}/install',
  'post /bots/{bot_id}/dependencies/{dep_id}/update',
  'post /bots/{bot_id}/dependencies/{dep_id}/reinstall',
  'delete /bots/{bot_id}/dependencies/{dep_id}',
])

export default defineConfig({
  input: './spec/swagger.json',
  output: 'packages/sdk/src',
  plugins: [
    '@hey-api/typescript',
    {
      name: '@hey-api/transformers',
      dates: true,
      bigInt: true,
    },
    {
      name: '@hey-api/sdk',
      transformer: true
    },
    '@hey-api/client-fetch',
    {
      name: '@pinia/colada',
      $hooks: {
        operations: {
          // An SSE stream returns { stream }, so it cannot be represented by
          // Pinia Colada's generated mutation contract, which expects { data }.
          isMutation: operation => sseOperations.has(`${operation.method} ${operation.path}`)
            ? false
            : undefined,
        },
      },
    },
  ],
})
