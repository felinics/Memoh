# Workspace Templates

`workspace/` is the canonical set of files seeded into every agent workspace
on first boot:

- `AGENTS.md`, `MEMORY.md`, `PROFILES.md` — bot persona and memory scaffolding
  (not developer guides).
- `.memoh/` — default hooks configuration and built-in skills
  (`skill-creator`, `hooks-setup`).

The Server embeds `templates.WorkspaceFS()` and applies it through a
provider-neutral filesystem after provisioning. Container workspaces currently
use a Bridge-backed adapter; providers such as E2B can implement the same
minimal filesystem contract without depending on Bridge.

When seeding, `.gitkeep` placeholders are skipped. User-owned files are
create-only, while built-in files below `.memoh/skills` are refreshed without
deleting extra user skills.
