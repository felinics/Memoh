#!/usr/bin/env python3
"""Initialize a Memoh workspace skill under /data/skills/user/personal."""

import argparse
import re
import sys
from pathlib import Path


MAX_SKILL_NAME_LENGTH = 64
ALLOWED_RESOURCES = {"scripts", "references", "assets"}


SKILL_TEMPLATE = """---
name: {skill_name}
description: "TODO: Explain what this skill does and exactly when to use it."
---

# {skill_title}

## Workflow

1. TODO: Describe the first action the agent should take.
2. TODO: Describe the core procedure.
3. TODO: Describe how to verify the result.

## Resources

TODO: Mention any scripts, references, or assets this skill provides. Delete this section if there are none.
"""


EXAMPLE_SCRIPT = '''#!/usr/bin/env python3
"""Example helper for {skill_name}. Replace or delete this file."""


def main() -> int:
    print("hello from {skill_name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
'''


EXAMPLE_REFERENCE = """# {skill_title} Reference

Replace this file with detailed reference material that should be loaded only when needed.

## Common Patterns

- TODO: Add examples, schemas, command recipes, or troubleshooting notes.
"""


EXAMPLE_ASSET = """This placeholder represents an asset file.

Replace it with templates, images, fonts, boilerplate, or sample data that the skill should use.
"""


def normalize_skill_name(raw: str) -> str:
    name = raw.strip().lower()
    name = re.sub(r"[^a-z0-9]+", "-", name)
    name = re.sub(r"-{2,}", "-", name).strip("-")
    return name


def title_case(skill_name: str) -> str:
    return " ".join(part.capitalize() for part in skill_name.split("-") if part)


def parse_resources(raw: str) -> list[str]:
    if not raw.strip():
        return []
    resources = [item.strip() for item in raw.split(",") if item.strip()]
    invalid = sorted(set(resources) - ALLOWED_RESOURCES)
    if invalid:
        allowed = ", ".join(sorted(ALLOWED_RESOURCES))
        raise ValueError(f"unknown resource type(s): {', '.join(invalid)}; allowed: {allowed}")
    out: list[str] = []
    seen: set[str] = set()
    for resource in resources:
        if resource not in seen:
            out.append(resource)
            seen.add(resource)
    return out


def create_resource_dirs(skill_dir: Path, skill_name: str, resources: list[str], examples: bool) -> None:
    skill_title = title_case(skill_name)
    for resource in resources:
        directory = skill_dir / resource
        directory.mkdir(exist_ok=True)
        if not examples:
            continue
        if resource == "scripts":
            path = directory / "example.py"
            path.write_text(EXAMPLE_SCRIPT.format(skill_name=skill_name), encoding="utf-8")
        elif resource == "references":
            (directory / "reference.md").write_text(
                EXAMPLE_REFERENCE.format(skill_title=skill_title),
                encoding="utf-8",
            )
        elif resource == "assets":
            (directory / "example-asset.txt").write_text(EXAMPLE_ASSET, encoding="utf-8")


def init_skill(skill_name: str, root: Path, resources: list[str], examples: bool) -> Path:
    skill_dir = root / skill_name
    if skill_dir.exists():
        raise ValueError(f"skill already exists: {skill_dir}")
    skill_dir.mkdir(parents=True)
    skill_title = title_case(skill_name)
    (skill_dir / "SKILL.md").write_text(
        SKILL_TEMPLATE.format(skill_name=skill_name, skill_title=skill_title),
        encoding="utf-8",
    )
    create_resource_dirs(skill_dir, skill_name, resources, examples)
    return skill_dir


def main() -> int:
    parser = argparse.ArgumentParser(description="Create a Memoh skill folder.")
    parser.add_argument("skill_name", help="Skill name; normalized to lowercase hyphen-case")
    parser.add_argument("--path", default="/data/skills/user/personal", help="Directory that will contain the skill folder")
    parser.add_argument("--resources", default="", help="Comma-separated list: scripts,references,assets")
    parser.add_argument("--examples", action="store_true", help="Create placeholder files in resource folders")
    args = parser.parse_args()

    try:
        skill_name = normalize_skill_name(args.skill_name)
        if not skill_name:
            raise ValueError("skill name must contain at least one letter or digit")
        if len(skill_name) > MAX_SKILL_NAME_LENGTH:
            raise ValueError(f"skill name is too long; max {MAX_SKILL_NAME_LENGTH} characters")
        resources = parse_resources(args.resources)
        if args.examples and not resources:
            raise ValueError("--examples requires --resources")
        out = init_skill(skill_name, Path(args.path).resolve(), resources, args.examples)
    except Exception as exc:  # noqa: BLE001 - CLI should print friendly errors.
        print(f"[ERROR] {exc}", file=sys.stderr)
        return 1

    print(f"[OK] created {out}")
    print(f"Next: edit {out / 'SKILL.md'} and run quick_validate.py")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
