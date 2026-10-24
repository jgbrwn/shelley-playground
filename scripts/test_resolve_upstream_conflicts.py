from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from resolve_upstream_conflicts import resolve_text


def conflict_fixture(text):
    return (
        text.replace("@@START@@", "<" * 7)
        .replace("@@SEPARATOR@@", "=" * 7)
        .replace("@@END@@", ">" * 7)
    )


class ConflictResolverTest(unittest.TestCase):
    def test_readme_keeps_upstream_site_link_and_playground_intro(self):
        readme = conflict_fixture('''# Shelley Playground — exe.dev-Deployer

@@START@@ HEAD
[shelley.dev](https://shelley.dev/)

Shelley is a mobile-friendly, web-based, multi-conversation, multi-modal,
multi-model, single-user coding agent built for but not exclusive to
[exe.dev](https://exe.dev/). It does not come with authorization or sandboxing:
bring your own.
@@SEPARATOR@@
**Shelley Playground** is a fork of [Shelley](https://github.com/boldsoftware/shelley) (by [Bold Software](https://github.com/boldsoftware)) that you can install on your MacBook or Linux system — or use to **replace Shelley on an existing exe.dev VM**.
@@END@@ deploy
> Upstream: **https://github.com/boldsoftware/shelley**
''')
        resolved = resolve_text("README.md", readme)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("# Shelley Playground — exe.dev-Deployer", resolved)
        self.assertIn("[shelley.dev](https://shelley.dev/)", resolved)
        self.assertIn("**Shelley Playground** is a fork", resolved)
        self.assertIn("> Upstream:", resolved)
        self.assertNotIn("bring your own.", resolved)

    def test_readme_keeps_fork_build_block_over_upstream_architecture_tail(self):
        # Upstream extended its Architecture section (idle compaction); the
        # fork replaced that whole tail with a pointer to ARCHITECTURE.md.
        readme = conflict_fixture('''### Build from source (any machine)

@@START@@ HEAD
The technical stack is Go for the backend, SQLite for storage, and Typescript
with Vue 3 + PrimeVue for the UI.

A profile's `idle_compaction_minutes` makes its top-level conversations compact
once idle that long after a turn.

# History

Shelley is partially based on our previous coding agent effort, [Sketch](https://github.com/boldsoftware/sketch).

# Shelley's Name

Shelley is so named because the main tool it uses is the shell, and I like
putting "-ey" at the end of words.

```
socat TCP-LISTEN:9001,fork TCP:localhost:9000
@@SEPARATOR@@
```bash
git clone https://github.com/jgbrwn/shelley-playground.git
cd shelley-playground
make        # or `make build-custom` for a stamped, deploy-capable binary
@@END@@ deploy
```

See upstream [ARCHITECTURE.md](ARCHITECTURE.md) for the stack details (Go, SQLite, Vue 3 + PrimeVue).
''')
        resolved = resolve_text("README.md", readme)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("git clone https://github.com/jgbrwn/shelley-playground.git", resolved)
        self.assertIn("make        # or `make build-custom`", resolved)
        self.assertEqual(resolved.count("```"), 2)
        self.assertNotIn("The technical stack is Go", resolved)
        self.assertNotIn("socat TCP-LISTEN", resolved)
        self.assertIn("[ARCHITECTURE.md](ARCHITECTURE.md)", resolved)

    def test_readme_keeps_fork_tail_when_upstream_edited_the_deleted_tail(self):
        # Upstream reworded the compaction paragraph it deletes, so git can
        # only express the replacement as an empty upstream side followed by
        # the fork's whole tail.
        readme = conflict_fixture('''make        # or `make build-custom` for a stamped, deploy-capable binary
```
@@START@@ HEAD
@@SEPARATOR@@

See upstream [ARCHITECTURE.md](ARCHITECTURE.md) for the stack details (Go, SQLite, Vue 3 + PrimeVue).

---

## Keeping it fresh: rebasing against upstream Shelley

- Shelley skill for customizing your own Shelley: search for `customizing-shelley` in the repo's `AGENTS.md`
@@END@@ deploy
''')
        resolved = resolve_text("README.md", readme)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("make        # or `make build-custom`", resolved)
        self.assertIn("[ARCHITECTURE.md](ARCHITECTURE.md)", resolved)
        self.assertIn("## Keeping it fresh", resolved)
        # The blank line separating the fence from the fork's tail is not
        # doubled by the resolution.
        self.assertIn("```\n\nSee upstream [ARCHITECTURE.md]", resolved)
        self.assertNotIn("```\n\n\n", resolved)

    def test_release_workflow_drops_upstream_headless_shell_steps(self):
        # Upstream keeps editing its headless-shell release steps; the fork
        # removes them and leaves a note explaining why.
        workflow = conflict_fixture('''      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@v6
@@START@@ HEAD
      - name: Resolve headless-shell stable version
        id: headless
        run: |
          VERSION=$(python3 scripts/extract-headless-shell.py --resolve-version)
@@SEPARATOR@@
# The headless-shell (Chromium) release step is intentionally omitted from
# the playground fork. Upstream Shelley publishes headless-shell as a separate
# GitHub release on boldsoftware/shelley; the playground fork downloads it
# from there at build time.
@@END@@ deploy
''')
        resolved = resolve_text(".github/workflows/release.yml", workflow)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertNotIn("extract-headless-shell.py --resolve-version", resolved)
        self.assertIn("intentionally omitted from", resolved)
        self.assertIn("- name: Run GoReleaser", resolved)

    def test_chat_interface_drops_stale_new_conversation_binding(self):
        # ChatOverflowMenu stopped emitting new-conversation, so the deploy
        # commit's binding for it must not survive the resolution.
        chat = conflict_fixture('''          @check-version="openVersionModal"
@@START@@ HEAD
@@SEPARATOR@@
          @new-conversation="onNewConversationClick"
          @open-deploy-modal="props.onOpenDeployModal?.()"
@@END@@ deploy
        >
''')
        resolved = resolve_text("ui/src/vue/components/ChatInterface.vue", chat)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertEqual(
            resolved.count('@open-deploy-modal="props.onOpenDeployModal?.()"'), 1
        )
        self.assertNotIn("@new-conversation=", resolved)

    def test_server_keeps_new_upstream_assignment_alongside_deploy(self):
        # Today's real hunk: upstream added a new field assignment at the
        # deploy feature's insertion point.
        server = conflict_fixture('''func NewServer(s *Server) {
    s.integrationSkills = newIntegrationSkillCache(logger, currentIntegrationSkillDiscoverer(logger))
@@START@@ HEAD
    s.listIntegrations = listCurrentIntegrations
@@SEPARATOR@@
    s.deployManager = deploy.NewManager(nil) // runs persisted on completion via persistRun
@@END@@ deploy
''')
        resolved = resolve_text("server/server.go", server)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("s.listIntegrations = listCurrentIntegrations", resolved)
        self.assertIn("s.deployManager = deploy.NewManager(nil)", resolved)
        # Upstream's line is preserved ahead of the fork's, as in the file.
        self.assertLess(
            resolved.index("s.listIntegrations"),
            resolved.index("s.deployManager"),
        )

    def test_readme_keeps_fork_heading_when_upstream_edits_inside_deleted_tail(
        self,
    ):
        # Upstream edited a line inside the Architecture/History tail the fork
        # deletes, so git aligned the hunk with upstream's whole tail as "ours"
        # against a single fork heading.
        readme = conflict_fixture("""README BEFORE
@@START@@ HEAD
# Architecture

The technical stack is Go for the backend, SQLite for storage, and Typescript
with Vue 3 + PrimeVue for the UI.

# History

Shelley is based on Sketch.

# Building Shelley

Run `make`. Run `make serve` to start Shelley locally, or `make serve-dev`.
@@SEPARATOR@@
### Build from source (any machine)
@@END@@ deploy

```bash
git clone https://github.com/jgbrwn/shelley-playground.git
```
""")
        resolved = resolve_text("README.md", readme)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("### Build from source (any machine)", resolved)
        self.assertIn("git clone https://github.com/jgbrwn/shelley-playground.git", resolved)
        self.assertNotIn("The technical stack is Go", resolved)
        self.assertNotIn("# Architecture", resolved)
        self.assertNotIn("make serve-dev", resolved)

    def test_app_preserves_favicon_and_deploy_features(self):
        app = conflict_fixture('''<template>
@@START@@ HEAD
        @open-favicon-emoji-picker="
          () => {
            faviconEmojiPickerOpen = true;
@@SEPARATOR@@
        @open-deploy-modal="
          () => {
            deployModalOpen = true;
@@END@@ deploy
            commandPaletteOpen = false;
          }
        "
@@START@@ HEAD
      <FaviconEmojiPicker
        :is-open="faviconEmojiPickerOpen"
        @close="
          () => {
            faviconEmojiPickerOpen = false;
@@SEPARATOR@@
      <DeployModal
        :is-open="deployModalOpen"
        :suggested-dir="mostRecentCwd ?? undefined"
        @close="
          () => {
            deployModalOpen = false;
@@END@@ deploy
            focusMessageInputIfUnfocused();
          }
        "
      />
@@START@@ HEAD
import FaviconEmojiPicker from "./components/FaviconEmojiPicker.vue";
@@SEPARATOR@@
import DeployModal from "./components/DeployModal.vue";
@@END@@ deploy
@@START@@ HEAD
const faviconEmojiPickerOpen = ref(false);
@@SEPARATOR@@
const deployModalOpen = ref(false);
@@END@@ deploy
</template>
''')
        resolved = resolve_text("ui/src/vue/App.vue", app)
        self.assertNotIn("<<<<<<<", resolved)
        for feature in (
            "@open-favicon-emoji-picker",
            "@open-deploy-modal",
            "<FaviconEmojiPicker",
            "<DeployModal",
            "FaviconEmojiPicker.vue",
            "DeployModal.vue",
            "faviconEmojiPickerOpen",
            "deployModalOpen",
        ):
            self.assertIn(feature, resolved)
        self.assertEqual(resolved.count("focusMessageInputIfUnfocused();"), 2)

    def test_palette_preserves_both_commands(self):
        palette = conflict_fixture('''<script>
@@START@@ HEAD
  (e: "open-favicon-emoji-picker"): void;
@@SEPARATOR@@
  (e: "open-deploy-modal"): void;
@@END@@ deploy
  items.push({
@@START@@ HEAD
    id: "favicon-emoji",
    type: "action",
    title: t("faviconEmoji"),
    subtitle: t("pickFaviconEmoji"),
    icon: ICON_SMILE,
    action: () => {
      emit("open-favicon-emoji-picker");
      emit("close");
    },
    keywords: ["favicon", "emoji", "icon", "tab", "change", "edit", "customize"],
@@SEPARATOR@@
    id: "deploy-to-vm",
    type: "action",
    title: "Deploy to new exe.dev VM",
    subtitle: "Forklift the current project directory onto a fresh VM",
    icon: ICON_UPLOAD,
    action: () => {
      emit("open-deploy-modal");
      emit("close");
    },
    keywords: ["deploy", "forklift", "production", "vm", "exe", "ship", "release"],
@@END@@ deploy
  });
</script>
''')
        resolved = resolve_text("ui/src/vue/components/CommandPalette.vue", palette)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn('(e: "open-favicon-emoji-picker"): void;', resolved)
        self.assertIn('(e: "open-deploy-modal"): void;', resolved)
        self.assertEqual(resolved.count("  items.push({"), 2)
        self.assertEqual(resolved.count('id: "favicon-emoji",'), 1)
        self.assertEqual(resolved.count('id: "deploy-to-vm",'), 1)

    def test_server_additive_assignments_and_imports(self):
        server = conflict_fixture('''package server
import (
@@START@@ HEAD
    "skills"
@@SEPARATOR@@
    "shelley.exe.dev/server/deploy"
@@END@@ deploy
)
func initServer(s *Server) {
@@START@@ HEAD
    s.integrationSkills = newIntegrationSkillCache(logger, currentIntegrationSkillDiscoverer(logger))
@@SEPARATOR@@
    s.integrationSkills = discoverIntegrationSkillsAtStartup(logger)
    s.deployManager = deploy.NewManager(nil) // runs persisted on completion via persistRun
@@END@@ deploy
}
''')
        resolved = resolve_text(
            "server/server.go", server,
            "s.integrationSkills = discoverIntegrationSkillsAtStartup(logger)\n",
        )
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn('"shelley.exe.dev/server/deploy"', resolved)
        self.assertIn('"skills"', resolved)
        self.assertIn("s.integrationSkills = newIntegrationSkillCache", resolved)
        self.assertIn("s.deployManager = deploy.NewManager(nil)", resolved)

    def test_app_preserves_mcp_and_deploy_props(self):
        # Upstream's MCP-servers modal and our deploy modal both bind a prop to
        # the chat interface at the same spot.
        app = conflict_fixture('''<template>
          :on-open-models-modal="() => (modelsModalOpen = true)"
@@START@@ HEAD
          :on-open-mcp-servers-modal="() => (mcpServersModalOpen = true)"
@@SEPARATOR@@
          :on-open-deploy-modal="() => (deployModalOpen = true)"
@@END@@ deploy
          :on-open-file-finder="openFileFinder"
''')
        resolved = resolve_text("ui/src/vue/App.vue", app)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn(":on-open-mcp-servers-modal=", resolved)
        self.assertIn(":on-open-deploy-modal=", resolved)
        self.assertIn(":on-open-file-finder=", resolved)

    def test_chat_interface_preserves_mcp_and_deploy_props(self):
        chat = conflict_fixture('''    onOpenModelsModal?: () => void;
@@START@@ HEAD
    onOpenMcpServersModal?: () => void;
@@SEPARATOR@@
    onOpenDeployModal?: () => void;
@@END@@ deploy
    onOpenFileFinder?: () => void;
''')
        resolved = resolve_text("ui/src/vue/components/ChatInterface.vue", chat)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("onOpenMcpServersModal?: () => void;", resolved)
        self.assertIn("onOpenDeployModal?: () => void;", resolved)
        self.assertIn("onOpenFileFinder?: () => void;", resolved)

    def test_chat_interface_keeps_upstream_slot_and_deploy_handler(self):
        chat = conflict_fixture('''          @check-version="openVersionModal"
@@START@@ HEAD
        >
          <button
            class="btn-new"
            :aria-label="t('newConversation')"
            @click="onNewConversationClick"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-icon-1rem">
              <path d="M12 4v16m8-8H4" />
            </svg>
          </button>
        </ChatOverflowMenu>
@@SEPARATOR@@
          @open-deploy-modal="props.onOpenDeployModal?.()"
        />
@@END@@ deploy
      </div>
''')
        resolved = resolve_text("ui/src/vue/components/ChatInterface.vue", chat)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn('@open-deploy-modal="props.onOpenDeployModal?.()"', resolved)
        self.assertIn('class="btn-new"', resolved)
        self.assertIn("@click=\"onNewConversationClick\"", resolved)
        self.assertIn("</ChatOverflowMenu>", resolved)
        self.assertNotIn("        />", resolved)

    def test_chat_interface_preserves_new_conversation_and_deploy_handlers(self):
        chat = conflict_fixture('''          @check-version="openVersionModal"
@@START@@ HEAD
          @new-conversation="onNewConversationClick"
@@SEPARATOR@@
          @open-deploy-modal="props.onOpenDeployModal?.()"
@@END@@ deploy
        >
''')
        resolved = resolve_text("ui/src/vue/components/ChatInterface.vue", chat)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertEqual(resolved.count('@new-conversation="onNewConversationClick"'), 1)
        self.assertEqual(
            resolved.count('@open-deploy-modal="props.onOpenDeployModal?.()"'), 1
        )

    def test_chat_interface_rejects_mixed_prop_and_event_conflicts(self):
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/components/ChatInterface.vue",
                "<<<<<<< ours\n"
                '@new-conversation="onNewConversationClick"\n'
                "=======\n"
                "onOpenDeployModal?: () => void;\n"
                ">>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/components/ChatInterface.vue",
                "<<<<<<< ours\n"
                '@new-conversation="onNewConversationClick"\n'
                "=======\n"
                '@new-conversation="onNewConversationClick"\n'
                ">>>>>>> theirs\n",
            )

    def test_chat_overflow_menu_keeps_deploy_without_stale_menu_items(self):
        menu = conflict_fixture('''      <button class="overflow-menu-item" @click="onMcpServers">
        {{ t("mcpServers") }}
      </button>
@@START@@ HEAD
@@SEPARATOR@@

      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onDeploy">
        <i class="pi pi-upload chat-menu-icon" aria-hidden="true" />
        Deploy to new exe.dev VM…
      </button>

      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onCheckVersion">
        <i class="pi pi-refresh chat-menu-icon" aria-hidden="true" />
        {{ t("checkForNewVersion") }}
        <span v-if="hasUpdate" class="version-menu-dot" />
        <span class="overflow-menu-shortcut"
          ><kbd>{{ menuShortcutLabel("checkVersion") }}</kbd></span
        >
      </button>

      <!-- Compact view/theme/notification controls -->
@@END@@ deploy
      <div class="overflow-menu-divider" />
      <div class="overflow-quick-controls" />
      <button class="overflow-menu-item" @click="onCheckVersion">
        {{ t("checkForNewVersion") }}
      </button>
''')
        resolved = resolve_text("ui/src/vue/components/ChatOverflowMenu.vue", menu)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertEqual(resolved.count("Deploy to new exe.dev VM…"), 1)
        self.assertEqual(resolved.count("{{ t(\"checkForNewVersion\") }}"), 1)
        self.assertEqual(resolved.count('@click="onCheckVersion"'), 1)
        self.assertNotIn("Compact view/theme/notification controls", resolved)
        self.assertEqual(resolved.count('<div class="overflow-menu-divider" />'), 2)

    def test_go_sum_takes_the_union_of_dependency_lines(self):
        # Upstream pulled in MCP/OAuth deps; our deploy SSH-key code pulled in
        # edkey. Both sides only add lines, and go.sum must carry both.
        gosum = conflict_fixture('''github.com/mattn/go-isatty v0.0.24 h1:tGZZoVgT/KiqK1c8ocVLeDS8BSWMRd47J3Lbz7vsReI=
@@START@@ HEAD
@@SEPARATOR@@
github.com/mikesmitty/edkey v0.0.0-20170222072505-3356ea4e686a h1:eU8j/ClY2Ty3qdHnn0TyW3ivFoPC/0F1gQZz8yTxbbE=
@@END@@ deploy
github.com/ncruces/go-strftime v1.0.0 h1:HMFp8mLCTPp341M/ZnA4qaf7ZlsbTc+miZjCLOFAw7w=
''')
        resolved = resolve_text("go.sum", gosum)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("mattn/go-isatty", resolved)
        self.assertIn("mikesmitty/edkey", resolved)
        self.assertIn("ncruces/go-strftime", resolved)

    def test_release_preserves_pinned_action_and_skip_guard(self):
        for old, version in (
            ("v4", "ea17c68df8912ef543352723c149a84f56e3d413 # v6.1.0"),
            ("ea17c68df8912ef543352723c149a84f56e3d413 # v6.1.0", "v7"),
        ):
            with self.subTest(old=old, version=version):
                hunk = conflict_fixture(f"""      - name: Set up pnpm
@@START@@ HEAD
        uses: pnpm/action-setup@{version}
@@SEPARATOR@@
        if: steps.version.outputs.skip != 'true'
        uses: pnpm/action-setup@{old}
@@END@@ fork
        with:
          package_json_file: ui/package.json
""")
                self.assertEqual(
                    resolve_text(".github/workflows/release.yml", hunk),
                    f"""      - name: Set up pnpm
        if: steps.version.outputs.skip != 'true'
        uses: pnpm/action-setup@{version}
        with:
          package_json_file: ui/package.json
""",
                )

    def test_release_rejects_non_version_changes(self):
        for ours, theirs in (
            ("uses: other/action@v6", "if: steps.version.outputs.skip != 'true'\nuses: pnpm/action-setup@v4"),
            ("uses: pnpm/action-setup@v6", "if: always()\nuses: pnpm/action-setup@v4"),
            ("uses: pnpm/action-setup@v6\nrun: unexpected", "if: steps.version.outputs.skip != 'true'\nuses: pnpm/action-setup@v4"),
        ):
            with self.subTest(ours=ours, theirs=theirs), self.assertRaises(ValueError):
                resolve_text(
                    ".github/workflows/release.yml",
                    f"<<<<<<< HEAD\n{ours}\n=======\n{theirs}\n>>>>>>> fork\n",
                )

    def test_headless_rule_cannot_discard_other_release_steps(self):
        note = "# The headless-shell (Chromium) release step is intentionally omitted from\n"
        for upstream, fork in (
            ("- name: Unrelated\nid: headless\n", note),
            ("- name: Resolve headless-shell stable version\nid: headless\n- name: Unrelated\n", note),
            ("- name: Resolve headless-shell stable version\nid: headless\n", note + "run: unrelated\n"),
        ):
            with self.subTest(upstream=upstream, fork=fork), self.assertRaises(ValueError):
                resolve_text(
                    ".github/workflows/release.yml",
                    f"<<<<<<< HEAD\n{upstream}=======\n{fork}>>>>>>> fork\n",
                )

    def test_server_drops_only_inherited_imports_removed_by_upstream(self):
        hunk = conflict_fixture('''import (
@@START@@ HEAD
@@SEPARATOR@@
    "shelley.exe.dev/server/deploy"
    "shelley.exe.dev/server/diskspace"
@@END@@ fork
    "shelley.exe.dev/server/resource"
)
''')
        resolved = resolve_text(
            "server/server.go", hunk, '"shelley.exe.dev/server/diskspace"\n'
        )
        self.assertNotIn("diskspace", resolved)
        self.assertIn('"shelley.exe.dev/server/deploy"', resolved)
        self.assertIn('"shelley.exe.dev/server/resource"', resolved)
        # Without the base proving it is inherited, discarding it is unsafe.
        with self.assertRaises(ValueError):
            resolve_text("server/server.go", hunk)

    def test_server_rejects_competing_or_unknown_fork_assignments(self):
        for ours, theirs in (
            ("s.other = upstream()", "s.other = fork()"),
            ("s.deployManager = upstream()", "s.deployManager = fork()"),
            ("s.other = upstream()", "s.other = one()\ns.other = two()"),
            ("s.other = one()\ns.other = two()", "s.deployManager = deploy.NewManager(nil)"),
        ):
            with self.subTest(ours=ours, theirs=theirs), self.assertRaises(ValueError):
                resolve_text(
                    "server/server.go",
                    f"<<<<<<< HEAD\n{ours}\n=======\n{theirs}\n>>>>>>> fork\n",
                )

    def test_readme_rejects_marker_mentions_and_unknown_replacements(self):
        for ours, theirs in (
            ("Important content mentioning # Architecture", "replacement"),
            ("# Architecture\nThe technical stack is Go for the backend", "arbitrary new text"),
            ("# Architecture\nThe technical stack is Go for the backend\n# Security\nImportant", "### Build from source (any machine)"),
            ("# Architecture", "### Build from source (any machine)"),
        ):
            with self.subTest(ours=ours, theirs=theirs), self.assertRaises(ValueError):
                resolve_text(
                    "README.md",
                    f"<<<<<<< HEAD\n{ours}\n=======\n{theirs}\n>>>>>>> fork\n",
                )

    def test_go_sum_rejects_malformed_and_competing_hashes(self):
        a, b = "A" * 43 + "=", "B" * 43 + "="
        for ours, theirs in (
            ("not a checksum", "also invalid"),
            (f"example.com/mod v1.0.0 h1:{a}", f"example.com/mod v1.0.0 h1:{b}"),
        ):
            with self.subTest(ours=ours), self.assertRaises(ValueError):
                resolve_text(
                    "go.sum",
                    f"<<<<<<< HEAD\n{ours}\n=======\n{theirs}\n>>>>>>> fork\n",
                )
        with self.assertRaises(ValueError):
            resolve_text(
                "go.sum",
                f"example.com/mod v1.0.0 h1:{a}\n"
                f"<<<<<<< HEAD\n=======\nexample.com/mod v1.0.0 h1:{b}\n>>>>>>> fork\n",
            )

    def test_rejects_unknown_hunks_and_paths(self):
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/App.vue",
                "<<<<<<< ours\nconst other = true;\n=======\nconst other = false;\n>>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/Unknown.vue",
                "<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "README.md",
                "<<<<<<< ours\nunknown\n=======\nunknown\n>>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "README.md",
                "<<<<<<< ours\nSomething upstream entirely new\n=======\nunknown\n>>>>>>> theirs\n",
            )
        # An empty-ours hunk is only accepted when the fork's own tail anchors
        # it; anything else must still fail closed.
        with self.assertRaises(ValueError):
            resolve_text(
                "README.md",
                "<<<<<<< ours\n=======\nSee upstream [ARCHITECTURE.md](ARCHITECTURE.md) for the stack details (Go, SQLite, Vue 3 + PrimeVue).\n>>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "server/server.go",
                "<<<<<<< ours\n"
                "    s.deployManager = deploy.NewManager(nil) // runs persisted on completion via persistRun\n"
                "=======\n"
                "    s.deployManager = somethingElse()\n"
                ">>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "server/server.go",
                "<<<<<<< ours\n"
                "    notAnAssignment()\n"
                "=======\n"
                "    s.deployManager = deploy.NewManager(nil)\n"
                ">>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/components/ChatOverflowMenu.vue",
                "<<<<<<< ours\nunknown\n=======\nunknown\n>>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/components/ChatInterface.vue",
                "<<<<<<< ours\n<UnknownSlot />\n=======\n<UnknownProp />\n>>>>>>> theirs\n",
            )


class GitConflictResolverTest(unittest.TestCase):
    """Exercise stage-1 reading and staging, not just string transformations."""

    def test_deleted_publisher_requires_modify_delete_stages(self):
        resolver = Path(__file__).with_name("resolve_upstream_conflicts.py").resolve()
        name = ".github/workflows/publish-version-metadata.yml"
        for stages in ((1, 2), (1, 2, 3), (1, 3)):
            with self.subTest(stages=stages), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                subprocess.run(
                    ["git", "init", "-q", temp], check=True, capture_output=True
                )
                blob = subprocess.check_output(
                    ["git", "hash-object", "-w", "--stdin"], input="publisher\n",
                    text=True, cwd=root,
                ).strip()
                subprocess.run(
                    ["git", "update-index", "--index-info"], cwd=root, check=True,
                    input="".join(f"100644 {blob} {stage}\t{name}\n" for stage in stages),
                    text=True,
                )
                path = root / name
                path.parent.mkdir(parents=True)
                path.write_text("publisher\n")
                result = subprocess.run(
                    [sys.executable, str(resolver)], cwd=root,
                    text=True, capture_output=True,
                )
                if stages == (1, 2):
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertFalse(path.exists())
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("expected upstream modify/fork delete", result.stderr)
                    self.assertEqual(path.read_text(), "publisher\n")

    def test_server_import_rebase(self):
        resolver = Path(__file__).with_name("resolve_upstream_conflicts.py").resolve()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)

            def git(*args, check=True):
                return subprocess.run(
                    ["git", *args], cwd=root, text=True,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check,
                )

            git("init", "-b", "base")
            git("config", "user.name", "Resolver test")
            git("config", "user.email", "resolver@example.test")
            path = root / "server/server.go"
            path.parent.mkdir()
            path.write_text('package server\nimport (\n"shelley.exe.dev/server/diskspace"\n)\n')
            git("add", ".")
            git("commit", "-m", "base")
            git("checkout", "-b", "fork")
            path.write_text(
                'package server\nimport (\n"shelley.exe.dev/server/deploy"\n'
                '"shelley.exe.dev/server/diskspace"\n)\n'
            )
            git("commit", "-am", "deploy import")
            git("checkout", "-b", "upstream", "base")
            path.write_text(
                'package server\nimport (\n"shelley.exe.dev/server/resource"\n)\n'
            )
            git("commit", "-am", "replace diskspace with resource")
            git("checkout", "fork")
            result = git("rebase", "upstream", check=False)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            subprocess.run([sys.executable, str(resolver)], cwd=root, check=True)
            self.assertEqual(git("diff", "--name-only", "--diff-filter=U").stdout, "")
            staged = git("show", ":server/server.go").stdout
            self.assertNotIn("diskspace", staged)
            self.assertIn('"shelley.exe.dev/server/deploy"', staged)
            self.assertIn('"shelley.exe.dev/server/resource"', staged)
            git("-c", "core.editor=true", "rebase", "--continue")


if __name__ == "__main__":
    unittest.main()
