# Telegram Sticker MCP

This service exposes one internal delivery tool, `send_telegram_sticker`.
Memoh hides that backend from the model and merges all configured Sticker Sets
into one complete, deterministically sorted catalog in the first-party
`send.sticker_id` schema. The model
therefore selects and sends text plus a Sticker in one tool call; there is no
model-visible search round.

The backend schema carries the catalog in the internal
`x-memoh-sticker-catalog` extension as structured `id`, `description`, `emoji`,
and `status` fields. Memoh removes that extension and deterministically renders
the compact model-visible description. Pending and failed entries intentionally
render identically, so persistence of a failure does not create a prompt-cache
miss when no usable visual description changed.

Descriptions are cached in SQLite by Telegram `file_unique_id`, vision model,
and prompt version. A sticker is sent to the vision model only once for that
combination. Failed attempts are cached too, preventing an unavailable or
unsupported sticker from causing an endless conversion loop.

HTTP initialization never waits for Telegram downloads or vision conversion.
Sticker recognition is initiated only through Memoh's first-party Web API and
uses Memoh's configured Vision model and stored Provider credentials. This
process never receives those Provider credentials and never calls an LLM by
itself. Different request headers may select different bots and Sticker Sets on
the same endpoint.

## Configuration

Copy the example values into a service-owned, mode-`0600` environment file
outside source control (or pass another file with `-env-file`):

```dotenv
TELEGRAM_STICKER_MCP_SET_NAME=myadestes_1_amashiro_natsuki_plus_nacho_neko
# Optional legacy cache key only; no LLM call is made by this service.
TELEGRAM_STICKER_MCP_VISION_MODEL=legacy-vision-model-id
TELEGRAM_STICKER_MCP_CACHE_PATH=telegram-sticker-cache.sqlite
```

`TELEGRAM_STICKER_MCP_SET_NAME` is the default short set name from the
configured URL:

```text
https://t.me/addstickers/myadestes_1_amashiro_natsuki_plus_nacho_neko
```

For stdio, also set `TELEGRAM_STICKER_MCP_BOT_TOKEN` to the token used by that
Memoh Telegram channel. For HTTP, do not put a shared Telegram token in the
server environment; each MCP connection sends its own token in the
`X-Telegram-Bot-Token` request header. It may override the default set with
`X-Telegram-Sticker-Set`. The header accepts one or more set names separated by
commas, newlines, or semicolons. Names are trimmed, deduplicated, and sorted so
header order does not change the model-visible catalog. This header remains the
server-side selection protocol; Memoh's Web UI edits the list without exposing
the header or token to the browser. Sticker caches are isolated by token and
individual set name, so adding another set does not invalidate existing set
metadata.
The SQLite description cache is shared safely across bots because it stores
only Telegram's stable file identifier and generated description, never a bot
token.

Environment variables already present in the process take precedence over the
dotenv file.

## Run over stdio

```bash
cd cmd/telegram-sticker-mcp
go run .
```

Build a binary when configuring it as a persistent Memoh stdio MCP connection:

```bash
cd cmd/telegram-sticker-mcp
go build -o telegram-sticker-mcp .
```

The connection command is the absolute path to that binary. For isolated
workspace containers, place the Linux binary inside the bot workspace and
configure `TELEGRAM_STICKER_MCP_BOT_TOKEN` and
`TELEGRAM_STICKER_MCP_SET_NAME` in the connection's environment, together with
the cache settings shown above.

A standard `mcpServers` entry for stdio is:

```json
{
  "mcpServers": {
    "telegram-stickers": {
      "command": "/absolute/path/to/telegram-sticker-mcp",
      "args": [
        "-env-file",
        "/etc/memoh/telegram-sticker-mcp.env"
      ]
    }
  }
}
```

stdio is the default transport and is the simplest choice when the MCP client
can launch the binary and access the dotenv file on the same machine.

stdio requires this additional environment value:

```dotenv
TELEGRAM_STICKER_MCP_BOT_TOKEN=123456789:replace-me
TELEGRAM_STICKER_MCP_TRANSPORT=stdio
```

## Run over streamable HTTP

Set:

```dotenv
TELEGRAM_STICKER_MCP_TRANSPORT=http
TELEGRAM_STICKER_MCP_ADDR=127.0.0.1:8091
```

Then connect Memoh to `http://127.0.0.1:8091/mcp`. A loopback-only listener may
omit bearer authentication. Any non-loopback listener (including `0.0.0.0`)
must also set `TELEGRAM_STICKER_MCP_AUTH_TOKEN`; clients send the same value as
`Authorization: Bearer <token>`.

The equivalent standard HTTP configuration is:

```json
{
  "mcpServers": {
    "telegram-stickers": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer replace-with-a-random-service-token",
        "X-Telegram-Bot-Token": "123456789:first-service-token",
        "X-Telegram-Sticker-Set": "myadestes_1_amashiro_natsuki_plus_nacho_neko,another_sticker_set_by_example_bot"
      }
    }
  }
}
```

Two services can share the same HTTP MCP endpoint while using different
Telegram bots:

```json
{
  "mcpServers": {
    "telegram-stickers-service-a": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer replace-with-the-shared-service-token",
        "X-Telegram-Bot-Token": "123456789:first-service-token",
        "X-Telegram-Sticker-Set": "myadestes_1_amashiro_natsuki_plus_nacho_neko"
      }
    },
    "telegram-stickers-service-b": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer replace-with-the-shared-service-token",
        "X-Telegram-Bot-Token": "987654321:second-service-token",
        "X-Telegram-Sticker-Set": "another_sticker_set_by_example_bot"
      }
    }
  }
}
```

`X-Telegram-Bot-Token` selects the Telegram bot and
`X-Telegram-Sticker-Set` selects its Sticker Set list for that MCP connection.
All selected sets appear to the model as one flat catalog; management responses
also retain per-set groups for the Web UI.
Memoh's ordinary MCP list/get responses redact both bearer credentials and
Telegram bot tokens. Explicit MCP export remains the credential-transfer
boundary and therefore includes configured secrets.

When Memoh itself runs in Docker, bind the MCP server to an address reachable
from that container and use the corresponding internal hostname instead of
`127.0.0.1`.

## Internal tool

Memoh calls `send_telegram_sticker` behind its first-party `send` tool:

`send_telegram_sticker` accepts:

```json
{
  "chat_id": "-1001234567890",
  "sticker_id": "A1B2C3D4E5F60708-S0123456789ABCDEF"
}
```

The backend receives a server-injected `chat_id` and a `sticker_id` selected
from the stable full catalog. Models do not call this backend directly and do
not need a separate search. When the first-party `send` contains both text and
a sticker, Memoh sends the text first and then invokes this internal backend in
the same tool execution.

For HTTP deployments, the authenticated management endpoints under `/api/`
expose the catalog, static previews, active model/prompt profile, and storage of
manual recognition results. Memoh proxies these as its first-party Telegram
Sticker settings UI without exposing MCP headers or Provider credentials to the
browser.

The 16-character prefix identifies the source set without exposing its name.
The local `S…` portion is derived from Telegram's `file_unique_id`, so inserting
or reordering stickers cannot silently redirect an old ID to different media.
Both portions use 64-bit SHA-256 prefixes; collisions are rejected instead of
selecting the first matching set.

## Deployment boundary

This command remains a transitional sidecar for the current release because
its SQLite catalog/cache and existing per-bot MCP connections are already
deployed. Keeping that boundary for the hardening release avoids coupling a
security fix to a cache migration. The process owns Telegram media/catalog
access only: it receives no LLM Provider credentials and performs no model
calls. A later migration can move the catalog backend in-process after its
cache and connection data have an explicit migration path.

Sticker Set metadata and visual descriptions persist in SQLite across service
restarts. Metadata has no TTL: normal reads never re-fetch the set from
Telegram. Use `POST /api/catalog/refresh` only after intentionally refreshing
the configured sets. Static stickers are described from their WebP file. Animated
and video stickers use Telegram's static thumbnail so the vision request has a
consistently supported image.
