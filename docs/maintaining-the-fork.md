# Maintaining the Shelley Playground fork

This fork keeps the deploy-to-exe.dev feature while rebasing nightly onto
`boldsoftware/shelley` main. The goal is that the sync needs **no human or AI
intervention** for ordinary upstream churn, and that whatever does need one
can never reach `main` and is never silent.

## How the nightly sync works

`.github/workflows/sync-upstream.yml`, daily at 03:00 UTC and on demand:

1. **Rebase** the fork's commits onto upstream main (`scripts/sync_rebase.sh`).
   Each conflict goes to `scripts/resolve_upstream_conflicts.py`, which
   composes it or refuses (see [the resolver](#the-resolver-compose-or-refuse)).
   What it refuses goes to [Copilot](#copilot-for-edit-vs-edit-conflicts)
   when that is set up. A fork commit left with no changes, because upstream
   adopted it, is dropped.
2. **Check** that no fork-only file still references a path upstream deleted
   (`scripts/check_deleted_paths.py`; see below).
3. **Push the candidate** to the `sync-candidate` branch, and run the full
   `Test` workflow on it: the Go and UI test suites, all four release builds,
   TypeScript and Vue type-checks, lint, `go vet`, and the resolver tests.
4. **Promote** the candidate to `main` only if Test passed, with a lease on
   the `main` the sync started from. The push runs Test and Release as usual.

If upstream has not moved, the sync only releases when the current commit's
version tag is missing.

Any failed stage opens an issue labelled **`sync-failure`** (or comments on
the open one) with the stage, the run link and, for a refused conflict, the
resolver's reason and the `git diff --cc`. The next successful sync closes it.
A failed candidate stays on `sync-candidate` for inspection, and `main` keeps
the last good release. A failure is retried by the next night's run, which
also picks up any fix pushed in between.

## Triage the actual failure first

Start from the `sync-failure` issue: it names the stage that failed.

- **Rebasing** — the resolver refused an edit-vs-edit conflict, and Copilot
  was not set up, declined, or produced a result that failed a check (the
  issue says which). Read the reason and the combined diff in the issue. Decide what the result should be,
  then either rebase by hand or, if this file will keep conflicting the same
  way, add a reviewed rule (see below).
- **Testing the candidate** — the rebase succeeded but the tree is broken:
  upstream removed or changed something fork code uses (a *semantic* break; on
  2026-11-04 upstream deleted `db.Tx.Exec`, which deploy code called), or a
  test is flaky. Check out `sync-candidate` and read the failing step's log.
  For a removed symbol, `git log -S'symbol' upstream/main` finds the upstream
  commit; fix the fork's call site.
- **Promoting** — `main` moved while the candidate was tested, which is
  expected after a manual push. The next run starts from the new `main`.

To reproduce, fetch both remotes and use the failed run's **exact** `main` and
upstream SHAs in a scratch worktree; local `custom` may be older than
`playground/main`. During a rebase **ours/HEAD is upstream** and **theirs is
the fork commit being replayed**; stage 1 is that commit's original base.

```bash
git fetch origin main --tags
git fetch playground main
gh run view RUN_ID --repo jgbrwn/shelley-playground --log-failed
```

The sync pushes with `PLAYGROUND_SYNC_TOKEN`, which must be allowed to update
workflow files, because upstream commits can change `.github/workflows`.

## Keep fork changes composable

Upstream changes often, so assume every upstream line near a fork change will
move. What decides whether the sync handles it alone is the *kind* of change
the fork makes:

- **Adding lines** (a new prop, menu item, route, API method, import) always
  composes with whatever upstream does around them, including when upstream
  adds its own lines at the same spot. Prefer additions.
- **Editing an upstream line** conflicts the moment upstream edits it too,
  and needs a human. When the fork must change upstream behavior, prefer a
  small inserted call (a hook) over rewriting upstream lines. The deploy
  feature's settings redaction is the model: one inserted
  `redactPrivateSettings(settings)` line survived upstream rewriting the
  response encoding around it.
- Put substantial fork code in fork-owned files (`server/deploy/`, `docs/`,
  `ui/src/vue/components/Deploy*.vue`), so upstream lines near it are few.
- Do not modify existing shared migrations; add new migrations and tables.

The fork owns **README.md**: its version wins. If upstream adds a README
section, the sync keeps going and logs an Actions warning naming the section,
to port at leisure. Operational detail belongs here, not in README.

## The resolver: compose or refuse

`scripts/resolve_upstream_conflicts.py` re-merges each conflicted file with
`git merge-file --diff3`, which shows, per hunk, the **base** lines and each
side's whole change. Git's default conflict view hides the base and pulls
shared lines out of the hunk, which is why the old resolver needed a
hand-written rule per file and per shape.

With the base visible, each side is either *inserting around* the base (base
kept intact) or *editing* it:

| Upstream | Fork | Result |
|---|---|---|
| inserted | inserted | upstream's lines, then the fork's |
| edited | inserted | fork's insertions around upstream's edit |
| inserted | edited | upstream's insertions around the fork's edit |
| edited | edited | **refuse** |

This is ordinary three-way merging at a finer grain; git only reported a
conflict because the changes were adjacent. A side that contains the base more
than once is ambiguous and also refused. Composed `.go` files go through
`gofmt`, which sorts imports and refuses a result that does not parse. An
upstream *deletion* is an edit of the base, so the resolver never resurrects
code upstream removed while the fork's commit still had it as context.

What this cannot see is **meaning**: two compositions that are each fine can
together be wrong. That is why the candidate must pass the whole Test workflow
before it becomes `main`. Composition decides what is *mechanically* safe;
the tests decide whether it is *correct*.

Reviewed rules remain only where the generic merge would be wrong or has
genuinely met edit-vs-edit conflicts:

- **README.md, go.mod, go.sum** are ownership rules, applied instead of
  composition. README: the fork's text wins (with the warning above), and the
  result must keep the fork's title and the `shelley.dev` link. Dependencies:
  upstream owns every version. In `go.mod` a fork line repeating the stage-1
  pin is a stale echo and upstream's line wins; `github.com/mikesmitty/edkey`
  is the only fork requirement; a fork line that *changed* a version aborts.
  `go.sum` takes the union after rejecting conflicting hashes, then
  `go mod tidy` runs, because GoReleaser's tidy hook fails on a dirty tree.
- **`release.yml`** — adopt upstream's `scripts/with-node` setup step with the
  fork's release-skip guard; preserve upstream action upgrades next to the
  skip guard; keep omitting upstream's headless-shell release job.
- **Deleted files** — `.github/workflows/publish-version-metadata.yml` is
  deliberately removed; only the expected modify/delete stages are resolved
  by deleting it.

Every other conflict composes or refuses. To add a rule, start from the real
hunk; add the positive fixture and nearby negative ones; and if the rule reads
git stages, add a git-level test that drives `main()`, because string
fixtures cannot catch a missing stage read. Then replay history:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/test_resolve_upstream_conflicts.py
git fetch playground --tags && git fetch origin main
scripts/replay_sync_history.py origin/main
```

`replay_sync_history.py` re-runs every past sync between consecutive releases
with the working resolver and compares each automatic result with what
actually shipped. On 2026-11-21 it replayed 67 syncs: 50 clean, 15 composed,
2 refused. Both refusals were genuine edit-vs-edit conflicts (a `test.yml`
line both sides rewrote, and a README from before the fork's README carried
the `shelley.dev` link). Three automatic results differed from what shipped,
and all were benign: import order (since fixed with `gofmt`); a documentation
line in `CommandPalette.vue` that the manual resolution had dropped; and an
`edkey` line in `go.mod` that the shipped release had wrongly marked indirect.
A resolver change should only turn refusals into results that match what
shipped.

### History: why per-shape rules did not converge

From September to November the resolver accepted a growing list of reviewed
hunk *shapes*: about a dozen rules across `App.vue`, `ChatInterface.vue`,
`ChatOverflowMenu.vue`, `CommandPalette.vue`, `server.go`, README and the
workflows. Each was correct and each was blind to the next upstream change:
README alone failed six times in October, once per alignment. Then, from
2026-11-07 to 11-21, the sync failed fifteen nights in a row because upstream
appended APIs to the end of `ui/src/services/api.ts` exactly where the deploy
commit appends its own, a file with no rule. Every one of those conflicts was
two insertions at one anchor, or an insertion next to an edit. The replay
showed composition covers all of the UI and server rules with identical
results, so they were deleted.

### A clean merge is not a correct merge

Git reports conflicts only where both sides touched the same lines. Two
failure classes merge **cleanly** and still break the tree:

1. **Upstream deletes a file a fork-only file references.** On 2026-10-28
   upstream deleted `ui/.nvmrc`; the fork-only `sync-upstream.yml` merged
   cleanly while still reading it. `scripts/check_deleted_paths.py` diffs the
   pre-rebase revision against HEAD and fails if a *functional* file
   (workflows, `Makefile`, `scripts/*`, `.goreleaser.yml`, `ui/package.json`,
   `go.mod`) still refers to a deleted path. Deliberate deletions come from the
   resolver's `DELETED_PATHS`. Prose mentions and output paths are ignored, so
   the check stays credible. Fix the reference; do not silence the check.
2. **Upstream removes a symbol fork code uses.** On 2026-11-04 to 06 upstream
   deleted `db.Tx.Exec`; three syncs rebased cleanly over a tree that no
   longer compiled, and the broken `main` was only caught by Test afterwards.
   The candidate gate now catches this before `main` moves.

## Copilot for edit-vs-edit conflicts

When both sides edited the same lines, a judgement is needed. If the
`COPILOT_GITHUB_TOKEN` secret is set, the sync asks Copilot CLI to make it
(`scripts/copilot_resolve.py`), at most five times per sync. This works on the
**Copilot Free** plan: Free includes Copilot CLI, while the cloud agent (assign
an issue to Copilot and it opens a pull request) needs a paid plan.

There is no pull request to approve. A rebase rewrites history, so a PR merge
would not apply, and approval is the same gate every sync passes: the result
must pass the full Test workflow on `sync-candidate` before it reaches `main`.
Each Copilot decision is then posted as a **`sync-ai-resolved`** issue showing
the resolved files compared with upstream's version, and Copilot's own summary,
for review after the fact. Close the issue once read. To undo a decision, fix
it on `main`; the next sync keeps whatever `main` has.

Copilot is treated as untrusted output in a public repository:

- It may read the repository and write **only the conflicted files**. Shell,
  URLs, memory and the built-in MCP servers are denied, and each session is
  capped at 50 AI credits (the CLI's minimum is 30), and actual usage is recorded in each report.
- The checkout persists no credentials, and only the push step sees the push
  token. The Copilot token needs no repository access at all.
- Its output is checked independently: only the conflicted files changed, no
  file was created, no markers remain, nothing token-shaped was written, Go
  passes `gofmt`, YAML and JSON parse. Any failure leaves the conflict for a
  person, as does Copilot leaving markers when it is unsure, which the prompt
  asks it to do rather than guess.
- `scripts/test_copilot_resolve.py` proves each check rejects what it should,
  using a stand-in for the CLI, so it costs no credits.

**Setup.** Create a fine-grained personal access token on your personal
account (Settings → Developer settings → Fine-grained tokens) with **no
repository access** and the account permission **Copilot Requests**. Add it to
this repository as the Actions secret `COPILOT_GITHUB_TOKEN`. Delete the secret
to switch Copilot off; the sync then refuses edit-vs-edit conflicts as before.

**Rehearse before relying on it.** The **Copilot rehearsal** workflow
(Actions → Copilot rehearsal → Run workflow) replays a historical sync with a
known-correct answer, without pushing anything, and its run summary compares
each of Copilot's resolutions with what actually shipped. The default replays
the 2026-10-03 sync, where both sides rewrote a `test.yml` line and upstream
had adopted two fork commits. Copilot Free uses automatic model selection and
an unpublished monthly credit allowance, so check its quality and its usage
(github.com/settings/copilot) before trusting it.

## Verify before pushing

Save a backup ref before rewriting history. Rehearse in a uniquely named
scratch worktree; do not reuse an old worktree without checking its state.

```bash
git branch backup/custom-before-DATE custom
git worktree add --detach /tmp/shelley-rehearse-DATE FAILED_HEAD_SHA
# Run rebase and reviewed conflict resolutions there, then inspect the result.
```

Rebase `custom` onto current `origin/main` with the resolver, exactly as the
sync does, preserving deploy functionality and upstream features. Textual
presence checks can help, but do not verify types, runtime behavior, or
semantic correctness; the full checks below do.

### Toolchain and build order

Node and pnpm are pinned in `ui/mise.toml`; run them through
`scripts/with-node`, as the workflows do, never whatever is on `PATH`.

Build embedded assets before Go tests. Some integration tests execute
`bin/shelley`, so rebuild the binary too after UI changes or rebases: an old
binary still embeds old UI build metadata, even if `ui/dist` is fresh.

```bash
make build-custom
go test ./... -count=1
go vet ./...
go mod tidy
git diff --exit-code -- go.mod go.sum
git diff --check
```

For UI changes (including rebased upstream changes), run in `ui/`:

```bash
pnpm install --frozen-lockfile
pnpm run type-check
pnpm run type-check:vue
pnpm run lint
pnpm test
pnpm run build
```

Use `set -euo pipefail` in multi-command validation scripts. Do not pipe a test
through `tail`/`grep` and report the filter's exit status as the test's status.
Log to a file if needed, retaining the original command's failure status.
Playwright steps in `test.yml` are currently disabled (`if: false`); green CI
is not evidence that browser E2E tests ran.

## Terminal-test incident: evidence, not a proven diagnosis

On October 23, 2026, fork Test run `39979457277` failed on
`TestDirectPTYCloseInterruptsRecv/detached`: the helper timed out waiting to
observe a Recv goroutine in a particular state. Local runs passed, as did a
later fork run (`39980741533`). The intervening fork changes were documentation
and resolver scripts, not Go code.

Upstream run `39970231175` at `b138f633` also failed in this test, **but at a
different assertion**: `holding_the_terminal` returned `Recv after Close =
<nil>, want os.ErrClosed`. It does not establish a common cause.

The previous investigation artificially created thousands of goroutines and
produced a dump larger than the helper's fixed 1 MiB buffer. This demonstrates
a possible truncation risk, **not a measured CI dump or a proven explanation**
for either failure. No actual failing-run dump established truncation. Increasing
that buffer alone has not been demonstrated to fix these failures.

If this recurs, capture diagnostics at the failing assertion (complete stacks,
truncation detection, Recv result, and relevant command state), then reproduce
and fix the race or test synchronization. An upstream-owned test can contain a
real bug; local passes do not exonerate it. Rerunning once can help classify
intermittency, but repeated reruns, skipping assertions, blanket automatic
retries, or tolerating known failures are not a durable fix. Keep release gated
on successful tests; don't trade correctness for a green badge.

## Publish and preview

```bash
git fetch playground main
# Check that remote main has not advanced beyond the head you investigated.
test "$(git merge-base custom origin/main)" = "$(git rev-parse origin/main)"
git push --force-with-lease playground custom:main
```

The lease prevents clobbering a concurrent remote update. Check the actual
Test and Release conclusions for the pushed SHA, then verify the release tag.
Do not confuse an older failed attempt or queued Release with a new failure.

Build with `make build-custom`, never plain `make build` for the preview:
the customized stamp prevents mainline binary self-updates discarding the fork.
Run the preview on port 8010 with `/tmp/shelley-custom-preview.db`, separate
from the live instance. Verify `/version-check` reports the intended commit
and `customized: true`. Never replace/restart the live instance without consent.

On this VM the persistent preview is managed by `shelley-preview.service`.
After a successful build, restart **that unit only**, not `shelley.service`:

```bash
sudo systemctl restart shelley-preview.service
curl -fsS http://localhost:8010/version-check
```
