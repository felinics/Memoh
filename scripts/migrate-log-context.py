#!/usr/bin/env python3
"""Rewrite logger calls to their Context variants where a context is in scope.

slog.Logger.Info and friends log with context.Background() internally, so a
handler cannot read anything the request put in the context — which is where
the request and trace identity live. InfoContext and friends take the context
the caller already has.

What this does, per call site of the form `<expr>.log.Info(` or
`<expr>.logger.Info(`:

  1. The enclosing function has a named context parameter
     -> `Info(` becomes `InfoContext(<name>, `
  2. The enclosing function declares the context as `_`
     -> the parameter is renamed to ctx first, then as above
  3. The enclosing function has no context parameter
     -> left alone and reported, because the fix is a judgement: thread a
        context through the callers, or accept that this record has no
        correlation. A goroutine that outlives the request wants
        context.WithoutCancel rather than the request's own context.

Usage:
    python3 scripts/migrate-log-context.py [--dry-run] <file.go|dir> ...
"""

from __future__ import annotations

import pathlib
import re
import sys

FUNC = re.compile(r"^func\b", re.M)
NAME = re.compile(r"\s*(\((?P<recv>[^)]*)\)\s*)?(?P<name>\w+)")
# The receiver may be a field (s.logger, h.log) or a plain local, which is
# how a logger derived with With is usually held: `log := s.log.With(...)`.
# Requiring a qualifier missed every one of the latter, and missed them
# silently — those functions simply never appeared in the report.
CALL = re.compile(r"(?:\w[\w.]*\.)?\b(?:logger|log)\.(?P<level>Debug|Info|Warn|Error)\(")
CTX_PARAM = re.compile(r"\b(\w+)\s+context\.Context")

# A call site can opt out. Some records deliberately carry no correlation even
# though a context is in scope: a callback stored on a long-lived object holds
# whichever context happened to build it, and stamping every later event with
# that one names a request that had nothing to do with it. Without a marker
# this script silently undoes that decision on every run.
OPT_OUT = "logctx:plain"


class Site:
    def __init__(self, line: int, func: str) -> None:
        self.line = line
        self.func = func


class Func:
    """One function declaration, located by matching brackets rather than by
    regex.

    A signature can contain braces — `stop chan struct{}` is the common case —
    so a pattern that stops at the first `{` silently fails to match such a
    declaration, and every call in its body is then attributed to whichever
    function happened to be declared above it. Counting brackets is the only
    way to know where the parameters end and the body begins.
    """

    def __init__(self, name: str, params: str, params_start: int, body: tuple[int, int]) -> None:
        self.name = name
        self.params = params
        self.params_start = params_start
        self.body = body

    def contains(self, pos: int) -> bool:
        return self.body[0] <= pos < self.body[1]


def _match_bracket(src: str, start: int, opening: str, closing: str) -> int:
    """Index just past the bracket that closes the one at src[start]."""
    depth = 0
    for i in range(start, len(src)):
        if src[i] == opening:
            depth += 1
        elif src[i] == closing:
            depth -= 1
            if depth == 0:
                return i + 1
    return -1


def parse_funcs(src: str) -> list[Func]:
    out: list[Func] = []
    for kw in FUNC.finditer(src):
        head = NAME.match(src, kw.end())
        if head is None:
            continue
        open_paren = src.find("(", head.end())
        if open_paren == -1:
            continue
        close_paren = _match_bracket(src, open_paren, "(", ")")
        if close_paren == -1:
            continue
        # The body opens at the first brace after the parameters that is not
        # itself inside the return type's own brackets.
        depth = 0
        body_open = -1
        for i in range(close_paren, len(src)):
            ch = src[i]
            if ch in "([":
                depth += 1
            elif ch in ")]":
                depth -= 1
            elif ch == "{":
                if depth == 0:
                    body_open = i
                break
            elif ch == "\n" and depth == 0 and src[close_paren:i].strip().endswith(";"):
                break
        if body_open == -1:
            continue  # an interface method or a declaration without a body
        body_close = _match_bracket(src, body_open, "{", "}")
        if body_close == -1:
            continue
        out.append(
            Func(
                name=head.group("name"),
                params=src[open_paren + 1 : close_paren - 1],
                params_start=open_paren + 1,
                body=(body_open, body_close),
            )
        )
    return out


def enclosing(funcs: list[Func], pos: int) -> Func | None:
    # Innermost wins: a call inside a literal nested in a function body belongs
    # to whichever declaration most tightly encloses it.
    found = None
    for func in funcs:
        if func.contains(pos) and (found is None or func.body[0] > found.body[0]):
            found = func
    return found


def migrate(path: pathlib.Path, dry_run: bool) -> tuple[int, list[Site]]:
    src = path.read_text()
    funcs = parse_funcs(src)

    edits: list[tuple[int, int, str]] = []
    needs_ctx_name: dict[int, Func] = {}
    deferred: list[Site] = []

    for call in CALL.finditer(src):
        line_start = src.rfind("\n", 0, call.start()) + 1
        line_end = src.find("\n", call.start())
        own_line = src[line_start : line_end if line_end != -1 else len(src)]
        # The marker counts on the call's own line, or alone on the line above
        # it. Accepting any preceding line that merely contains it would let a
        # trailing marker exempt the next call as well.
        prev_start = src.rfind("\n", 0, line_start - 1) + 1
        previous = src[prev_start : max(prev_start, line_start - 1)].strip()
        if OPT_OUT in own_line or previous.startswith("//") and OPT_OUT in previous:
            continue
        func = enclosing(funcs, call.start())
        if func is None:
            deferred.append(Site(src[: call.start()].count("\n") + 1, "<file scope>"))
            continue
        ctx = CTX_PARAM.search(func.params)
        if ctx is None:
            deferred.append(Site(src[: call.start()].count("\n") + 1, func.name))
            continue
        name = ctx.group(1)
        if name == "_":
            name = "ctx"
            needs_ctx_name[func.params_start] = func
        # Replace the "(" that opens the call with "Context(<ctx>, ".
        edits.append((call.end() - 1, 1, f"Context({name}, "))

    for params_start, func in needs_ctx_name.items():
        renamed = func.params.replace("_ context.Context", "ctx context.Context", 1)
        if renamed != func.params:
            edits.append((params_start, len(func.params), renamed))

    # Apply back to front so earlier offsets stay valid.
    edits.sort(key=lambda edit: -edit[0])
    for pos, old_len, new in edits:
        src = src[:pos] + new + src[pos + old_len :]

    if edits and not dry_run:
        path.write_text(src)
    return len(edits), deferred


def targets(args: list[str]) -> list[pathlib.Path]:
    out: list[pathlib.Path] = []
    for arg in args:
        p = pathlib.Path(arg)
        if p.is_dir():
            out.extend(sorted(f for f in p.rglob("*.go") if not f.name.endswith("_test.go")))
        elif p.suffix == ".go" and not p.name.endswith("_test.go"):
            out.append(p)
    return out


def main() -> int:
    args = [a for a in sys.argv[1:] if a != "--dry-run"]
    dry_run = "--dry-run" in sys.argv[1:]
    if not args:
        print(__doc__, file=sys.stderr)
        return 2

    total_edits = 0
    total_deferred: list[tuple[pathlib.Path, Site]] = []
    for path in targets(args):
        edits, deferred = migrate(path, dry_run)
        if edits:
            total_edits += edits
            print(f"{path}: {edits} rewritten")
        total_deferred.extend((path, site) for site in deferred)

    if total_deferred:
        print(f"\n{len(total_deferred)} call sites need a judgement (no context in scope):")
        for path, site in total_deferred:
            print(f"  {path}:{site.line} in {site.func}")

    print(f"\nrewritten {total_edits}, deferred {len(total_deferred)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
