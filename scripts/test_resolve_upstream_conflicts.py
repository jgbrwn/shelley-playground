import contextlib
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from resolve_upstream_conflicts import NotAdditive, resolve_hunk, resolve_text


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

    def test_readme_warns_when_upstream_adds_a_section(self):
        base = "# Shelley\n\n[shelley.dev](https://shelley.dev/)\n\n# Installation\n"
        upstream = "[shelley.dev](https://shelley.dev/)\n\n# Installation\n\n# Brand New Section\n"
        fork = "# Shelley Playground\n\n[shelley.dev](https://shelley.dev/)\n"
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            resolved = resolve_text(
                "README.md", f"<<<<<<< HEAD\n{upstream}=======\n{fork}>>>>>>> fork\n", base
            )
        self.assertEqual(resolved, fork)
        self.assertIn("::warning file=README.md::", out.getvalue())
        self.assertIn("# Brand New Section", out.getvalue())
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

        # Anything else in that position still stops the sync.
        with self.assertRaises(ValueError):
            resolve_text(
                ".github/workflows/release.yml",
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

    def test_go_mod_keeps_upstream_versions_and_fork_edkey(self):
        # Today's real hunk: upstream bumped compress while the replayed
        # deploy commit carried the old pin plus its own edkey requirement.
        T = "\t"
        base = T + "github.com/klauspost/compress v1.20.0\n" + T + "github.com/modelcontextprotocol/go-sdk v1.8.0\n"
        ours = T + "github.com/klauspost/compress v1.20.3\n"
        theirs = T + "github.com/klauspost/compress v1.20.0\n" + T + "github.com/mikesmitty/edkey v0.0.0-20170222072505-3356ea4e686a\n"
        gomod = "<<<<<<< HEAD\n" + ours + "=======\n" + theirs + ">>>>>>> deploy\n"
        resolved = resolve_text("go.mod", gomod, base)
        self.assertNotIn("<<<<<<<", resolved)
        self.assertIn("github.com/klauspost/compress v1.20.3", resolved)
        self.assertNotIn("v1.20.0", resolved)
        self.assertIn("github.com/mikesmitty/edkey", resolved)

    def test_go_mod_rejects_unreviewed_or_non_addition_lines(self):
        T = "\t"
        base = T + "github.com/klauspost/compress v1.20.0\n"
        cases = [
            (
                T + "github.com/klauspost/compress v1.20.3\n",
                T + "github.com/klauspost/compress v1.20.0\n" + T + "github.com/other/thing v1.0.0\n",
                "a module absent from base but not edkey is unreviewed",
            ),
            (
                T + "github.com/klauspost/compress v1.20.3\n",
                T + "github.com/klauspost/compress v1.20.0\n" + "go 1.27.3\n",
                "non-require lines are unexpected",
            ),
            (
                T + "github.com/klauspost/compress v1.20.3\n",
                T + "github.com/klauspost/compress v1.20.2\n" + T + "github.com/mikesmitty/edkey v0.0.0-20170222072505-3356ea4e686a\n",
                "both sides changing the same pin is not an addition",
            ),
        ]
        for ours, theirs, note in cases:
            with self.subTest(note=note), self.assertRaises(ValueError):
                resolve_text("go.mod", f"<<<<<<< HEAD\n{ours}=======\n{theirs}\n>>>>>>> deploy\n", base)

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

    def test_reviewed_rules_refuse_unknown_hunks_and_paths(self):
        # resolve_text holds only the reviewed edit-vs-edit rules; every other
        # path is composed by resolve_hunk or refused before reaching it.
        for name, hunk in (
            ("ui/src/vue/App.vue", "<<<<<<< ours\na\n=======\nb\n>>>>>>> theirs\n"),
            ("README.md", "<<<<<<< ours\n=======\nunknown\n>>>>>>> theirs\n"),
            (".github/workflows/release.yml", "<<<<<<< ours\na: 1\n=======\na: 2\n>>>>>>> theirs\n"),
        ):
            with self.subTest(name=name), self.assertRaises(ValueError):
                resolve_text(name, hunk)


class ComposeHunkTest(unittest.TestCase):
    """resolve_hunk(upstream, base, fork) on whole diff3 hunks."""

    def test_both_inserted_at_one_anchor_keeps_upstream_then_fork(self):
        self.assertEqual(
            resolve_hunk(["secrets\n"], [], ["deploy\n"]), ["secrets\n", "deploy\n"]
        )

    def test_identical_insertions_are_kept_once(self):
        self.assertEqual(resolve_hunk(["x\n"], [], [" x\n"]), ["x\n"])

    def test_fork_insertion_survives_an_upstream_rewrite(self):
        # 2026-11: upstream folded two encode lines into writeJSON while the
        # fork inserted a redaction call before them.
        base = ["header()\n", "encode()\n"]
        self.assertEqual(
            resolve_hunk(["writeJSON()\n"], base, ["redact()\n", *base]),
            ["redact()\n", "writeJSON()\n"],
        )

    def test_fork_edit_survives_upstream_insertions(self):
        self.assertEqual(
            resolve_hunk(["new()\n", "old()\n", "tail()\n"], ["old()\n"], ["custom()\n"]),
            ["new()\n", "custom()\n", "tail()\n"],
        )

    def test_insertions_around_the_base_from_both_sides(self):
        self.assertEqual(
            resolve_hunk(["u\n", "b\n"], ["b\n"], ["f\n", "b\n", "g\n"]),
            ["u\n", "f\n", "b\n", "g\n"],
        )

    def test_refuses_when_both_sides_edit_the_base(self):
        with self.assertRaises(NotAdditive):
            resolve_hunk(["a = 1\n"], ["a = 0\n"], ["a = 2\n"])

    def test_refuses_an_ambiguous_base(self):
        # The fork's side contains the base twice, so it is unknowable which
        # copy is the original and which was added.
        with self.assertRaises(NotAdditive):
            resolve_hunk(["x\n"], ["}\n"], ["}\n", "}\n"])


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

    def test_go_mod_rebase_keeps_edkey_and_upstream_pin(self):
        # main() must read Git stage 1 for go.mod, exactly as for server.go.
        # Without it every fork line looks like an unreviewed addition and
        # the sync aborts on the shape it was taught to handle.
        resolver = Path(__file__).with_name("resolve_upstream_conflicts.py").resolve()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)

            def git(*args, check=True):
                return subprocess.run(
                    ["git", *args], cwd=root, text=True,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check,
                )

            git("init", "-q", "-b", "base")
            git("config", "user.name", "Resolver test")
            git("config", "user.email", "resolver@example.test")
            (root / "scripts").mkdir()
            (root / "scripts" / "resolve_upstream_conflicts.py").write_text(resolver.read_text())
            go = root / "go.mod"
            go.write_text(
                "module example.test/m\n\n"
                "require (\n"
                "\tgithub.com/klauspost/compress v1.20.0\n"
                ")\n"
            )
            git("add", ".")
            git("commit", "-m", "base")
            git("checkout", "-b", "fork")
            go.write_text(
                "module example.test/m\n\n"
                "require (\n"
                "\tgithub.com/klauspost/compress v1.20.0\n"
                "\tgithub.com/mikesmitty/edkey v0.0.0-20170222072505-3356ea4e686a\n"
                ")\n"
            )
            git("commit", "-am", "add edkey")
            git("checkout", "-b", "upstream", "base")
            go.write_text(
                "module example.test/m\n\n"
                "require (\n"
                "\tgithub.com/klauspost/compress v1.20.3\n"
                ")\n"
            )
            git("commit", "-am", "bump compress")
            git("checkout", "fork")
            result = git("rebase", "upstream", check=False)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            subprocess.run([sys.executable, str(resolver)], cwd=root, check=True)
            self.assertEqual(git("diff", "--name-only", "--diff-filter=U").stdout, "")
            staged = git("show", ":go.mod").stdout
            self.assertIn("compress v1.20.3", staged)
            self.assertNotIn("v1.20.0", staged)
            self.assertIn("github.com/mikesmitty/edkey", staged)
            git("-c", "core.editor=true", "rebase", "--continue")

    def test_server_import_rebase(self):
        # Upstream deleted an import the fork's commit still carries as
        # context. Deleting is an edit of the base, while the fork only
        # inserted around it, so the deletion wins and is never undone.
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


    def test_unknown_path_composes_or_refuses(self):
        resolver = Path(__file__).with_name("resolve_upstream_conflicts.py").resolve()
        base = "package p\n\nfunc get() {\n\theader()\n\tencode()\n}\n"
        cases = {
            "compose": (
                "package p\n\nfunc get() {\n\twriteJSON()\n}\n",
                "package p\n\nfunc get() {\n\tredact()\n\theader()\n\tencode()\n}\n",
                "package p\n\nfunc get() {\n\tredact()\n\twriteJSON()\n}\n",
            ),
            "refuse": (
                "package p\n\nfunc get() {\n\twriteJSON()\n}\n",
                "package p\n\nfunc get() {\n\theader()\n\tencodeIndented()\n}\n",
                None,
            ),
        }
        for label, (upstream, fork, want) in cases.items():
            with self.subTest(label), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)

                def git(*args):
                    return subprocess.run(
                        ["git", *args], cwd=root, text=True, capture_output=True
                    )

                git("init", "-q", "-b", "main")
                git("config", "user.name", "t")
                git("config", "user.email", "t@example.test")
                path = root / "pkg" / "unreviewed.go"
                path.parent.mkdir()
                path.write_text(base)
                git("add", ".")
                git("commit", "-qm", "base")
                git("checkout", "-qb", "fork")
                path.write_text(fork)
                git("commit", "-qam", "fork")
                git("checkout", "-q", "main")
                path.write_text(upstream)
                git("commit", "-qam", "upstream")
                git("checkout", "-q", "fork")
                self.assertNotEqual(git("rebase", "main").returncode, 0)
                result = subprocess.run(
                    [sys.executable, str(resolver)], cwd=root, text=True, capture_output=True
                )
                if want is None:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("both sides edited the same lines", result.stderr)
                    self.assertIn("<<<<<<<", path.read_text())
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(path.read_text(), want)
                    self.assertEqual(git("diff", "--name-only", "--diff-filter=U").stdout, "")


if __name__ == "__main__":
    unittest.main()
