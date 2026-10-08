---
name: twilight-ai
description: Assist with development in the Twilight AI Go SDK. Use when working in this repository, adding or updating providers, embeddings, tool calling, streaming, examples, or docs for Twilight AI.
---

# Twilight AI

## When To Use

Use this skill when the task involves `twilight-ai`, especially:

- implementing or refactoring SDK APIs in `sdk/`
- adding or updating providers under `provider/`
- working on `Model.Generate`, `Model.Stream`, `Embed`, `EmbedMany`, `GenerateImage`, or `EditImage`
- adding tool-calling, streaming, reasoning, embedding, or image generation support
- writing examples, docs, or usage guidance for this library

## Project Snapshot

Twilight AI is a lightweight Go AI SDK with a provider-agnostic core API.

- Text generation: build an `sdk.Request` and call `Model.Generate` or `Model.Stream`; one call, one `ModelResult`
- Image generation: `sdk.GenerateImage`, `sdk.EditImage`
- Embeddings: `sdk.Embed`, `sdk.EmbedMany`
- Tool calling: `sdk.ToolDefinition` (or `sdk.NewToolDefinition[T]`), typed `sdk.ToolCall` / `sdk.ToolArguments` / `sdk.ToolOutput`
- Streaming: typed `StreamPart` events over Go channels
- Current providers:
  - `provider/openai/completions`
  - `provider/openai/responses`
  - `provider/openai/codex`
  - `provider/openai/images`
  - `provider/anthropic/messages`
  - `provider/google/generativeai`
  - `provider/openai/embedding`
  - `provider/google/embedding`

## Default Mental Model

Prefer the high-level SDK API first, then drop to provider details only when needed.

- `sdk.Model` binds a chat model to a `sdk.Provider`
- `sdk.EmbeddingModel` binds an embedding model to an `sdk.EmbeddingProvider`
- `sdk.ImageGenerationModel` binds an image generation model to an `sdk.ImageGenerationProvider`
- `sdk.ImageEditModel` binds an image edit model to an `sdk.ImageEditProvider`
- A runtime drives `Model.Generate` or `Model.Stream` with an `sdk.Request`, runs the returned `ToolCalls`, and appends the assistant and tool messages of the step to the next request
- Providers handle backend-specific HTTP, request mapping, response parsing, and SSE translation

## Core API Guidance

Choose the narrowest API that matches the task:

- Need one model call and its result: use `Model.Generate` or `Model.Stream` with an `sdk.Request`
- Need one vector: use `sdk.Embed`
- Need multiple vectors or embedding token usage: use `sdk.EmbedMany`
- Need image generation from a text prompt: use `sdk.GenerateImage`
- Need image editing or inpainting: use `sdk.EditImage`

If the task introduces examples or docs, prefer simple end-to-end snippets that start with:

1. construct provider
2. get model
3. call SDK API
4. handle error

## Provider Selection Rules

- Use `openai/completions` for broad OpenAI-compatible support such as DeepSeek, Groq, Ollama, Azure-style compatible endpoints, and generic `/chat/completions` backends.
- Use `openai/responses` when the task needs OpenAI Responses API features such as first-class reasoning models, reasoning summaries, URL citation annotations, or flat input mapping.
- Use `openai/codex` when the task needs OpenAI Codex coding agent models (gpt-5.x-codex series) with ChatGPT access token authentication and encrypted reasoning content.
- Use `anthropic/messages` for Claude and Anthropic extended thinking via `WithThinking`.
- Use `google/generativeai` for Gemini chat, tool calling, vision, streaming, and Gemini reasoning.
- Use `openai/images` for image generation (dall-e-2, dall-e-3, gpt-image-1) and image editing via the OpenAI Images API.
- Use `openai/embedding` or `google/embedding` for embeddings. Keep embedding-provider work separate from chat-provider work.

## Implementation Rules

### Chat Providers

If adding or changing a chat provider, preserve the `sdk.Provider` contract:

- `Name()`
- `ListModels(ctx)`
- `Test(ctx)`
- `TestModel(ctx, modelID)`
- `DoGenerate(ctx, req sdk.Request) (sdk.ModelResult, error)`
- `DoStream(ctx, req sdk.Request) (<-chan sdk.StreamPart, error)`

Keep provider responsibilities focused:

- translate SDK messages/options into backend request format
- parse backend responses into `sdk.ModelResult`
- map backend streaming events into typed `sdk.StreamPart` values; the SDK core assembles them into a `ModelResult`
- report usage, finish reasons, reasoning, tool calls, sources, and files when supported

### Embedding Providers

Embedding providers are separate from chat providers. Use `sdk.EmbeddingProvider` and return an `sdk.EmbeddingModel` via `EmbeddingModel(id)`.

When updating embeddings:

- keep `sdk.Embed` for single-string convenience
- keep `sdk.EmbedMany` for batched requests
- preserve `Usage.Tokens`
- only expose dimensions/task-type behavior when the backend supports it

### Image Providers

Image providers are separate from chat, embedding, and speech providers. Use `sdk.ImageGenerationProvider` and/or `sdk.ImageEditProvider`.

When updating image providers:

- keep `sdk.GenerateImage` for generation convenience
- keep `sdk.EditImage` for editing convenience
- preserve `ImageUsage` token details when the backend supports them
- support both multipart file upload and JSON reference modes for edit inputs

### Tool Calling

Prefer `sdk.NewToolDefinition[T]` for new tool examples: typed arguments and an inferred JSON Schema.

Use these defaults unless the task requires something else:

- `Request.ToolChoice` left zero (auto) for normal use; `sdk.ToolChoice{Mode: sdk.ToolChoiceRequired}` or `{Mode: sdk.ToolChoiceTool, Tool: name}` to force a call
- one `Generate` per step; the caller runs the returned `ToolCalls` and decides how many steps to take
- a call's arguments are `sdk.ToolArguments`; decode with `Unmarshal`, never assume a `map[string]any`; `Valid()` false means answer the model with an error result instead of running the tool
- a tool's result is a `sdk.ToolResultPart` whose `Result` is `sdk.TextOutput` or `sdk.JSONOutput`; the replayed assistant message keeps the reasoning parts and each part's `ProviderMetadata`

When streaming with tools, the stream emits tool input construction parts and one `StreamToolCallPart` per completed call.

### Streaming

Twilight AI streaming is channel-first and type-safe. Prefer type switches over loosely typed event parsing.

Important expectations:

- `Model.Stream` returns an `sdk.ModelStream`: `Parts` is the channel of `sdk.StreamPart` values, `Result()` is valid once `Parts` is drained
- `sdk.CollectStream` is the convenience path when callers do not want manual event handling
- a stream is one model call; it carries no tool execution events

### Messages And Results

Preserve the SDK message model and avoid backend-specific shapes leaking into public usage.

- user, assistant, system, and tool messages should stay in SDK types
- support rich parts where relevant: text, image, file, reasoning, tool call, tool result
- keep finish reason mapping aligned with SDK constants such as `stop`, `length`, `content-filter`, and `tool-calls`

## Common Task Patterns

### Add A New Usage Example

Use this structure:

1. pick the correct provider package
2. create provider with explicit options
3. create model via `ChatModel`, `EmbeddingModel`, `GenerationModel`, or `EditModel`
4. call the top-level `sdk` function
5. show minimal but idiomatic result handling

### Add Or Update A Provider Feature

Check all affected layers:

1. request mapping
2. non-streaming response mapping
3. streaming event mapping
4. finish-reason and usage mapping
5. reasoning/tool/source/file support if the backend exposes them
6. model discovery and provider health checks if endpoints exist

### Add A Custom Provider

Use the built-in providers as the template. A custom provider should feel identical to existing ones from the caller's perspective.

Minimum behavior:

1. return a provider-bound model from `ChatModel`
2. implement discovery and health-check methods
3. support `DoGenerate`
4. support `DoStream` with correct lifecycle parts

## Documentation Rules

When writing Twilight AI docs or README content:

- prefer provider-agnostic phrasing first, provider-specific details second
- use Go examples, not pseudocode, unless explaining an interface contract
- keep examples small and runnable in spirit
- mention exact package paths for imports
- explain when to choose Completions vs Responses vs Codex when OpenAI is involved
- keep embeddings, tool calling, and streaming as separate concerns unless the example truly combines them

## Terminology

Use these terms consistently:

- Provider: backend implementation for chat generation
- Embedding provider: backend implementation for embeddings
- Image generation provider: backend implementation for image generation
- Image edit provider: backend implementation for image editing
- Model: provider-bound chat model
- Embedding model: provider-bound embedding model
- Image generation model: provider-bound image generation model
- Image edit model: provider-bound image edit model
- Tool calling: model requests a tool invocation
- Step: one model call plus the tool calls it made; the caller assembles the assistant and tool messages
- Stream part: a typed event from the model seam (`sdk.StreamPart`), delivered by `Model.Stream`

## Quick Checklist

Before finishing work in this repo, verify:

- the chosen provider package matches the intended backend capabilities
- chat, embedding, and image concerns are not mixed accidentally
- public examples use top-level `sdk` APIs unless lower-level behavior is the point
- streaming logic uses typed `StreamPart` handling
- tool-calling examples build the replayed assistant and tool messages by hand
- provider work includes health checks or model discovery behavior if the backend supports them

## Additional Resources

- For exported APIs, signatures, provider options, and stream/event types, see [reference.md](reference.md)
