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


if __name__ == "__main__":
    unittest.main()
