#!/usr/bin/env python3
"""Validate a Memoh workspace skill folder."""

import argparse
import re
import sys
from pathlib import Path


MAX_SKILL_NAME_LENGTH = 64
ALLOWED_FRONTMATTER = {"name", "description", "metadata", "license", "allowed-tools"}


def parse_frontmatter(text: str) -> tuple[dict[str, str], str]:
    match = re.match(r"^---\n(.*?)\n---\s*", text, re.DOTALL)
    if not match:
        raise ValueError("SKILL.md must start with YAML frontmatter delimited by ---")
    data: dict[str, str] = {}
    for raw_line in match.group(1).splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if ":" not in line:
            raise ValueError(f"frontmatter line is not key: value: {raw_line!r}")
        key, value = line.split(":", 1)
        key = key.strip()
        if key not in ALLOWED_FRONTMATTER:
            allowed = ", ".join(sorted(ALLOWED_FRONTMATTER))
            raise ValueError(f"unexpected frontmatter key {key!r}; allowed: {allowed}")
        value = value.strip()
        if ": " in value and value[:1] not in {'"', "'"}:
            raise ValueError(f"frontmatter {key!r} contains ': '; wrap the value in quotes so it stays valid YAML")
        data[key] = value.strip('"').strip("'")
    body = text[match.end() :]
    return data, body


def validate(skill_dir: Path) -> list[str]:
    warnings: list[str] = []
    if not skill_dir.is_dir():
        raise ValueError(f"not a directory: {skill_dir}")
    skill_md = skill_dir / "SKILL.md"
    if not skill_md.exists():
        raise ValueError("missing SKILL.md")
    frontmatter, body = parse_frontmatter(skill_md.read_text(encoding="utf-8"))

    name = frontmatter.get("name", "").strip()
    description = frontmatter.get("description", "").strip()
    if not name:
        raise ValueError("frontmatter.name is required")
    if not re.fullmatch(r"[a-z0-9]+(?:-[a-z0-9]+)*", name):
        raise ValueError("frontmatter.name should be lowercase hyphen-case")
    if name != skill_dir.name:
        warnings.append(f"name {name!r} differs from folder {skill_dir.name!r}")
    if len(name) > MAX_SKILL_NAME_LENGTH:
        raise ValueError(f"name is longer than {MAX_SKILL_NAME_LENGTH} characters")
    if not description:
        raise ValueError("frontmatter.description is required")
    if len(description) > 1024:
        raise ValueError("description is longer than 1024 characters")
    if "when" not in description.lower() and "use" not in description.lower():
        warnings.append("description should say when to use the skill")
    if not body.strip():
        raise ValueError("SKILL.md body is empty")

    for noisy in ("README.md", "CHANGELOG.md", "INSTALL.md", "INSTALLATION_GUIDE.md"):
        if (skill_dir / noisy).exists():
            warnings.append(f"avoid auxiliary docs unless explicitly needed: {noisy}")

    for scripts_dir in [skill_dir / "scripts"]:
        if scripts_dir.is_dir():
            for script in scripts_dir.iterdir():
                if script.is_file() and script.suffix in {".py", ".sh"} and script.stat().st_size == 0:
                    raise ValueError(f"empty script file: {script}")

    return warnings


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate a Memoh skill folder.")
    parser.add_argument("skill_dir", help="Path to a skill directory")
    args = parser.parse_args()

    try:
        warnings = validate(Path(args.skill_dir).resolve())
    except Exception as exc:  # noqa: BLE001 - CLI should print friendly errors.
        print(f"[ERROR] {exc}", file=sys.stderr)
        return 1

    for warning in warnings:
        print(f"[WARN] {warning}")
    print("[OK] skill is valid")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
