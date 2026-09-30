# ChatGPT subscription provider

The `openai-chatgpt` provider connects Memoh's native, in-process agent to OpenAI's Sign in with ChatGPT protocol for open-source applications. It coexists with `openai-codex`; it does not launch Codex CLI or change workspace runtime credentials.

## Connect a self-hosted server

1. Configure `auth.agent_credentials_encryption_key` with a stable, base64-encoded 32-byte key. Keep it outside version control and back it up securely. Changing it makes existing encrypted sessions unreadable.
2. Open the server in Memoh Desktop. Remote servers require HTTPS for credential transfer; HTTP is accepted only for loopback development servers.
3. Open Settings → Providers → ChatGPT and choose **Sign in with ChatGPT**. Complete OpenAI sign-in and approve ChatGPT plan usage in the browser, then return to Desktop.
4. Enable the provider, refresh its managed model catalog, and select an imported chat model for a native Memoh bot.

Browser-only settings can show connection status and manage a connected provider. Initial sign-in and renewed consent use Desktop's temporary `127.0.0.1` listener. Desktop exchanges the authorization code locally and imports credentials through the authenticated server API. The issued client ID is saved before code exchange, so a failed exchange can retry the same registration. Only verified credentials activate the connection; credentials are not returned to the renderer.

The account owner can reconnect or disconnect. Other members can use models exposed by the shared provider but cannot replace its authorization. Identity-only consent is saved separately from permission to use the subscription; inference remains blocked until usage permission is granted.

## State and lifecycle

`chatgpt_runtime_hosts` stores one host ID per team-owned logical Server runtime. Providers in that runtime share the host ID. Server replicas for the same team use the same logical registration; per-replica and workspace VM registrations are outside this implementation.

`chatgpt_provider_sessions` stores the provider owner and an AES-GCM encrypted payload containing the issued client ID, tokens, account identity, and pending state/nonce. Additional authenticated data binds ciphertext to the team, provider, and owner. Host registration and client mapping survive disconnect; deleting the provider cascades its session deletion. Migration rollback removes ChatGPT providers and their sessions.

Server refresh holds the session row lock through exchange and replacement, so concurrent requests and replicas do not reuse a rotating refresh token. Terminal refresh failures clear unusable credentials while retaining registration. Transient failures preserve the session. Disconnect uses OpenAI's discovered revocation endpoint before clearing local tokens.

An authentication rejection invalidates only the access token used by that request; a delayed rejection cannot clear newer credentials. Before streaming starts, the request can refresh and retry once. A midstream failure does not replay partial output. If recovery cannot finish, settings show the recovery state and Desktop offers **Sign in again** without requiring a disconnect first.

HTTP and streaming failures retain status, error code, parameter, response shape, and upstream request ID in private diagnostic logs. Response bodies, upstream messages, and credentials are excluded. The UI receives stable, localized error codes.

## Inference and limits

Requests use the public `/v1/responses` endpoint with `store: false`, streaming, developer instructions, and namespaced native function tools. A response is successful only after `response.completed`; text followed by a quota error or an interrupted stream remains a failure. Unsupported request parameters are omitted. Imports request `/v1/models?client_version=1.0.0` with the same authorization: only `visibility: list` entries appear, in the server's order. The compatibility value is shared with Codex discovery and is independent of Memoh's release. In live verification, omitting this query parameter returned an older catalog missing callable GPT-6 models; both `0.159.0` and `1.0.0` returned the current catalog. The public documentation's minimal listing example omits the parameter, so this behavior is based on verified responses rather than a documented guarantee.

Imports retain context limits, image input support, and reasoning levels supported by Memoh. Refresh updates these capabilities and positions without changing enabled model choices. Catalog listings are not an entitlement test; a completed inference verifies access to the selected model for that request. Model names in documentation examples are not used as a fallback catalog.

This integration shares the existing ChatGPT subscription limits with ChatGPT and Codex. It does not add quota or fall back to API-key billing. Subscription usage limits and missing usage permission have stable, localized error codes. Hosted tools, Files API uploads, audio/video, external Codex runtime sign-in, and commercial Cloud partner onboarding are outside this first implementation.

## Official references

- [Registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
- [Accounts and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions)
- [Models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)
- [Self-hosted VMs](https://developers.openai.com/siwc/token-sharing-open-source/self-hosted-vms)
- [Errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)
