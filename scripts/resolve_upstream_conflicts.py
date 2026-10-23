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
    "go.sum",
    "server/server.go",
    "ui/src/vue/App.vue",
    "ui/src/vue/components/ChatInterface.vue",
    "ui/src/vue/components/ChatOverflowMenu.vue",
    "ui/src/vue/components/CommandPalette.vue",
    ".github/workflows/release.yml",
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
    if any(" " not in line.strip() for line in content):
        raise ValueError("go.sum conflict is not a set of dependency lines")
    return sorted(set(content), key=lambda line: line.split(" ", 1)[0])


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


def resolve_readme_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Keep upstream links that survive, with the fork's replacement content.

    The Playground README deliberately replaces two upstream regions: the
    generic Shelley introduction and the Architecture/History tail. Upstream
    edits inside those regions re-conflict on every rebase; the fork's
    replacement wins, keeping upstream's site link in the intro.
    """
    ours_text, theirs_text = normalized(ours), normalized(theirs)
    if (
        ours_text
        and ours_text[0] == "[shelley.dev](https://shelley.dev/)"
        and theirs_text
        and theirs_text[0].startswith("**Shelley Playground** is a fork of")
    ):
        # Preserve the upstream website link, but not the displaced
        # boilerplate; the fork's intro and upstream attribution follow in
        # the surrounding README.
        return [ours[0], "\n"] + theirs

    if (
        ours_text
        and ours_text[0].startswith("The technical stack is Go for the backend")
        and theirs_text[:3]
        == [
            "```bash",
            "git clone https://github.com/jgbrwn/shelley-playground.git",
            "cd shelley-playground",
        ]
        and any(line.startswith("make") for line in theirs_text[3:])
    ):
        # Upstream's Architecture/History tail was replaced by a pointer to
        # ARCHITECTURE.md. The fork's build-from-source block closes with the
        # shared fence after the conflict, so keep it verbatim.
        return theirs

    if (
        not ours_text
        and theirs_text[:1]
        == [
            "See upstream [ARCHITECTURE.md](ARCHITECTURE.md) for the stack details (Go, SQLite, Vue 3 + PrimeVue)."
        ]
        and any("customizing-shelley" in line for line in theirs_text)
    ):
        # Upstream edited the tail that the fork deletes, so git can only
        # express the replacement as "upstream ends here, the fork's tail
        # begins". The fork's tail wins verbatim: it already opens with the
        # blank line that follows the shared build-block fence.
        return theirs

    raise ValueError("unknown README.md conflict hunk")


def resolve_release_workflow_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Drop upstream's headless-shell release steps, keeping the fork's note.

    Upstream publishes Chromium headless-shell as its own release; the fork
    downloads it from upstream at build time instead. Each upstream edit in
    that region re-conflicts, and the fork's replacement is the stated policy:
    the note that explains why the steps are absent.
    """
    theirs_text = normalized(theirs)
    if (
        any("id: headless" in line for line in normalized(ours))
        and theirs_text
        and theirs_text[0].startswith("# The headless-shell (Chromium) release step")
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


def is_import(line: str) -> bool:
    stripped = line.strip()
    return stripped.startswith('"') and stripped.endswith('"')


def assignment_fields(lines: list[str]) -> dict[str, list[str]]:
    """Map each struct field to the assignment lines one side makes to it."""
    fields: dict[str, list[str]] = {}
    for line in lines:
        match = ASSIGNMENT_RE.match(line.strip())
        if match:
            fields.setdefault(match.group("field"), []).append(line.strip())
    return fields


def resolve_server_hunk(ours: list[str], theirs: list[str]) -> list[str]:
    """Keep independent additive lines in NewServer.

    Upstream and the deploy feature both add lines to the same place in
    NewServer: imports of new packages, and single-line assignments to
    struct fields. Distinct fields are independent, so keeping both is the
    resolution.

    When both sides assign the same field, the deploy commit is carrying a
    stale copy of a line upstream owns and has since changed, so upstream's
    line is the truth. The exception is the fork's own deploy assignment: a
    duplicate there would mean upstream and the fork disagree about the same
    field, which is a real conflict.
    """
    content = [line for line in ours + theirs if line.strip()]
    if not content:
        raise ValueError("empty server/server.go conflict")

    imports = [line for line in content if is_import(line)]
    upstream = assignment_fields(ours)
    deploy = assignment_fields(theirs)

    kept: list[str] = []
    seen: set[str] = set()
    for line in content:
        if is_import(line):
            continue
        match = ASSIGNMENT_RE.match(line.strip())
        if not match:
            raise ValueError(
                "server/server.go conflict is not an additive import or "
                f"single-line assignment: {line.strip()!r}"
            )
        field = match.group("field")
        if field in seen:
            continue
        seen.add(field)
        both = upstream.get(field), deploy.get(field)
        if both[0] and both[1] and set(both[0]) != set(both[1]):
            if DEPLOY_ASSIGNMENT in both[0] + both[1]:
                raise ValueError(
                    f"server/server.go disagrees about s.{field}, "
                    "which the deploy feature owns"
                )
            # The deploy commit echoes a line upstream owns and has since
            # changed, so upstream's line is the truth.
            kept.append(both[0][0] + "\n")
            continue
        kept.append(line)

    imports.sort(key=str.strip)
    return imports + kept


def resolve_text(name: str, text: str) -> str:
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
            replacement, consumed = resolve_server_hunk(ours, theirs), 0
        elif name == "README.md":
            replacement, consumed = resolve_readme_hunk(ours, theirs), 0
        elif name == "go.sum":
            replacement, consumed = resolve_go_sum_hunk(ours, theirs), 0
        elif name == "ui/src/vue/App.vue":
            replacement, consumed = resolve_app_hunk(lines, ours, theirs, after)
        elif name == "ui/src/vue/components/ChatInterface.vue":
            replacement, consumed = resolve_chat_interface_hunk(ours, theirs), 0
        elif name == ".github/workflows/release.yml":
            replacement, consumed = resolve_release_workflow_hunk(ours, theirs), 0
        elif name == "ui/src/vue/components/ChatOverflowMenu.vue":
            replacement, consumed = resolve_chat_overflow_menu_hunk(ours, theirs), 0
        elif name == "ui/src/vue/components/CommandPalette.vue":
            replacement, consumed = resolve_palette_hunk(lines, merged, ours, theirs, after)
        else:
            raise ValueError(f"unsupported conflict path: {name}")

        if name in ("server/server.go", "go.sum"):
            for line in replacement:
                if line not in merged:
                    merged.append(line)
        else:
            merged.extend(replacement)
        i = after + consumed

    return "".join(merged)


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
            if path.exists():
                path.unlink()
            subprocess.run(["git", "rm", "--quiet", "--", name], check=True)
            continue
        try:
            path.write_text(resolve_text(name, path.read_text()))
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
