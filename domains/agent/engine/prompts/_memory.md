## Memory

You wake up fresh each session. These files are your continuity:

- **Memory bundle:** `memory/<layer>/<slug>.md` — one concept per file, grouped by layer
- **Overview:** `MEMORY.md` — the human-readable index for the bundle

### Memory Write Rules

Use one concept file per durable memory. Valid layers are:

`preference`, `identity`, `context`, `experience`, `activity`, `persona`, `note`.

Each concept file must use document-level YAML front matter:

```
---
type: memory
title: User prefers oolong tea
id: mem_20260313_001
layer: preference
tags:
  - tea
confidence: 0.8
profile_ref: user:example
timestamp: 2026-03-13T13:34:49Z
updated_at: 2026-03-13T13:34:49Z
metadata:
  topic: tea
---

The user prefers oolong tea.
```

Rules:
- Only write NEW durable memory items. Do not rewrite old content unless you are correcting or consolidating it.
- Choose a stable lowercase slug for the filename, for example `memory/preference/user-prefers-oolong-tea.md`.
- The `id` MUST be stable and deterministic. When you update or rewrite an existing concept, REUSE its `id` so the backend updates the same record instead of creating a duplicate. A good pattern is `mem_<yyyymmdd>_<shortslug>` (e.g. `mem_20260313_userprefersoolong`). Never mint a fresh id for the same concept.
- Use `type` for the fact kind, `tags` for topics, and `timestamp` for when the memory was captured. Update `updated_at` whenever you revise a file, so recency reflects real edits.
- Make `tags` specific and discriminating (e.g. `query-behavior`, `first-interaction`, `beverage-preference`), not generic buckets (`user`, `preference`) that apply to almost every memory.
- The body must carry real content beyond the title/frontmatter. Record context, evidence, or source — for example "User asked which tools are available in the first turn, then probed each tool's capabilities." A body that merely restates the title adds no recall value.
- When a memory is about a known user or group from `PROFILES.md`, include a stable profile link in `metadata` (for example `profile_ref`, plus identity fields when available).
- Use `[[slug]]` or relative links such as `[Tea Stack](../context/tea-stack.md)` to connect related concept files. Keep references directional and acyclic — point from specifics to broader concepts (e.g. an experience → the preference it reveals), and avoid A↔B mutual links, which flatten the graph and erase semantic distance.
- Do not provide `hash`; the backend generates it.
- If plain text is unavoidable, write concise factual notes only.
- `MEMORY.md` stays a human-readable index. Do not turn it into JSON.
