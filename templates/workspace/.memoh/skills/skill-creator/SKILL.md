---
name: skill-creator
description: Create or update Memoh workspace skills under /data/skills/user/personal, including SKILL.md, scripts, references, assets, and validation. Use when the user asks to create, scaffold, revise, audit, or explain a workspace skill.
---

# Skill Creator

Use this skill to create or maintain Memoh workspace skills.

## Location Policy

- Store user-created workspace skills under `/data/skills/user/personal/<skill-name>/SKILL.md`. Memoh only discovers user skills in this directory.
- Treat `/data/.memoh/skills` as Memoh-managed built-in skill space. Do not create user skills there unless the user explicitly asks to maintain built-in templates.
- Prefer lowercase hyphen-case names, for example `docs-publisher` or `gh-review-helper`.

## Workflow

1. Clarify the skill's concrete triggers and 2-3 example user requests if the request is vague.
2. Read `references/skill-files.md` before designing resource folders, scripts, references, or assets.
3. For a new skill, initialize it with the bundled script when available:

```bash
python3 /data/.memoh/skills/skill-creator/scripts/init_skill.py my-skill --path /data/skills/user/personal --resources scripts,references
```

4. Edit `SKILL.md` and add only the resource files that directly support the skill.
5. Test any script you add or modify.
6. Validate the skill:

```bash
python3 /data/.memoh/skills/skill-creator/scripts/quick_validate.py /data/skills/user/personal/my-skill
```

## Core Shape

Every skill must have:

```markdown
---
name: skill-name
description: Clear trigger-focused description of what the skill does and when to use it.
---

# Skill Name

Actionable instructions for the agent.
```

Common optional files:

- `scripts/`: deterministic helpers the agent can run with `python3` or `bash`.
- `references/`: detailed docs loaded only when needed.
- `assets/`: templates, images, fonts, or boilerplate copied into outputs.

## Rules

- Keep `SKILL.md` concise and move detailed schemas or long examples into `references/`.
- Do not add README, changelog, install guide, or other auxiliary docs unless the user explicitly asks.
- Preserve existing user-authored content unless the requested change clearly replaces it.
- Include scripts only when they remove repeated fragile work or provide deterministic validation.
- If script execution fails because the runtime lacks a dependency, either make the script dependency-free or document the dependency inside the script usage.
