"""Exercise copilot_resolve.py and sync_rebase.sh against a stand-in Copilot CLI.

The stand-in keeps upstream's side of each conflict, or misbehaves according
to STUB_MODE, so every check copilot_resolve.py makes on Copilot's output is
proven to reject what it should, without spending AI credits.
"""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent
STUB = r'''#!/usr/bin/env python3
import os, re, sys
args = sys.argv[1:]
assert "shell" in args and "url" in args and "--max-ai-credits" in args, args
files = [a[6:-1] for a in args if a.startswith("write(")]
mode = os.environ["STUB_MODE"]
open(args[args.index("--usage-output-file") + 1], "w").write('{"aiCredits": 2}')
if mode == "fail":
    sys.exit("quota exhausted")
for f in files if mode != "decline" else []:
    s = open(f).read()
    s = re.sub(r"^<{7}.*?\n(.*?)^(?:\|{7}.*?\n.*?)?={7}\n.*?^>{7}.*?\n", r"\1", s, flags=re.S | re.M)
    if mode == "token":
        s += "ghp_" + "A" * 36 + "\n"
    open(f, "w").write(s)
if mode == "extra":
    open("other.txt", "w").write("x\n")
print("kept upstream")
'''


class CopilotResolveTest(unittest.TestCase):
    def repo(self, temp: str, upstream: str, fork: str) -> Path:
        root = Path(temp) / "repo"
        root.mkdir()
        env = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
        self.env = {**os.environ, **env, "PATH": f"{temp}:{os.environ['PATH']}", "COPILOT_GITHUB_TOKEN": "test"}
        stub = Path(temp) / "copilot"
        stub.write_text(STUB)
        stub.chmod(0o755)

        def git(*args):
            subprocess.run(["git", *args], cwd=root, env=self.env, check=True, capture_output=True)

        git("init", "-q", "-b", "main")
        (root / "conf.txt").write_text("run: make exe-scroll\n")
        (root / "other.txt").write_text("untouched\n")
        git("add", ".")
        git("commit", "-qm", "base")
        git("checkout", "-qb", "fork")
        (root / "conf.txt").write_text(fork)
        git("commit", "-qam", "fork change")
        git("checkout", "-q", "main")
        (root / "conf.txt").write_text(upstream)
        git("commit", "-qam", "upstream change")
        git("checkout", "-q", "fork")
        return root

    def sync(self, mode: str, upstream: str, fork: str) -> tuple[subprocess.CompletedProcess, Path, Path]:
        temp = tempfile.mkdtemp()
        root = self.repo(temp, upstream, fork)
        out = Path(temp) / "out"
        result = subprocess.run(
            [str(SCRIPTS / "sync_rebase.sh"), "main", str(out)], cwd=root, text=True,
            capture_output=True, env={**self.env, "STUB_MODE": mode},
        )
        return result, root, out

    def test_accepts_a_clean_resolution_and_reports_it(self):
        result, root, out = self.sync("good", "run: make exe-scroll-all\n", "run: fetch --wait 300\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((root / "conf.txt").read_text(), "run: make exe-scroll-all\n")
        report = (out / "ai-resolution.md").read_text()
        self.assertIn("fork change", report)
        self.assertIn("kept upstream", report)
        self.assertIn('"aiCredits": 2', report)

    def test_rejects_bad_or_missing_resolutions(self):
        for mode, reason in (
            ("decline", "conflict markers remain"),
            ("token", "shaped like a GitHub token"),
            ("extra", "changed files it was not given"),
            ("fail", "Copilot CLI failed"),
        ):
            with self.subTest(mode):
                result, root, out = self.sync(mode, "run: make exe-scroll-all\n", "run: fetch --wait 300\n")
                self.assertEqual(result.returncode, 1)
                self.assertIn(reason, (out / "sync-report.md").read_text())
                # The rebase was aborted: the fork branch is untouched.
                self.assertEqual((root / "conf.txt").read_text(), "run: fetch --wait 300\n")
                self.assertFalse((out / "ai-resolution.md").exists())

    def test_drops_a_fork_commit_upstream_adopted(self):
        # Upstream made the same change with different wording around it; once
        # the conflict resolves to upstream's text the fork commit is empty.
        result, root, out = self.sync("good", "run: make exe-scroll-all\n", "run: make exe-scroll-all # fork\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        log = subprocess.check_output(["git", "log", "--format=%s"], cwd=root, text=True)
        self.assertNotIn("fork change", log)
        self.assertIn("fork change", (out / "dropped.txt").read_text())

    def test_without_a_token_the_conflict_is_left_for_a_person(self):
        temp = tempfile.mkdtemp()
        root = self.repo(temp, "run: a\n", "run: b\n")
        env = {k: v for k, v in self.env.items() if k != "COPILOT_GITHUB_TOKEN"}
        result = subprocess.run(
            [str(SCRIPTS / "sync_rebase.sh"), "main", f"{temp}/out"], cwd=root, text=True,
            capture_output=True, env={**env, "STUB_MODE": "good"},
        )
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("Copilot", Path(f"{temp}/out/sync-report.md").read_text())


if __name__ == "__main__":
    unittest.main()
