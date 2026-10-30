#!/usr/bin/env python3
"""Lists the Node built-ins each package's entry file imports.

The iOS runtime has no Node: `fs`, `crypto`, `zlib`, `child_process` and the rest
are refused with `ErrUnsupportedModule`. Screening a community package for them is
the difference between "this might work" and "this cannot", and doing it by eye is
how an audit becomes an impression.

    scripts/audit-node-imports.py DIR [DIR ...]

DIR is a directory of packages, or of `*-index.mjs` entry files. Bare npm
specifiers are listed separately: they fail the same way, for a different reason.
"""

from __future__ import annotations

import pathlib
import re
import sys

BUILTINS = {
    "assert", "async_hooks", "buffer", "child_process", "cluster", "console",
    "crypto", "dgram", "dns", "events", "fs", "http", "http2", "https", "net",
    "os", "path", "perf_hooks", "process", "querystring", "readline", "stream",
    "string_decoder", "timers", "tls", "tty", "url", "util", "v8", "vm", "zlib",
    "worker_threads",
}

# `from "x"`, `require("x")`, and `import("x")` are the three ways a package
# reaches for something outside itself.
SPECIFIER = re.compile(r"""(?:from\s+|require\(\s*|import\(\s*)["']([^"']+)["']""")


def entries(root: pathlib.Path, every: bool) -> list[pathlib.Path]:
    if root.is_file():
        return [root]
    if every:
        # Entry files alone are not enough: in the community set, `node:fs` and
        # `node:crypto` sit in lib/server.mjs, adapter.mjs and oauth.mjs while the
        # entry imports nothing but its own modules.
        return sorted(root.rglob("*.mjs")) + sorted(root.rglob("*.js"))
    found = sorted(root.glob("*-index.mjs")) + sorted(root.glob("*/index.mjs"))
    return found or sorted(root.rglob("*.mjs"))


def strip_comments(source: str) -> str:
    """Removes comments, keeping strings.

    Without this, a JSDoc `@type {import('...')}` counts as an import: the Copilot
    package looks like it needs an npm module it never loads. Only whole-line `//`
    comments are stripped in addition to block comments, so a `//` inside a URL
    cannot swallow the rest of a line.
    """
    source = re.sub(r"/\*.*?\*/", "", source, flags=re.S)
    return re.sub(r"(?m)^\s*//[^\n]*", "", source)


def audit(path: pathlib.Path) -> tuple[list[str], list[str]]:
    source = strip_comments(path.read_text(encoding="utf-8", errors="replace"))
    specifiers = {s for s in SPECIFIER.findall(source) if not s.startswith((".", "/"))}
    node = sorted(
        s for s in specifiers if s.startswith("node:") or s.split("/")[0] in BUILTINS
    )
    npm = sorted(s for s in specifiers if s not in node)
    return node, npm


def main() -> int:
    arguments = sys.argv[1:]
    every = False
    if arguments and arguments[0] in ("-a", "--all"):
        every = True
        arguments = arguments[1:]
    if not arguments:
        print("usage: scripts/audit-node-imports.py [-a] DIR [DIR ...]", file=sys.stderr)
        return 2
    blocked = 0
    for argument in arguments:
        root = pathlib.Path(argument)
        if not root.exists():
            print(f"audit-node-imports: no such path: {root}", file=sys.stderr)
            return 2
        for path in entries(root, every):
            node, npm = audit(path)
            if node or npm:
                blocked += 1
            name = path.parent.name if path.name == "index.mjs" else path.name
            parts = []
            if node:
                parts.append("node=" + ",".join(node))
            if npm:
                parts.append("npm=" + ",".join(npm))
            print(f"{name:24} {' '.join(parts) if parts else 'no outside imports'}")
    print(f"\n{blocked} entr(y/ies) reach outside the package", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
