#!/usr/bin/env python3
"""Replay every historical upstream sync with a resolver; compare to what shipped.

For each pair of consecutive playground releases A, B whose upstream bases
differ, rebase A onto B's upstream base, resolving conflicts with RESOLVER
exactly as the sync does, and report whether the rebase was clean, resolved
automatically, or refused. An automatic result is compared with B's tree,
ignoring files that fork commits new in B touched, since those were edited by
hand. Run it before changing the resolver: a change should only turn refusals
into results that match what shipped.

  git fetch playground --tags && git fetch origin main
  scripts/replay_sync_history.py origin/main [RESOLVER]
"""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

REPO = "jgbrwn/shelley-playground"


def git(*args: str, cwd: Path | None = None, check: bool = True) -> str:
    result = subprocess.run(["git", *args], cwd=cwd, text=True, capture_output=True)
    if check and result.returncode:
        raise SystemExit(f"git {' '.join(args)}: {result.stderr.strip()}")
    return result.stdout


def main() -> None:
    if len(sys.argv) not in (2, 3):
        raise SystemExit(__doc__)
    upstream = sys.argv[1]
    root = Path(git("rev-parse", "--show-toplevel").strip())
    work = Path(tempfile.mkdtemp(prefix="replay-sync-"))
    # The resolver must live outside the worktree: rebasing replaces scripts/.
    resolver = work / "resolver.py"
    shutil.copy(sys.argv[2] if len(sys.argv) == 3 else root / "scripts/resolve_upstream_conflicts.py", resolver)
    tags = json.loads(subprocess.check_output(
        ["gh", "release", "list", "--repo", REPO, "--limit", "1000", "--json", "tagName,createdAt"], text=True
    ))
    states = []
    for tag in sorted(tags, key=lambda t: t["createdAt"]):
        commit = git("rev-list", "-n1", tag["tagName"], check=False).strip()
        if commit:
            states.append((tag["tagName"], commit, git("merge-base", commit, upstream).strip()))

    tree = work / "tree"
    git("worktree", "add", "-q", "--detach", str(tree), states[0][1])
    env = {**os.environ, "GIT_EDITOR": "true", "GIT_AUTHOR_NAME": "replay",
           "GIT_AUTHOR_EMAIL": "replay@invalid", "GIT_COMMITTER_NAME": "replay",
           "GIT_COMMITTER_EMAIL": "replay@invalid"}
    counts: dict[str, int] = {}
    try:
        for (tag_a, a, base_a), (tag_b, b, base_b) in zip(states, states[1:]):
            if base_a == base_b:
                continue
            git("checkout", "-q", "-f", "--detach", a, cwd=tree)
            git("clean", "-qfdx", cwd=tree)
            status, note = "clean", ""
            step = subprocess.run(["git", "rebase", base_b], cwd=tree, env=env, capture_output=True, text=True)
            while step.returncode:
                if not git("diff", "--name-only", "--diff-filter=U", cwd=tree).strip():
                    status, note = "error", step.stderr.strip()[-200:]
                    break
                resolved = subprocess.run([sys.executable, resolver], cwd=tree, env=env, capture_output=True, text=True)
                if resolved.returncode:
                    status, note = "refused", (resolved.stderr.strip().splitlines() or ["?"])[-1]
                    break
                status = "auto"
                step = subprocess.run(["git", "rebase", "--continue"], cwd=tree, env=env, capture_output=True, text=True)
            if status in ("clean", "auto"):
                new = set(git("log", "--format=%s", f"{base_b}..{b}").splitlines()) - set(
                    git("log", "--format=%s", f"{base_a}..{a}").splitlines())
                hand_edited = set()
                for line in git("log", "--format=%H %s", f"{base_b}..{b}").splitlines():
                    commit, subject = line.split(" ", 1)
                    if subject in new:
                        hand_edited |= set(git("show", "--name-only", "--format=", commit).split())
                differs = [f for f in git("diff", "--name-only", "HEAD", b, cwd=tree).split() if f not in hand_edited]
                if differs:
                    note = f"differs from {tag_b}: {' '.join(differs)}"
            git("rebase", "--abort", cwd=tree, check=False)
            counts[status] = counts.get(status, 0) + 1
            if note:
                print(f"{tag_a} -> {tag_b} [{status}] {note}", flush=True)
    finally:
        git("worktree", "remove", "--force", str(tree), check=False)
        shutil.rmtree(work)
    print("summary:", ", ".join(f"{n} {s}" for s, n in sorted(counts.items())))


if __name__ == "__main__":
    main()
