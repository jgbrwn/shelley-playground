# Maintaining the Shelley Playground fork

Operational notes earned from the daily upstream sync. Read this before
changing anything that lives near upstream code, and before debugging a failed
`Sync upstream & release` run.

Everything here exists because a nightly sync broke on it at least once.

## Where to put documentation

Every Markdown file at the repository root (`README.md`, `AGENTS.md`,
`ARCHITECTURE.md`, `API.md`, `HOOKS.md`, `AGENT_TESTING.md`,
`FILE_COMPLETION.md`) is **upstream-owned**. Editing them adds a conflict site
that then has to be resolved by hand every single night.

Put fork documentation in `docs/`, which upstream does not have. The `README.md`
is the sole exception: the fork deliberately rewrites it, and
`resolve_readme_hunk` knows how to merge it (see below).

The same rule applies to code: prefer *adding* a file (`server/deploy/*`, a new
migration that only creates new tables) over editing an upstream one. Every
upstream file the fork edits is a permanent rebase obligation.

## The nightly sync and its resolver

`.github/workflows/sync-upstream.yml` runs daily at 03:00 UTC. It rebases the
fork's commits onto `boldsoftware/shelley` `main`, driving conflicts through
`scripts/resolve_upstream_conflicts.py`.

The resolver is **fail-closed by design**. When it meets a conflict shape it
has not been taught, it prints the combined diff (`git diff --cc`) and runs
`git rebase --abort`. Nothing is pushed, nothing is corrupted. That is the
intended outcome: an unattended bot should stop rather than guess at a merge.

So a red sync run usually means "teach the resolver one more shape", not
"something is broken". The logged `git diff --cc` is normally enough to write
the new rule and its fixture without reproducing the run locally.

### Adding a rule

1. Copy the exact conflicting hunk from the workflow log.
2. Add the narrowest rule that accepts it, anchored on something structural
   (a distinctive first line, a marker the fork itself owns) rather than on a
   full-text dump of the region.
3. Add a fixture built from the real hunk, plus a negative fixture proving
   that a nearby-but-different hunk still aborts.
4. Run `PYTHONDONTWRITEBYTECODE=1 python3 scripts/test_resolve_upstream_conflicts.py`.
5. Rebase for real and confirm the resolved file is correct, not merely
   marker-free.

Anchor on the fork's own invariant wherever possible. Rules anchored on
upstream's exact prose rot silently: upstream reworded a paragraph it deletes,
which turned a twelve-line conflict into a sixty-four-line one with an empty
"ours" side, and the old rule stopped matching.

The same region produces a different hunk depending on where upstream's edit
lands. The fork deletes upstream's whole Architecture/History tail, so an
upstream edit inside it can surface either as "upstream ends here, the fork's
tail begins" (empty "ours") or as "upstream's whole tail" against a single
fork heading. Rather than enumerate every alignment, the rule checks whether
"ours" contains any marker of that deleted tail and "theirs" contains none.
Prefer recognising the *region* over recognising the exact lines.

### Rules that must keep aborting

Do not widen these away. A conflict where both sides change the same thing for
different reasons is a judgement call about intent, and the sync should stop
for a human:

- a second, different assignment to a struct field the deploy feature owns
  (`s.deployManager`)
- a README hunk that is not recognisably one of the fork's two replaced
  regions
- any file outside the allowlist, and any unknown path

The fixtures encode these; they are not merely tests of the happy path.

### Policies the resolver encodes

Upstream and the fork disagree on purpose in three places. Each is expressed as
a rule so that it stops re-breaking the sync every night:

- **Deleted files** (`DELETED_PATHS`):
  `.github/workflows/publish-version-metadata.yml` is deleted in this fork
  (the repo has no GitHub Pages). Upstream keeps editing it, so every sync
  raises a modify/delete conflict. The deletion is the resolution.
  *If upstream ever deletes it too, remove the `DELETED_PATHS` entry.*
- **`release.yml`**: upstream publishes Chromium `headless-shell` as its own
  release; the fork downloads it from upstream at build time instead. The
  fork's explanatory comment wins, upstream's steps are dropped.
- **Dependency graph**: after `go.sum` conflicts the resolver runs
  `go mod tidy` itself. `.goreleaser.yml` runs `go mod tidy` as a before-hook
  and fails if it rewrites the tree, so a hand-merged `go.sum` that is not
  tidy-clean would break the release *after* a successful rebase.

## Verifying a rebase before pushing

Resolve moving marker-free is not the same as resolved correctly. Check the
places the fork actually wires into upstream:

```bash
grep -c 'onOpenDeployModal' ui/src/vue/components/ChatInterface.vue   # expect 2
grep -c 'on-open-deploy-modal' ui/src/vue/App.vue                     # expect 1
grep -c 's.deployManager = deploy.NewManager' server/server.go        # expect 1
```

Automatic merges are the dangerous case: a conflict forces you to look, while
a clean merge of `App.vue` or `ChatInterface.vue` can silently drop or
duplicate the deploy wiring. Rebase in a scratch worktree first when upstream
has touched those files, then diff the result against the fork before applying
it:

```bash
git worktree add --detach /tmp/rehearse custom
git -C /tmp/rehearse rebase origin/main
```

### Audit the rule anchors

A rule whose anchor upstream has since reworded is dormant, and it will fail at
03:00 rather than now. After rebasing (or before relying on the sync), confirm
each anchor still exists upstream:

```bash
git show origin/main:README.md            | grep -c '^\[shelley.dev\](https://shelley.dev/)$'
git show origin/main:README.md            | grep -c '^The technical stack is Go for the backend'
git show origin/main:.github/workflows/release.yml | grep -c 'id: headless'
git show origin/main:server/server.go     | grep -c 's.listIntegrations'
git show origin/main:.github/workflows/publish-version-metadata.yml >/dev/null && echo present
```

## Build and test order

The customized binary embeds `ui/dist/build-info.json`, and
`ui/embedfs.go` refuses to start when `ui/src` is newer than that snapshot
("UI build is stale"). A rebase rewrites `ui/src` mtimes, and `ui/dist` alone
cannot clear the check — the timestamp lives in the binary.

Therefore: **`make build-custom`, then `go test ./...`**. Running the tests
first fails in `client` and `test` for reasons that have nothing to do with the
change being tested.

```bash
make build-custom
go test ./... && go vet ./...
```

For UI changes, the full gate is:

```bash
cd ui && pnpm run type-check && pnpm run type-check:vue && pnpm run lint && pnpm run build
```

Playwright E2E steps in `.github/workflows/test.yml` are disabled (`if: false`),
so a green CI run does not mean the browser tests ran.

## Cutting a release

A push to `main` triggers `Test`, whose success triggers `release.yml`. A manual
push therefore publishes its own release, and consecutive pushes produce
several releases in a day; `release.yml` publishes only the current tip of
`main`, so older commits do not fight for the `Latest` tag.

```bash
git fetch origin main --tags
test "$(git merge-base custom origin/main)" = "$(git rev-parse origin/main)"
git push --force-with-lease playground custom:main
```

`--force-with-lease` matters: the rebase rewrites history, so a plain `--force`
could discard a sync that landed in the meantime.

## Preview

```bash
tmux new-session -d -s shelley-preview "$PWD/bin/shelley -db /tmp/shelley-custom-preview.db serve -port 8010"
curl -fsS http://localhost:8010/version-check
```

Use `make build-custom`, never plain `make build`: an unstamped binary offers
the mainline self-update path, which would silently discard the fork.
