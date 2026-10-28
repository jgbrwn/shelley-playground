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
    def test_readme_keeps_fork_text_and_upstream_site_link(self):
        # The fork owns README.md: its text wins even where git aligned the
        # hunk against unrelated upstream prose (a shape that changed six
        # times), while the shelley.dev link the intro keeps survives.
        base = "# Shelley\n\n[shelley.dev](https://shelley.dev/)\n\n# Installation\n"
        hunk = conflict_fixture('''# Shelley Playground

[shelley.dev](https://shelley.dev/)

@@START@@ HEAD
You'll need Go. `make` installs the Node.js and pnpm pinned in
`ui/mise.toml` (see `scripts/with-node`).
@@SEPARATOR@@
### Replacing the Shelley that already runs on a fresh exe.dev VM

Most users meet Shelley for the first time on a fresh exe.dev VM.
@@END@@ fork

```bash
git clone https://github.com/jgbrwn/shelley-playground
```
''')
        resolved = resolve_text("README.md", hunk, base)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("### Replacing the Shelley that already runs", resolved)
        self.assertIn("git clone https://github.com/jgbrwn/shelley-playground", resolved)
        self.assertNotIn("ui/mise.toml", resolved)
        self.assertIn("[shelley.dev](https://shelley.dev/)", resolved)

    def test_readme_aborts_when_upstream_adds_a_section(self):
        base = "# Shelley\n\n[shelley.dev](https://shelley.dev/)\n\n# Installation\n"
        upstream = "[shelley.dev](https://shelley.dev/)\n\n# Installation\n\n# Brand New Section\n"
        for hunk in (
            f"<<<<<<< HEAD\n{upstream}=======\n# Forked\n>>>>>>> fork\n",
            f"{base}\n<<<<<<< HEAD\n{upstream}=======\n# Forked\n>>>>>>> fork\n",
        ):
            with self.subTest(hunk=hunk), self.assertRaises(ValueError):
                resolve_text("README.md", hunk, base)
        # A heading upstream already had in the pre-rebase file is not new.
        rewritten = (
            "# Shelley Playground\n\n[shelley.dev](https://shelley.dev/)\n"
            "<<<<<<< HEAD\n# Installation\n=======\n"
            "# Shelley Playground\n>>>>>>> fork\n"
        )
        resolved = resolve_text("README.md", rewritten, base)
        self.assertNotIn("<<<<<<<", resolved)

    def test_node_setup_adopts_upstream_mise_step(self):
        ours = (
            "      - name: Set up Node.js and pnpm\n"
            "        run: |\n"
            '          dirs="$(scripts/with-node)"\n'
            "          echo \"${dirs//:/$'\\n'}\" >> \"$GITHUB_PATH\"\n"
        )
        guarded = (
            "        - name: Set up pnpm\n"
            "          if: steps.version.outputs.skip != 'true'\n"
            "          uses: pnpm/action-setup@ea17c68df8912ef543352723c149a84f56e3d413 # v6.1.0\n"
            "          with:\n"
            "            package_json_file: ui/package.json\n"
            "        - name: Set up Node.js\n"
            "          if: steps.version.outputs.skip != 'true'\n"
            "          uses: actions/setup-node@v4\n"
            "          with:\n"
            "            node-version-file: ui/.nvmrc\n"
            "            cache: 'pnpm'\n"
            "            cache-dependency-path: ui/pnpm-lock.yaml\n"
        )
        # release.yml: keep the release-skip guard on upstream's step.
        resolved = resolve_text(
            ".github/workflows/release.yml",
            f"<<<<<<< HEAD\n{ours}=======\n{guarded}>>>>>>> fork\n",
        )
        self.assertIn("scripts/with-node", resolved)
        self.assertIn("if: steps.version.outputs.skip != 'true'", resolved)
        self.assertNotIn("ui/.nvmrc", resolved)
        self.assertNotIn("pnpm/action-setup", resolved)

        # test.yml/sync-upstream.yml: the same steps without a guard.
        unguarded = guarded.replace("          if: steps.version.outputs.skip != 'true'\n", "")
        for path in (".github/workflows/test.yml", ".github/workflows/sync-upstream.yml"):
            with self.subTest(path=path):
                out = resolve_text(
                    path, f"<<<<<<< HEAD\n{ours}=======\n{unguarded}>>>>>>> fork\n"
                )
                self.assertIn("scripts/with-node", out)
                self.assertNotIn("ui/.nvmrc", out)

        # Anything else in that position still stops the sync.
        for path in (".github/workflows/test.yml", ".github/workflows/release.yml"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                resolve_text(
                    path,
                    "<<<<<<< HEAD\n- name: Set up Node.js and pnpm\n"
                    "=======\n- name: Something else\n>>>>>>> fork\n",
                )

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
        # The fork owns README.md, but only while upstream adds no sections.
        with self.assertRaises(ValueError):
            resolve_text(
                "README.md",
                "<<<<<<< ours\n=======\nunknown\n>>>>>>> theirs\n",
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
