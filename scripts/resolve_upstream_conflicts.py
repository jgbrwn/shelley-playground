#!/usr/bin/env python3
"""Resolve conflicts in the upstream sync rebase, or refuse.

Most conflicts are two independent changes that touch adjacent lines. Each
file is re-merged in diff3 form; where at most one side edited a hunk's base
and the other only inserted around it, the two compose (resolve_hunk). Only
edit-vs-edit hunks need a rule, and only for the few files listed below;
anything else refuses, and the sync stops without pushing. Composed results
are then held to the full Test workflow before they can reach main.
"""

import re
from pathlib import Path
import subprocess


# Files this fork deletes on purpose. Upstream keeps editing them, so every
# sync reports a modify/delete conflict; the deletion is the resolution.
DELETED_PATHS = {
    ".github/workflows/publish-version-metadata.yml",
}

# Files whose conflicts have ownership semantics a union would get wrong: the
# fork owns README.md, and upstream owns every dependency version.
OWNED_PATHS = {"README.md", "go.mod", "go.sum"}

# Files where both sides have edited the same lines before, each with a rule
# for exactly the shapes that were reviewed. Everything else must compose.
KNOWN_PATHS = OWNED_PATHS | {".github/workflows/release.yml"}


def marker_content(lines: list[str], start: int) -> tuple[list[str], list[str], int]:
    ours: list[str] = []
    theirs: list[str] = []
    i = start + 1
    while i < len(lines) and not lines[i].startswith("=======\n"):
        ours.append(lines[i])
        i += 1
    if i == len(lines):
        raise ValueError("unterminated rebase conflict")

    i += 1
    while i < len(lines) and not lines[i].startswith(">>>>>>> "):
        theirs.append(lines[i])
        i += 1
    if i == len(lines):
        raise ValueError("unterminated rebase conflict")
    return ours, theirs, i + 1


def normalized(lines: list[str]) -> list[str]:
    return [line.strip() for line in lines if line.strip()]


def resolve_go_sum_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """go.sum conflicts are dependency-line additions from both sides.

    Both sides only add lines, so the union keeps every module hash. main()
    then runs `go mod tidy`, which is authoritative for ordering and for
    direct-vs-indirect placement, so this union only has to be complete.
    """
    content = [line for line in ours + theirs if line.strip()]
    if not content:
        raise ValueError("empty go.sum conflict")
    hashes: dict[tuple[str, str], str] = {}
    for line in content:
        parts = line.split()
        if len(parts) != 3 or not re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", parts[2]):
            raise ValueError("go.sum conflict is not a set of dependency hashes")
        module, version, digest = parts
        key = module, version
        if key in hashes and hashes[key] != digest:
            raise ValueError(f"conflicting go.sum hashes for {module} {version}")
        hashes[key] = digest
    return sorted(set(content))


MODULE_RE = re.compile(r"^[ \t]*(?P<mod>[^/\s][^\s]*) v(?P<ver>\S+)(?P<indirect> // indirect)?[ \t]*$")


def resolve_go_mod_hunk(
    ours: list[str], theirs: list[str], base_text: str
) -> list[str]:
    """Upstream owns dependency versions; the fork adds only edkey.

    A go.mod conflict is upstream bumping a module while the replayed deploy
    commit still carries the old pin. Upstream's line wins: the fork owns no
    dependency versions. A requirement the fork adds is only kept when it is
    absent from stage 1, the commit's original base -- which proves it is a
    real fork addition rather than a stale echo of an upstream pin.
    `go mod tidy` runs afterwards and is authoritative.
    """
    upstream = [line for line in ours if line.strip()]
    fork = [line for line in theirs if line.strip()]
    if not upstream or not fork:
        raise ValueError("go.mod conflict is not a require-block addition")

    for line in upstream + fork:
        if not MODULE_RE.match(line):
            raise ValueError(f"unexpected go.mod conflict line: {line.strip()!r}")

    # A fork line is a stale echo only when its module AND version are exactly
    # what the base already had -- the replayed commit simply carried an old
    # pin. If the fork changed a version from base, that is a real fork-authored
    # change to a dependency version, which upstream owns; stop for a human.
    base_by_module = {}
    for line in base_text.splitlines():
        match = MODULE_RE.match(line)
        if match:
            base_by_module[match.group("mod")] = line.strip()

    upstream_modules = {MODULE_RE.match(line)["mod"] for line in upstream}
    additions = []
    for line in fork:
        match = MODULE_RE.match(line)
        module = match.group("mod")
        if base_by_module.get(module) == line.strip():
            continue  # stale echo of an upstream pin; upstream's line wins
        if module in base_by_module:
            raise ValueError(
                f"go.mod conflicts on the version of {module}, "
                "which the fork does not own"
            )
        additions.append(line)

    # edkey is the sole reviewed fork addition.
    if any(MODULE_RE.match(line)["mod"] != "github.com/mikesmitty/edkey" for line in additions):
        raise ValueError("unreviewed dependency addition in go.mod")

    kept = list(upstream)
    for line in additions:
        if MODULE_RE.match(line)["mod"] not in upstream_modules:
            kept.append(line)
    return kept


def resolve_readme_hunk(
    ours: list[str], theirs: list[str], base_text: str
) -> list[str]:
    """The fork owns README.md; keep its text, guard against new upstream sections.

    The Playground README replaces upstream's introduction, installation, and
    Architecture/History tail. Git aligns that replacement differently
    depending on where upstream's latest edit lands, so enumerating shapes
    never ends: every upstream prose edit produces a new alignment.

    Ownership is the invariant instead: the fork's side wins wholesale, and
    resolve_text checks the merged file still looks like the fork's README.
    If upstream introduced a heading its pre-rebase README lacked, it added a
    section the fork has never seen. That is worth a look but never worth
    blocking upgrades over, so it is reported as an Actions warning.
    """
    theirs_text = normalized(theirs)
    if not theirs_text:
        raise ValueError("empty README.md fork side")

    base_headings = {
        line.strip()
        for line in base_text.splitlines()
        if re.match(r"^#{1,6} ", line.strip())
    }
    added = [
        line.strip()
        for line in ours
        if re.match(r"^#{1,6} ", line.strip()) and line.strip() not in base_headings
    ]
    if added:
        print(
            f"::warning file=README.md::upstream added README sections {added}; "
            "kept the fork's README, consider porting them",
            flush=True,
        )

    return theirs


def resolve_node_setup_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Adopt upstream's mise-pinned Node setup, keeping the fork's skip guard.

    Upstream replaced the two pinned-action setup steps with one
    `scripts/with-node` step. `ui/.nvmrc` no longer exists there, so the fork's
    copy of the old steps is stale by construction. Keep upstream's step and
    re-apply the fork's release-skip condition.
    """
    # Accept only upstream's reviewed step: name, `run: |`, the with-node call,
    # and the GITHUB_PATH append. Anything else stops the sync.
    if (
        normalized(ours)[:3]
        != ["- name: Set up Node.js and pnpm", "run: |", 'dirs="$(scripts/with-node)"']
        or len(normalized(ours)) != 4
        or not normalized(ours)[3].startswith('echo "${dirs//:/')
        or not normalized(ours)[3].endswith('>> "$GITHUB_PATH"')
    ):
        raise ValueError("unknown Node setup conflict hunk")
    skip = "if: steps.version.outputs.skip != 'true'"
    pinned = [
        "- name: Set up pnpm",
        skip,
        "uses: pnpm/action-setup@ea17c68df8912ef543352723c149a84f56e3d413 # v6.1.0",
        "with:",
        "package_json_file: ui/package.json",
        "- name: Set up Node.js",
        skip,
        "uses: actions/setup-node@v4",
        "with:",
        "node-version-file: ui/.nvmrc",
        "cache: 'pnpm'",
        "cache-dependency-path: ui/pnpm-lock.yaml",
    ]
    if normalized(theirs) != pinned:
        raise ValueError("unexpected fork Node setup steps")
    indent = " " * (len(ours[0]) - len(ours[0].lstrip()))
    return [ours[0], indent + "  " + pinned[1] + "\n"] + ours[1:]


def resolve_release_workflow_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Drop upstream's headless-shell release steps, keeping the fork's note.

    Upstream publishes Chromium headless-shell as its own release; the fork
    downloads it from upstream at build time instead. Each upstream edit in
    that region re-conflicts, and the fork's replacement is the stated policy:
    the note that explains why the steps are absent.
    """
    ours_text, theirs_text = normalized(ours), normalized(theirs)
    # Preserve a reviewed upstream action upgrade alongside the fork's
    # existing skip condition. Do not accept a changed condition, repository,
    # or additional commands as though they were a version-only update.
    skip = "if: steps.version.outputs.skip != 'true'"
    if len(ours_text) == 1 and len(theirs_text) == 2 and theirs_text[0] == skip:
        action_pattern = (
            r"uses: pnpm/action-setup@(?:v[0-9]+(?:\.[0-9]+){0,2}|[0-9a-f]{40})"
            r"(?: # [^\n]+)?"
        )
        if re.fullmatch(action_pattern, ours_text[0]) and re.fullmatch(
            action_pattern, theirs_text[1]
        ):
            return [theirs[0]] + ours

    headless_steps = {
        "- name: Resolve headless-shell stable version",
        "- name: Extract and release headless-shell",
    }
    if (
        ours_text[:1] == ["- name: Resolve headless-shell stable version"]
        and "id: headless" in ours_text
        and all(
            line in headless_steps
            for line in ours_text
            if line.startswith("- name:")
        )
        and theirs_text
        and theirs_text[0].startswith("# The headless-shell (Chromium) release step")
        and all(line.startswith("#") for line in theirs_text)
        and any("does not" in line or "is intentionally omitted" in line for line in theirs_text)
    ):
        return theirs
    raise ValueError("unknown release.yml conflict hunk")


class NotAdditive(ValueError):
    """A conflict where both sides edited the same base lines."""


def around(side: list[str], base: list[str]) -> tuple[list[str], list[str]] | None:
    """Split `side` into lines added before and after an untouched `base`.

    Returns None when `side` edited the base. With an empty base the whole
    side is an insertion at the hunk's anchor. A base that occurs more than
    once in `side` is ambiguous and treated as edited.
    """
    if not base:
        return side, []
    n = len(base)
    hits = [k for k in range(len(side) - n + 1) if side[k : k + n] == base]
    if len(hits) != 1:
        return None
    return side[: hits[0]], side[hits[0] + n :]


def resolve_hunk(ours: list[str], base: list[str], theirs: list[str]) -> list[str]:
    """Compose a diff3 hunk when at most one side edited the base.

    A side that kept the base intact only inserted lines around it, and those
    insertions apply unchanged on top of whatever the other side did to the
    base. Upstream's insertions come first, so the result is deterministic.
    """
    if normalized(ours) == normalized(theirs):
        return ours
    up, fork = around(ours, base), around(theirs, base)
    if up and fork:
        return up[0] + fork[0] + base + up[1] + fork[1]
    if fork:
        return fork[0] + ours + fork[1]
    if up:
        return up[0] + theirs + up[1]
    raise NotAdditive("both sides edited the same lines")


def resolve_additive(diff3: str) -> str:
    """Resolve every hunk of `git merge-file --diff3` output with resolve_hunk.

    Unlike the zealous two-way markers in the worktree, a diff3 hunk carries
    each side's whole change and the base lines it replaced, which is what
    tells an insertion apart from an edit.
    """
    lines = diff3.splitlines(keepends=True)
    merged: list[str] = []
    i = 0
    while i < len(lines):
        if not lines[i].startswith("<<<<<<< "):
            merged.append(lines[i])
            i += 1
            continue
        sections: list[list[str]] = [[], [], []]
        part = 0
        i += 1
        while True:
            if i == len(lines):
                raise ValueError("unterminated conflict")
            line = lines[i]
            i += 1
            if part == 0 and line.startswith("||||||| "):
                part = 1
            elif part == 1 and line == "=======\n":
                part = 2
            elif part == 2 and line.startswith(">>>>>>> "):
                break
            else:
                sections[part].append(line)
        if part != 2:
            raise ValueError("malformed diff3 conflict")
        merged.extend(resolve_hunk(*sections))
    return "".join(merged)


def diff3_merge(name: str) -> str:
    """Re-merge a conflicted path's three index stages in diff3 style."""
    staged = {
        fields.split()[2]: fields.split()[1]
        for fields in subprocess.check_output(
            ["git", "ls-files", "--unmerged", "--", name], text=True
        ).splitlines()
    }
    if set(staged) != {"1", "2", "3"}:
        raise NotAdditive("not a both-modified content conflict")
    paths = []
    for stage in ("2", "1", "3"):
        blob = subprocess.check_output(["git", "cat-file", "blob", staged[stage]])
        if b"\0" in blob:
            raise NotAdditive("binary file")
        path = Path(subprocess.check_output(["mktemp"], text=True).strip())
        path.write_bytes(blob)
        paths.append(str(path))
    result = subprocess.run(
        ["git", "merge-file", "-p", "--diff3", "-L", "upstream", "-L", "base", "-L", "fork", *paths],
        capture_output=True,
        text=True,
    )
    for path in paths:
        Path(path).unlink()
    if result.returncode < 0 or result.returncode > 127:
        raise ValueError(f"git merge-file failed: {result.stderr.strip()}")
    return result.stdout


def resolve_text(name: str, text: str, base_text: str = "") -> str:
    lines = text.splitlines(keepends=True)
    merged: list[str] = []
    i = 0

    while i < len(lines):
        if not lines[i].startswith("<<<<<<< "):
            merged.append(lines[i])
            i += 1
            continue

        ours, theirs, after = marker_content(lines, i)
        if not normalized(ours) and not normalized(theirs):
            raise ValueError(f"empty conflict in {name}")

        if name == "README.md":
            replacement = resolve_readme_hunk(ours, theirs, base_text)
        elif name == "go.sum":
            replacement = resolve_go_sum_hunk(ours, theirs)
        elif name == "go.mod":
            replacement = resolve_go_mod_hunk(ours, theirs, base_text)
        elif name == ".github/workflows/release.yml":
            try:
                replacement = resolve_node_setup_hunk(ours, theirs)
            except ValueError:
                replacement = resolve_release_workflow_hunk(ours, theirs)
        else:
            raise ValueError(f"unsupported conflict path: {name}")

        if name in ("go.mod", "go.sum"):
            for line in replacement:
                if line not in merged:
                    merged.append(line)
        else:
            merged.extend(replacement)
        i = after

    result = "".join(merged)
    if name == "README.md":
        for marker in ("# Shelley Playground", "[shelley.dev](https://shelley.dev/)"):
            if marker not in result:
                raise ValueError(f"resolved README.md lost its {marker!r}")
    if name == "go.sum":
        # Check hashes against the surrounding, non-conflicted lines too.
        resolve_go_sum_hunk(result.splitlines(keepends=True), [])
    return result


def resolve_path(name: str) -> None:
    """Resolve and stage one conflicted path, or raise ValueError."""
    path = Path(name)
    if name in DELETED_PATHS:
        stages = {
            line.split()[2]
            for line in subprocess.check_output(
                ["git", "ls-files", "--unmerged", "--", name], text=True
            ).splitlines()
        }
        if stages != {"1", "2"}:
            raise ValueError("expected upstream modify/fork delete")
        if path.exists():
            path.unlink()
        subprocess.run(["git", "rm", "--quiet", "--", name], check=True)
        return
    if name not in OWNED_PATHS:
        try:
            merged = resolve_additive(diff3_merge(name))
        except NotAdditive as error:
            if name not in KNOWN_PATHS:
                raise ValueError(f"{error}; no reviewed rule applies") from error
        else:
            path.write_text(merged)
            # gofmt sorts imports both sides added, and refuses a composition
            # that does not parse.
            if name.endswith(".go") and subprocess.run(["gofmt", "-w", name]).returncode:
                raise ValueError("the composed file is not valid Go")
            print(f"{name}: composed independent changes")
            subprocess.run(["git", "add", "--", name], check=True)
            return
    base_text = ""
    if name in ("README.md", "go.mod"):
        base_text = subprocess.check_output(["git", "show", f":1:{name}"], text=True)
    path.write_text(resolve_text(name, path.read_text(), base_text))
    subprocess.run(["git", "add", "--", name], check=True)


def main() -> None:
    """Resolve every conflicted path; leave only the refused ones unmerged.

    Refused paths are restored as diff3 conflicts (base included) for the
    next resolver, a person or the Copilot step, and the exit status is 1.
    """
    conflicts = subprocess.check_output(
        ["git", "diff", "--name-only", "--diff-filter=U"], text=True
    ).splitlines()
    if not conflicts:
        raise SystemExit("rebase stopped without a conflicted path")

    refused = []
    for name in conflicts:
        try:
            resolve_path(name)
        except ValueError as error:
            refused.append(f"{name}: {error}")
            subprocess.run(["git", "checkout", "--conflict=diff3", "--", name], capture_output=True)

    if "go.sum" in conflicts and not any(r.startswith(("go.sum:", "go.mod:")) for r in refused):
        # GoReleaser runs `go mod tidy` as a before-hook and fails if it
        # rewrites the tree, so a hand-merged dependency graph must be tidied
        # here. tidy also sorts go.sum and moves direct dependencies out of
        # the indirect block, which the union above does not do.
        subprocess.run(["go", "mod", "tidy"], check=True)
        subprocess.run(["git", "add", "--", "go.mod", "go.sum"], check=True)
    if refused:
        raise SystemExit("\n".join(refused))


if __name__ == "__main__":
    main()
