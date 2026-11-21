#!/usr/bin/env python3
"""Ask Copilot CLI to resolve the conflicts the deterministic resolver refused.

Runs inside a stopped rebase, after resolve_upstream_conflicts.py has staged
everything it could and left only edit-vs-edit conflicts, in diff3 form.
Copilot may read the repository and write only the conflicted files: no
shell, no URLs, no MCP servers. Its result is then verified independently
of anything it claims, and staged only if every check passes:

  - only the conflicted files changed, and no file was created;
  - no conflict markers remain;
  - nothing token-shaped was written (the repository is public);
  - Go files pass gofmt; YAML and JSON files parse.

The decisions and Copilot's own summary are appended to REPORT for review.
A refusal to guess, or any failed check, exits 1 and leaves the conflict
for a person. Usage: copilot_resolve.py REPORT
"""

import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile

# Copilot CLI accepts no cap below 30.
MAX_AI_CREDITS = "50"
CONTEXT_LIMIT = 40_000
MARKER = re.compile(r"^(<{7}|\|{7}|={7}|>{7})( |$)", re.M)
TOKEN = re.compile(r"\b(gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}")


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], text=True)


def clip(text: str) -> str:
    if len(text) <= CONTEXT_LIMIT:
        return text
    return text[:CONTEXT_LIMIT] + "\n[... truncated ...]\n"


def prompt(files: list[str]) -> str:
    replayed = git("log", "-1", "--format=%h %s%n%n%b", "REBASE_HEAD").strip()
    old_base = git("merge-base", "HEAD", "REBASE_HEAD").strip()
    listing = "\n".join(f"- {f}" for f in files)
    return f"""You are resolving a git rebase conflict, unattended, in CI.

The repository is the Shelley Playground: a fork of boldsoftware/shelley
("upstream") that adds a "Deploy to new exe.dev VM" feature and a few CI
changes. Every night the fork's commits are rebased onto upstream's latest
main. A deterministic resolver already handled every conflict where only one
side changed existing lines. What is left is where upstream and the fork
commit both edited the same lines, so a judgement is needed.

Fork commit being replayed:
{replayed}

Conflicted files. Each conflict is in diff3 form: after `<<<<<<<` is
upstream's current version, after `|||||||` the original lines the fork
commit was written against, after `=======` the fork commit's version, ending
with `>>>>>>>`. A file both sides created has no `|||||||` section. When
upstream has adopted the fork's change, keeping upstream's version is right;
if that leaves the fork commit with nothing to add, it is dropped.
Never drop a flag or argument upstream added, even when re-applying the
fork's change to that line.
{listing}

What the fork commit changed in these files:
```diff
{clip(git("show", "--format=", "REBASE_HEAD", "--", *files))}
```

What upstream changed in these files since the fork commit was written:
```diff
{clip(git("diff", old_base, "HEAD", "--", *files))}
```

Rules:
1. Edit only the conflicted files listed above. Do not create, delete or
   rename files, and do not try to run commands.
2. Resolve every conflict and remove all marker lines (`<<<<<<<`, `|||||||`,
   `=======`, `>>>>>>>`). Do not change anything outside the conflicts.
3. Upstream's current version is authoritative for whatever the fork commit
   did not deliberately change. Re-apply the fork commit's intent on top of
   upstream's new code, adapted to it.
4. If upstream now does what the fork change did, or made it obsolete, keep
   upstream's version.
5. If you are not confident about a conflict, leave that file exactly as it
   is, markers included. An unresolved conflict goes to a person; a wrong
   guess ships.
6. End with a short summary: for each file, what you kept from each side
   and why.
"""


def check(files: list[str], before: tuple[set[str], set[str]]) -> None:
    changed = set(git("diff", "--name-only").split())
    untracked = set(git("ls-files", "--others", "--exclude-standard").split())
    if extra := (changed - before[0]) - set(files):
        raise ValueError(f"Copilot changed files it was not given: {sorted(extra)}")
    if new := untracked - before[1]:
        raise ValueError(f"Copilot created files: {sorted(new)}")
    for name in files:
        text = Path(name).read_text()
        if MARKER.search(text):
            raise ValueError(f"{name}: conflict markers remain (Copilot declined or failed)")
        if TOKEN.search(text):
            raise ValueError(f"{name}: contains something shaped like a GitHub token")
        if name.endswith(".go") and subprocess.run(["gofmt", "-l", name], capture_output=True).returncode:
            raise ValueError(f"{name}: not valid Go")
        if name.endswith((".yml", ".yaml")):
            import yaml  # Preinstalled on GitHub-hosted Ubuntu runners.

            yaml.safe_load(text)
        if name.endswith(".json"):
            json.loads(text)


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    report = Path(sys.argv[1])
    root = Path(git("rev-parse", "--show-toplevel").strip())
    files = git("diff", "--name-only", "--diff-filter=U").split()
    for name in files:
        stages = {line.split()[2] for line in git("ls-files", "--unmerged", "--", name).splitlines()}
        if stages not in ({"1", "2", "3"}, {"2", "3"}):
            raise SystemExit(f"{name}: not a both-modified or both-added conflict; left for a person")
    before = (set(git("diff", "--name-only").split()), set(git("ls-files", "--others", "--exclude-standard").split()))
    upstream = {name: git("show", f":2:{name}") for name in files}

    usage = Path(tempfile.mkdtemp()) / "usage.json"
    command = [
        "copilot", "-p", prompt(files), "--silent", "--no-ask-user", "--no-auto-update",
        "--no-custom-instructions", "--disable-builtin-mcps", "--disallow-temp-dir",
        "--max-ai-credits", MAX_AI_CREDITS, "--usage-output-file", str(usage),
        "--allow-tool", "read",
        "--deny-tool", "shell", "--deny-tool", "url", "--deny-tool", "memory",
    ]
    for name in files:
        command += ["--allow-tool", f"write({root / name})"]
    result = subprocess.run(command, capture_output=True, text=True, timeout=900)
    spent = usage.read_text().strip() if usage.exists() else "(not reported)"
    print(f"Copilot usage: {spent[:1000]}", file=sys.stderr)
    if result.returncode:
        raise SystemExit(f"Copilot CLI failed ({result.returncode}): {result.stderr.strip()[-2000:]}")
    try:
        check(files, before)
    except Exception as error:  # Includes YAML and JSON parse errors.
        raise SystemExit(f"Rejected Copilot's resolution: {error}") from error

    replayed = git("log", "-1", "--format=%h %s", "REBASE_HEAD").strip()
    with report.open("a") as out:
        out.write(f"### While replaying `{replayed}`\n\n")
        for name in files:
            diff = subprocess.run(
                ["diff", "-u", "--label", f"upstream/{name}", "--label", f"resolved/{name}", "-", name],
                input=upstream[name], capture_output=True, text=True,
            ).stdout
            out.write(f"Copilot's resolution of `{name}`, compared with upstream's version:\n\n")
            out.write(f"```diff\n{clip(diff)}```\n\n")
        out.write(f"Copilot's summary:\n\n{result.stdout.strip()}\n\n")
        out.write(f"<details><summary>Usage</summary>\n\n```json\n{spent[:4000]}\n```\n</details>\n\n")
    subprocess.run(["git", "add", "--", *files], check=True)
    print(f"Copilot resolved: {' '.join(files)}")


if __name__ == "__main__":
    main()
