#!/usr/bin/env python3
"""Replay every historical upstream sync with a resolver; compare to what shipped.

For each pair of consecutive playground releases A, B whose upstream bases
differ, rebase A onto B's upstream base with scripts/sync_rebase.sh, exactly
as the sync does but without Copilot, resolving conflicts with RESOLVER. It
reports whether each rebase was clean, resolved automatically, or refused. An automatic result is compared with B's tree,
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
    # Run copies from outside the worktree, as the sync does: rebasing
    # replaces scripts/. The resolver under test may come from elsewhere.
    for name in ("sync_rebase.sh", "copilot_resolve.py"):
        shutil.copy(root / "scripts" / name, work / name)
    shutil.copy(sys.argv[2] if len(sys.argv) == 3 else root / "scripts/resolve_upstream_conflicts.py",
                work / "resolve_upstream_conflicts.py")
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
            out = work / "out"
            shutil.rmtree(out, ignore_errors=True)
            # No COPILOT_GITHUB_TOKEN: replay what the deterministic resolver does.
            step = subprocess.run([str(work / "sync_rebase.sh"), base_b, str(out)], cwd=tree,
                                  env={k: v for k, v in env.items() if k != "COPILOT_GITHUB_TOKEN"},
                                  capture_output=True, text=True)
            note = ""
            if step.returncode:
                status = "refused"
                report = out / "refused.txt"
                note = report.read_text().strip().splitlines()[0] if report.exists() else step.stderr.strip()[-200:]
            else:
                status = "auto" if (out / "stops.txt").exists() else "clean"
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
            counts[status] = counts.get(status, 0) + 1
            if note:
                print(f"{tag_a} -> {tag_b} [{status}] {note}", flush=True)
    finally:
        git("worktree", "remove", "--force", str(tree), check=False)
        shutil.rmtree(work)
    print("summary:", ", ".join(f"{n} {s}" for s, n in sorted(counts.items())))


if __name__ == "__main__":
    main()
