import unittest

from resolve_upstream_conflicts import resolve_text


def conflict_fixture(text):
    return (
        text.replace("@@START@@", "<" * 7)
        .replace("@@SEPARATOR@@", "=" * 7)
        .replace("@@END@@", ">" * 7)
    )


class ConflictResolverTest(unittest.TestCase):
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
    "server/deploy"
@@SEPARATOR@@
    "skills"
@@END@@ upstream
)
func initServer(s *Server) {
@@START@@ HEAD
    s.integrationSkills = discoverIntegrationSkillsAtStartup(logger)
    s.deployManager = deploy.NewManager(nil) // runs persisted on completion via persistRun
@@SEPARATOR@@
    s.integrationSkills = newIntegrationSkillCache(logger, currentIntegrationSkillDiscoverer(logger))
@@END@@ upstream
}
''')
        resolved = resolve_text("server/server.go", server)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn('"server/deploy"', resolved)
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
                "ui/src/vue/components/ChatOverflowMenu.vue",
                "<<<<<<< ours\nunknown\n=======\nunknown\n>>>>>>> theirs\n",
            )
        with self.assertRaises(ValueError):
            resolve_text(
                "ui/src/vue/components/ChatInterface.vue",
                "<<<<<<< ours\n<UnknownSlot />\n=======\n<UnknownProp />\n>>>>>>> theirs\n",
            )


if __name__ == "__main__":
    unittest.main()
