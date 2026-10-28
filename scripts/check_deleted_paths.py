#!/usr/bin/env python3
"""Fail when a functional file references a path that no longer exists.

A rebase can delete a file that the fork still uses. That breakage merges
*cleanly*: the fork-only workflow or script that mentions it is not part of the
conflict, so git has nothing to report. On 2026-10-28 upstream deleted
`ui/.nvmrc`, and the fork-only `.github/workflows/sync-upstream.yml` kept
reading it while the rebase reported success.

The check is deliberately narrow: only paths deleted between a base revision
and HEAD, and only functional files whose job is to execute (workflows,
Makefile, scripts, package manifests). Mentions in prose cannot break a build,
and undefined output paths (`bin/shelley-*`, artifact directories) are not
deletions, so neither produces noise.

Paths in the conflict resolver's DELETED_PATHS are intentionally absent; that
set is the single source of truth for deliberate deletions.
"""

import argparse
from pathlib import Path
import re
import subprocess
import sys


# These name deleted paths on purpose: they exist to describe and resolve
# deletions, so a mention there is documentation, not a broken reference.
DESCRIBING_FILES = {
    "scripts/check_deleted_paths.py",
    "scripts/resolve_upstream_conflicts.py",
    "scripts/test_check_deleted_paths.py",
    "scripts/test_resolve_upstream_conflicts.py",
}

# Files that execute or configure a build. A stale reference here breaks CI.
FUNCTIONAL_GLOBS = (
    ".github/workflows/*.yml",
    ".github/workflows/*.yaml",
    "Makefile",
    "scripts/*",
    ".goreleaser.yml",
    "ui/package.json",
    "go.mod",
)


def deleted_paths(base: str) -> list[str]:
    out = subprocess.check_output(
        ["git", "diff", "--name-only", "--diff-filter=D", base, "HEAD"], text=True
    )
    return [line for line in out.splitlines() if line]


def intentional_deletions() -> set[str]:
    sys.path.insert(0, str(Path(__file__).parent))
    from resolve_upstream_conflicts import DELETED_PATHS

    return set(DELETED_PATHS)


def functional_files() -> list[Path]:
    paths: set[Path] = set()
    for pattern in FUNCTIONAL_GLOBS:
        paths.update(p for p in Path(".").glob(pattern) if p.is_file())
    return sorted(path for path in paths if str(path) not in DESCRIBING_FILES)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True, help="revision to diff HEAD against")
    args = parser.parse_args()

    gone = [p for p in deleted_paths(args.base) if p not in intentional_deletions()]
    if not gone:
        print(f"No non-intentional deletions between {args.base} and HEAD.")
        return

    # Match a path only as a whole path token: `ui/.nvmrc` must not match
    # inside `ui/.nvmrc.bak`, which is a different file.
    patterns = {
        path: re.compile(r"(?<![\w/.-])" + re.escape(path) + r"(?![\w.-])")
        for path in gone
    }
    failures = []
    for file in functional_files():
        text = file.read_text(errors="replace")
        for path, pattern in patterns.items():
            for number, line in enumerate(text.splitlines(), 1):
                if pattern.search(line):
                    failures.append(f"{file}:{number}: references deleted {path}")

    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        print(
            "\nA file this repo still uses was deleted. Update these references, "
            "or add the path to DELETED_PATHS if the deletion is intentional.",
            file=sys.stderr,
        )
        raise SystemExit(1)

    print(f"References checked against {len(gone)} deleted path(s): none stale.")


if __name__ == "__main__":
    main()
