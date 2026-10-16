# Memoh Skill Files Reference

Use this reference when deciding which files a Memoh workspace skill should contain.

## File Tree

```text
skill-name/
├── SKILL.md                 required
├── scripts/                 optional executable helpers
├── references/              optional detailed docs loaded on demand
└── assets/                  optional files copied or used in outputs
```

Create only the files that directly support the requested skill. A small, sharp `SKILL.md` is better than a large folder full of unused placeholders.

## Required SKILL.md

`SKILL.md` must start with YAML frontmatter:

```markdown
---
name: skill-name
description: Clear trigger-focused description of what the skill does and when to use it.
---

# Skill Name

Use imperative, actionable instructions.
```

Rules:

- Keep the frontmatter `name` equal to the folder name.
- Prefer lowercase hyphen-case names under 64 characters.
- Put all trigger criteria in `description`; the body is loaded only after the skill triggers.
- Keep the body focused on workflow, decision points, and when to read bundled resources.
- Move long schemas, API docs, recipes, and examples to `references/`.

## scripts/

Use `scripts/` for deterministic operations that the agent would otherwise rewrite repeatedly.

Good candidates:

- File conversion or normalization helpers.
- API/client wrappers with stable inputs.
- Validation scripts.
- Repeatable report, migration, or generation commands.

Script rules:

- Prefer dependency-free Python or shell unless the dependency is already part of the workspace.
- Include a `--help` friendly CLI with clear arguments.
- Test every script you add or modify.
- In `SKILL.md`, tell the agent when to run the script and what output to expect.
- Scripts may be run with `python3 path/to/script.py`; do not rely on executable bits.

## references/

Use `references/` for detailed material that should be loaded only when needed.

Good candidates:

- API reference snippets.
- Database schemas.
- Long hook/config docs.
- Domain rules and examples.
- Troubleshooting matrices.

Reference rules:

- Link each important reference directly from `SKILL.md`.
- Avoid deep nesting; one reference level is easiest for agents to discover.
- For long files, include a table of contents near the top.
- Do not duplicate the same long content in both `SKILL.md` and `references/`.

## assets/

Use `assets/` for files that support output generation rather than instructions.

Good candidates:

- Project boilerplate.
- Document or slide templates.
- Images, icons, fonts.
- Sample data that gets copied or transformed.

Asset rules:

- Do not require the agent to load binary assets into context unless the task needs inspection.
- In `SKILL.md`, describe when to copy or adapt an asset.
- Keep assets scoped to the skill's job.

## What Not To Add

Avoid clutter:

- `README.md`
- `CHANGELOG.md`
- `INSTALL.md`
- Long design notes about why the skill was created
- Placeholder examples that are not meant to be used

If the user asks for documentation, create it as an intentional skill resource, not as generic project paperwork.

## Creation Checklist

- The folder lives under `/data/skills/user/personal/<skill-name>/`.
- `SKILL.md` exists and has valid frontmatter.
- The description says when to use the skill.
- Optional resources are referenced from `SKILL.md`.
- Scripts run successfully or have documented prerequisites.
- `quick_validate.py` passes.
