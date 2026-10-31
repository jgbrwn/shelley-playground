#!/usr/bin/env python3
"""Resolve only reviewed additive conflicts in the upstream sync rebase."""

import re
from pathlib import Path
import subprocess
import sys


# Files this fork deletes on purpose. Upstream keeps editing them, so every
# sync reports a modify/delete conflict; the deletion is the resolution.
DELETED_PATHS = {
    ".github/workflows/publish-version-metadata.yml",
}

KNOWN_PATHS = {
    "README.md",
    "go.mod",
    "go.sum",
    "server/server.go",
    "ui/src/vue/App.vue",
    "ui/src/vue/components/ChatInterface.vue",
    "ui/src/vue/components/ChatOverflowMenu.vue",
    "ui/src/vue/components/CommandPalette.vue",
    ".github/workflows/release.yml",
    ".github/workflows/test.yml",
    ".github/workflows/sync-upstream.yml",
}

DEPLOY_ASSIGNMENT = (
    "s.deployManager = deploy.NewManager(nil) // runs persisted on completion via persistRun"
)
APP_HANDLER_SUFFIX = [
    "            commandPaletteOpen = false;\n",
    "          }\n",
    '        "\n',
]
APP_MODAL_SUFFIX = [
    "            focusMessageInputIfUnfocused();\n",
    "          }\n",
    '        "\n',
    "      />\n",
]


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


def resolve_app_hunk(
    lines: list[str],
    ours: list[str],
    theirs: list[str],
    after: int,
) -> tuple[list[str], int]:
    ours_text, theirs_text = normalized(ours), normalized(theirs)

    handler_prefixes = {
        (
            '@open-favicon-emoji-picker="',
            "() => {",
            "faviconEmojiPickerOpen = true;",
        ),
        (
            '@open-deploy-modal="',
            "() => {",
            "deployModalOpen = true;",
        ),
    }
    if tuple(ours_text) in handler_prefixes and tuple(theirs_text) in handler_prefixes:
        if lines[after : after + len(APP_HANDLER_SUFFIX)] != APP_HANDLER_SUFFIX:
            raise ValueError("unknown App.vue command-palette handler conflict")
        return ours + APP_HANDLER_SUFFIX + theirs + APP_HANDLER_SUFFIX, len(APP_HANDLER_SUFFIX)

    modal_prefixes = {
        (
            "<FaviconEmojiPicker",
            ':is-open="faviconEmojiPickerOpen"',
            '@close="',
            "() => {",
            "faviconEmojiPickerOpen = false;",
        ),
        (
            "<DeployModal",
            ':is-open="deployModalOpen"',
            ':suggested-dir="mostRecentCwd ?? undefined"',
            '@close="',
            "() => {",
            "deployModalOpen = false;",
        ),
    }
    if tuple(ours_text) in modal_prefixes and tuple(theirs_text) in modal_prefixes:
        if lines[after : after + len(APP_MODAL_SUFFIX)] != APP_MODAL_SUFFIX:
            raise ValueError("unknown App.vue modal conflict")
        return ours + APP_MODAL_SUFFIX + theirs + APP_MODAL_SUFFIX, len(APP_MODAL_SUFFIX)

    if len(ours_text) == len(theirs_text) == 1:
        known_declarations = {
            'import FaviconEmojiPicker from "./components/FaviconEmojiPicker.vue";',
            'import DeployModal from "./components/DeployModal.vue";',
            'import McpServersModal from "./components/McpServersModal.vue";',
            "const faviconEmojiPickerOpen = ref(false);",
            "const deployModalOpen = ref(false);",
            "const mcpServersModalOpen = ref(mcpLoginResult.value !== null);",
        }
        if ours_text[0] in known_declarations and theirs_text[0] in known_declarations:
            return ours + theirs, 0

        # Both sides bind an independent optional prop to the chat interface,
        # e.g. upstream's MCP-servers modal alongside our deploy modal.
        chat_props = {
            ':on-open-mcp-servers-modal="() => (mcpServersModalOpen = true)"',
            ':on-open-deploy-modal="() => (deployModalOpen = true)"',
        }
        if ours_text[0] in chat_props and theirs_text[0] in chat_props:
            return ours + theirs, 0

    raise ValueError("unknown App.vue conflict hunk")


def resolve_chat_interface_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Preserve independent chat props, handlers, and the upstream slot."""
    ours_text, theirs_text = normalized(ours), normalized(theirs)
    prop_declarations = {
        "onOpenMcpServersModal?: () => void;",
        "onOpenDeployModal?: () => void;",
    }
    if len(ours_text) == len(theirs_text) == 1:
        if (
            ours_text[0] != theirs_text[0]
            and ours_text[0] in prop_declarations
            and theirs_text[0] in prop_declarations
        ):
            return ours + theirs

        # Upstream emits this event for its new-conversation menu item; our
        # deployment feature adds a separate event binding at the same point.
        event_bindings = {
            '@new-conversation="onNewConversationClick"',
            '@open-deploy-modal="props.onOpenDeployModal?.()"',
        }
        if {ours_text[0], theirs_text[0]} == event_bindings:
            return ours + theirs

    new_conversation_slot = (
        ours_text[0:4]
        == [
            ">",
            "<button",
            'class="btn-new"',
            ':aria-label="t(\'newConversation\')"',
        ]
        and ours_text[-2:] == ["</button>", "</ChatOverflowMenu>"]
        and '@click="onNewConversationClick"' in ours_text
        and any(line.startswith("<svg") for line in ours_text)
    )
    if new_conversation_slot and theirs_text == [
        '@open-deploy-modal="props.onOpenDeployModal?.()"',
        "/>",
    ]:
        # Upstream moved the new-conversation button into ChatOverflowMenu's
        # slot. Keep that slot and add our independent event binding to its
        # opening tag; the deployment side's self-closing tag is obsolete.
        return [theirs[0]] + ours

    if (
        not ours_text
        and theirs_text.count('@open-deploy-modal="props.onOpenDeployModal?.()"') == 1
        and set(theirs_text)
        <= {
            '@new-conversation="onNewConversationClick"',
            '@open-deploy-modal="props.onOpenDeployModal?.()"',
        }
        and len(set(theirs_text)) == len(theirs_text)
    ):
        # ChatOverflowMenu no longer emits new-conversation (upstream moved
        # that button into the component itself), so only our deploy binding
        # survives. Dropping the stale one keeps the template honest: an
        # event that no longer exists must not stay bound.
        return [line for line in theirs if line.strip().startswith("@open-deploy-modal=")]

    raise ValueError("unknown ChatInterface.vue conflict hunk")


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
    The guard here is structural: if upstream introduced a heading that its
    own pre-rebase README lacked, upstream added a section the fork has never
    seen, so the sync stops for a human.
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
        raise ValueError(f"upstream added README sections: {added}")

    return theirs


def resolve_node_setup_hunk(
    ours: list[str], theirs: list[str], skip_guarded: bool
) -> list[str]:
    """Adopt upstream's mise-pinned Node setup, keeping the fork's skip guard.

    Upstream replaced the two pinned-action setup steps with one
    `scripts/with-node` step. `ui/.nvmrc` no longer exists there, so the fork's
    copy of the old steps is stale by construction. Keep upstream's step and
    re-apply the fork's release-skip condition; in the sync and test workflows
    there is no such condition, so keep the step as-is.
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
    if skip_guarded:
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
    else:
        pinned = [
            "- name: Set up pnpm",
            "uses: pnpm/action-setup@ea17c68df8912ef543352723c149a84f56e3d413 # v6.1.0",
            "with:",
            "package_json_file: ui/package.json",
            "- name: Set up Node.js",
            "uses: actions/setup-node@v4",
            "with:",
            "node-version-file: ui/.nvmrc",
            "cache: 'pnpm'",
            "cache-dependency-path: ui/pnpm-lock.yaml",
        ]
    if normalized(theirs) != pinned:
        raise ValueError("unexpected fork Node setup steps")
    if not skip_guarded:
        return ours
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


def resolve_chat_overflow_menu_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Keep the deploy action, but not the stale menu block around it.

    Upstream moved version checking below the preferences. The deploy commit
    still carries the old check-version item and old preferences boundary in
    this hunk, so only its divider and deploy button are safe to transplant.
    """
    expected = [
        '<div class="overflow-menu-divider" />',
        '<button class="overflow-menu-item" @click="onDeploy">',
        '<i class="pi pi-upload chat-menu-icon" aria-hidden="true" />',
        "Deploy to new exe.dev VM…",
        "</button>",
        '<div class="overflow-menu-divider" />',
        '<button class="overflow-menu-item" @click="onCheckVersion">',
        '<i class="pi pi-refresh chat-menu-icon" aria-hidden="true" />',
        '{{ t("checkForNewVersion") }}',
        '<span v-if="hasUpdate" class="version-menu-dot" />',
        '<span class="overflow-menu-shortcut"',
        '><kbd>{{ menuShortcutLabel("checkVersion") }}</kbd></span',
        ">",
        "</button>",
        "<!-- Compact view/theme/notification controls -->",
    ]
    if not normalized(ours) and normalized(theirs) == expected:
        # Keep the deploy action and its leading divider. The upstream side
        # after the conflict already supplies the preferences divider, and
        # its current check-version action appears later in the menu.
        return theirs[:6]
    raise ValueError("unknown ChatOverflowMenu.vue conflict hunk")


def resolve_palette_hunk(
    lines: list[str],
    merged: list[str],
    ours: list[str],
    theirs: list[str],
    after: int,
) -> tuple[list[str], int]:
    ours_text, theirs_text = normalized(ours), normalized(theirs)
    emit_signatures = {
        '(e: "open-favicon-emoji-picker"): void;',
        '(e: "open-deploy-modal"): void;',
    }
    if len(ours_text) == len(theirs_text) == 1:
        if ours_text[0] in emit_signatures and theirs_text[0] in emit_signatures:
            return ours + theirs, 0

    action_signatures = {
        "favicon": (
            'id: "favicon-emoji",',
            'title: t("faviconEmoji"),',
            'emit("open-favicon-emoji-picker");',
        ),
        "deploy": (
            'id: "deploy-to-vm",',
            'title: "Deploy to new exe.dev VM",',
            'emit("open-deploy-modal");',
        ),
    }

    def action_kind(content: list[str]) -> str | None:
        text = "\n".join(normalized(content))
        for kind, signature in action_signatures.items():
            if all(part in text for part in signature):
                return kind
        return None

    kinds = (action_kind(ours), action_kind(theirs))
    if set(kinds) == {"favicon", "deploy"}:
        if not merged or merged[-1] != "  items.push({\n":
            raise ValueError("unknown CommandPalette.vue action conflict prefix")
        if after >= len(lines) or lines[after] != "  });\n":
            raise ValueError("unknown CommandPalette.vue action conflict suffix")
        opener = merged.pop()
        closer = lines[after]
        replacement = [opener] + ours + [closer, "\n", opener] + theirs + [closer]
        return replacement, 1

    raise ValueError("unknown CommandPalette.vue conflict hunk")


ASSIGNMENT_RE = re.compile(r"^s\.(?P<field>[A-Za-z0-9_]+) = \S.*$")


def resolve_server_hunk(
    ours: list[str], theirs: list[str], base_text: str
) -> list[str]:
    """Keep upstream edits and the deploy commit's two reviewed additions.

    Rebase HEAD is upstream. Stage 1 is the replayed commit's parent, so a
    line unchanged from stage 1 is inherited context, not a fork addition.
    In particular, never resurrect an import upstream deliberately removed.
    """
    upstream, fork = normalized(ours), normalized(theirs)
    content = upstream + fork
    if not content:
        raise ValueError("empty server/server.go conflict")
    imports = all(re.fullmatch(r'"[A-Za-z0-9_./-]+"', line) for line in content)
    assignments = all(ASSIGNMENT_RE.fullmatch(line) for line in content)
    if not imports and not assignments:
        raise ValueError("unknown server/server.go conflict")
    if assignments:
        for side in (upstream, fork):
            fields = [ASSIGNMENT_RE.fullmatch(line)["field"] for line in side]
            if len(set(fields)) != len(fields):
                raise ValueError("multiple assignments to the same field")
        if any(
            ASSIGNMENT_RE.fullmatch(line)["field"] == "deployManager"
            and line != DEPLOY_ASSIGNMENT
            for line in content
        ):
            raise ValueError("server/server.go disagrees about s.deployManager")

    addition = '"shelley.exe.dev/server/deploy"' if imports else DEPLOY_ASSIGNMENT
    base = set(normalized(base_text.splitlines()))
    if any(line != addition and line not in base for line in fork):
        raise ValueError("unreviewed fork addition in server/server.go")
    kept = [line for line in ours if line.strip()]
    if addition in fork and addition not in upstream:
        kept.extend(line for line in theirs if line.strip() == addition)
    return kept


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

        if name == "server/server.go":
            replacement, consumed = resolve_server_hunk(ours, theirs, base_text), 0
        elif name == "README.md":
            replacement, consumed = resolve_readme_hunk(ours, theirs, base_text), 0
        elif name == "go.sum":
            replacement, consumed = resolve_go_sum_hunk(ours, theirs), 0
        elif name == "go.mod":
            replacement, consumed = resolve_go_mod_hunk(ours, theirs, base_text), 0
        elif name == "ui/src/vue/App.vue":
            replacement, consumed = resolve_app_hunk(lines, ours, theirs, after)
        elif name == "ui/src/vue/components/ChatInterface.vue":
            replacement, consumed = resolve_chat_interface_hunk(ours, theirs), 0
        elif name == ".github/workflows/release.yml":
            try:
                replacement = resolve_node_setup_hunk(ours, theirs, skip_guarded=True)
            except ValueError:
                replacement = resolve_release_workflow_hunk(ours, theirs)
            consumed = 0
        elif name in (
            ".github/workflows/test.yml",
            ".github/workflows/sync-upstream.yml",
        ):
            replacement, consumed = resolve_node_setup_hunk(ours, theirs, skip_guarded=False), 0
        elif name == "ui/src/vue/components/ChatOverflowMenu.vue":
            replacement, consumed = resolve_chat_overflow_menu_hunk(ours, theirs), 0
        elif name == "ui/src/vue/components/CommandPalette.vue":
            replacement, consumed = resolve_palette_hunk(lines, merged, ours, theirs, after)
        else:
            raise ValueError(f"unsupported conflict path: {name}")

        if name in ("server/server.go", "go.mod", "go.sum"):
            for line in replacement:
                if line not in merged:
                    merged.append(line)
        else:
            merged.extend(replacement)
        i = after + consumed

    result = "".join(merged)
    if name == "README.md":
        for marker in ("# Shelley Playground", "[shelley.dev](https://shelley.dev/)"):
            if marker not in result:
                raise ValueError(f"resolved README.md lost its {marker!r}")
    if name == "go.sum":
        # Check hashes against the surrounding, non-conflicted lines too.
        resolve_go_sum_hunk(result.splitlines(keepends=True), [])
    return result


def main() -> None:
    conflicts = subprocess.check_output(
        ["git", "diff", "--name-only", "--diff-filter=U"], text=True
    ).splitlines()
    if not conflicts or any(
        name not in KNOWN_PATHS and name not in DELETED_PATHS for name in conflicts
    ):
        print("Unexpected rebase conflicts:", *conflicts, sep="\n", file=sys.stderr)
        raise SystemExit(1)

    for name in conflicts:
        path = Path(name)
        if name in DELETED_PATHS:
            stages = {
                line.split()[2]
                for line in subprocess.check_output(
                    ["git", "ls-files", "--unmerged", "--", name], text=True
                ).splitlines()
            }
            if stages != {"1", "2"}:
                raise SystemExit(f"{name}: expected upstream modify/fork delete")
            if path.exists():
                path.unlink()
            subprocess.run(["git", "rm", "--quiet", "--", name], check=True)
            continue
        try:
            base_text = ""
            if name in ("server/server.go", "README.md", "go.mod"):
                base_text = subprocess.check_output(
                    ["git", "show", f":1:{name}"], text=True
                )
            path.write_text(resolve_text(name, path.read_text(), base_text))
        except ValueError as error:
            raise SystemExit(f"{name}: {error}") from error
        if name == "server/server.go":
            subprocess.run(["gofmt", "-w", name], check=True)
        subprocess.run(["git", "add", "--", name], check=True)

    if "go.sum" in conflicts:
        # GoReleaser runs `go mod tidy` as a before-hook and fails if it
        # rewrites the tree, so a hand-merged dependency graph must be tidied
        # here. tidy also sorts go.sum and moves direct dependencies out of
        # the indirect block, which the union above does not do.
        subprocess.run(["go", "mod", "tidy"], check=True)
        subprocess.run(["git", "add", "--", "go.mod", "go.sum"], check=True)


if __name__ == "__main__":
    main()
