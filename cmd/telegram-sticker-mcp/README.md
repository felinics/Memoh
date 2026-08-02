# Telegram Sticker MCP

This service exposes one internal delivery tool, `send_telegram_sticker`.
Memoh hides that backend from the model and merges all configured Sticker Sets
into one complete, deterministically sorted catalog in the first-party
`send.sticker_id` schema. The model
therefore selects and sends text plus a Sticker in one tool call; there is no
model-visible search round.

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

Copy the example values into the repository root `.env` (or pass another file
with `-env-file`):

```dotenv
TELEGRAM_STICKER_MCP_SET_NAME=myadestes_1_amashiro_natsuki_plus_nacho_neko
# Optional legacy cache key only; no LLM call is made by this service.
TELEGRAM_STICKER_MCP_VISION_MODEL=gemini-3.1-pro-preview
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
        "/absolute/path/to/Memoh/.env"
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

Then connect Memoh to `http://127.0.0.1:8091/mcp`. The installed service is
intended for the internal network and does not require an `Authorization`
header.

The equivalent standard HTTP configuration is:

```json
{
  "mcpServers": {
    "telegram-stickers": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
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
        "X-Telegram-Bot-Token": "123456789:first-service-token",
        "X-Telegram-Sticker-Set": "myadestes_1_amashiro_natsuki_plus_nacho_neko"
      }
    },
    "telegram-stickers-service-b": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
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
The installed internal-network service does not require `Authorization`.

When Memoh itself runs in Docker, bind the MCP server to an address reachable
from that container and use the corresponding internal hostname instead of
`127.0.0.1`.

## Internal tool

Memoh calls `send_telegram_sticker` behind its first-party `send` tool:

`send_telegram_sticker` accepts:

```json
{
  "chat_id": "-1001234567890",
  "sticker_id": "3CF26A-S017"
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

The six-character prefix identifies the source set without exposing its name
in every enum value, and remains stable when unrelated sets are added or
removed.

Sticker Set metadata and visual descriptions persist in SQLite across service
restarts. Metadata has no TTL: normal reads never re-fetch the set from
Telegram. Use `POST /api/catalog/refresh` only after intentionally refreshing
the configured sets. Static stickers are described from their WebP file. Animated
and video stickers use Telegram's static thumbnail so the vision request has a
consistently supported image.
