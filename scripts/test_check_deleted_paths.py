import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECKER = Path(__file__).with_name("check_deleted_paths.py").resolve()


class DeletedPathCheckerTest(unittest.TestCase):
    """The checker must catch the 2026-10-28 breakage and stay quiet otherwise."""

    def run_check(self, files, deleted, extra=()):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)

            def git(*args):
                subprocess.run(
                    ["git", *args], cwd=root, check=True, capture_output=True, text=True
                )

            git("init", "-q", ".")
            for name, text in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text)
            git("add", ".")
            git("-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "base")
            for name in deleted:
                git("rm", "-q", name)
            for name, text in extra:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text)
                git("add", name)
            if deleted or extra:
                git("-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "change")
            # The checker imports DELETED_PATHS from its sibling resolver.
            (root / "scripts").mkdir(parents=True, exist_ok=True)
            for script in (CHECKER, CHECKER.with_name("resolve_upstream_conflicts.py")):
                (root / "scripts" / script.name).write_text(script.read_text())
            result = subprocess.run(
                [sys.executable, str(root / "scripts" / CHECKER.name), "--base", "HEAD~1"],
                cwd=root, capture_output=True, text=True,
            )
            return result.returncode, result.stdout + result.stderr

    def test_flags_workflow_reading_a_deleted_file(self):
        rc, out = self.run_check(
            {"ui/.nvmrc": "24\n", ".github/workflows/test.yml": "name: t\n"},
            deleted=["ui/.nvmrc"],
            extra=[(
                ".github/workflows/sync.yml",
                "steps:\n  - uses: actions/setup-node@v4\n    with:\n      node-version-file: ui/.nvmrc\n",
            )],
        )
        self.assertEqual(rc, 1, out)
        self.assertIn("sync.yml:4: references deleted ui/.nvmrc", out)

    def test_quiet_when_nothing_references_the_deletion(self):
        rc, out = self.run_check(
            {
                "ui/.nvmrc": "24\n",
                ".github/workflows/test.yml": "name: t\n",
                ".github/workflows/sync.yml": "uses: scripts/with-node\n",
            },
            deleted=["ui/.nvmrc"],
        )
        self.assertEqual(rc, 0, out)

    def test_prose_and_output_paths_do_not_trigger(self):
        # Mentions in documentation and build outputs are not broken references.
        rc, out = self.run_check(
            {"ui/.nvmrc": "24\n", "docs/notes.md": "we used ui/.nvmrc once\n"},
            deleted=["ui/.nvmrc"],
            extra=[(".github/workflows/test.yml", "go build -o bin/shelley-linux\n")],
        )
        self.assertEqual(rc, 0, out)

    def test_intentional_deletions_are_ignored(self):
        name = ".github/workflows/publish-version-metadata.yml"
        rc, out = self.run_check(
            {name: "name: p\n", "Makefile": "all:\n"}, deleted=[name]
        )
        self.assertEqual(rc, 0, out)
        self.assertIn("No non-intentional deletions", out)

    def test_partial_path_match_does_not_trigger(self):
        rc, out = self.run_check(
            {"ui/.nvmrc": "24\n", "Makefile": "all:\n\t@echo ui/.nvmrc.bak\n"},
            deleted=["ui/.nvmrc"],
        )
        self.assertEqual(rc, 0, out)


if __name__ == "__main__":
    unittest.main()
