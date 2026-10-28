# Maintaining the Shelley Playground fork

This fork keeps the deploy-to-exe.dev feature while rebasing onto
`boldsoftware/shelley` main. Treat upstream changes as new code to review, not
as obstacles to silence. Unknown conflicts must stop unattended automation.

## Triage the actual failure first

1. Fetch both remotes and identify the failed run's **exact head and upstream
   target SHA**. Local `custom` may be older than `playground/main`; the nightly
   bot can have pushed since your last session.
2. Read the failing step and assertion, not just the workflow's red status:

   ```bash
   git fetch origin main --tags
   git fetch playground main
   gh run list --repo jgbrwn/shelley-playground --limit 10
   gh run view RUN_ID --repo jgbrwn/shelley-playground --log-failed
   ```

3. Distinguish a rejected rebase from a build/toolchain error, failing test,
   or release/token problem. Each needs a different fix. A red sync is **not**
   automatically a reason to broaden the resolver.
4. Reproduce a rebase using the failed run's head in a scratch worktree and
   its upstream target SHA. Then test against the current upstream tip too.
   During rebase, **ours/HEAD is upstream**, and **theirs is the fork commit
   being replayed**. Stage 1 is that commit's original base.

The sync is scheduled for 03:00 UTC; GitHub may start it later. It pushes with
`PLAYGROUND_SYNC_TOKEN`, which must have permission to update workflow files.
A successful rebased push triggers `Test`, then `Release`. A failed rebase
aborts before pushing. If upstream is unchanged, sync only releases when the
current commit's generated tag is absent.

## Keep custom changes small, not forbidden

Prefer fork documentation in `docs/` and cohesive deploy code in
`server/deploy/`. Upstream can add paths there too; these are not reserved
namespaces. Editing upstream files is sometimes necessary and is allowed.
Review and test those edits rather than avoiding a real fix to save a merge.
Do not modify existing shared migrations; use new migrations and new tables.

The fork owns **README.md** and replaces most of it. Do not put operational
detail back into README; link to this guide.

Between October 12 and October 28 that ownership was modelled as a list of
acceptable hunk *shapes*, and the nightly sync failed six times, each time
because upstream's next prose edit made git align the same replacement
differently:

| Date | Upstream change | New shape |
|---|---|---|
| Oct 12 | added a site link to the intro | intro replacement |
| Oct 16 | added an idle-compaction paragraph | Architecture tail vs build block |
| Oct 21 | reworded and deleted trailing blank | empty-ours tail replacement |
| Oct 23 | added a heading inside the deleted tail | upstream tail vs one fork heading |
| Oct 28 | moved build instructions to mise | build block vs replacement subsection |

Enumerating alignments does not converge: every shape was correct, and each
one was blind to the next. The rule now models the actual invariant — the fork
owns the file — and guards the one thing ownership must not swallow: if
upstream introduces a **heading its pre-rebase README lacked**, it has added a
section the fork has never seen, and the sync aborts for a human.

That inversion is the general lesson. When the same file keeps re-conflicting,
stop adding rules for each alignment and ask which side owns the file and what
must provably survive. A per-shape rule set looks safe because each rule is
narrow, but it fails *at 03:00*, once per upstream prose edit, forever.

## Resolver safety and tests

`scripts/resolve_upstream_conflicts.py` accepts reviewed conflict shapes only.
Sync copies it outside the checkout before rebase (old commits would otherwise
replace it), runs its tests, and logs `git diff --cc` before aborting when a
shape is rejected. The ordinary Test workflow also runs the resolver tests.

For each rule change:

- Use the real hunk and inspect the upstream change and replayed fork patch.
  A combined diff alone may omit important surrounding context.
- State what each side owns. Do not assume different assignments are harmless,
  or that a shared heading means all surrounding prose may be discarded.
- Add the real positive fixture and nearby negative fixtures. Unknown commands,
  competing values, or changed action repositories must still be rejected.
- Exercise the CLI against Git index stages when the rule uses merge-base
  information; string-only fixtures cannot verify that behavior.
- Replay the complete commit series using a saved copy of the new resolver.
  Inspect the result against upstream and the intended fork delta. A clean
  merge or a marker-free file is not proof of correctness.

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/test_resolve_upstream_conflicts.py
```

Current reviewed policies:

- **Server imports/initialization:** keep upstream and the deploy commit's
  known additions. Inherited lines are identified from Git stage 1, not guessed
  from a field name. This prevents resurrecting imports upstream removed. An
  unfamiliar fork addition or competing `s.deployManager` value aborts.
- **README:** the fork's side wins wholesale, because the fork owns the file.
  The resolver aborts if upstream's side introduces a heading absent from the
  pre-rebase file (a new upstream section), and the merged result must still
  contain the fork's title and the `shelley.dev` link.
- **Node setup:** upstream replaced the two pinned setup actions with one
  `scripts/with-node` step and deleted `ui/.nvmrc`. Adopt upstream's step and
  re-apply the fork's release-skip condition in `release.yml`; anything other
  than that exact step aborts. `sync-upstream.yml` is fork-only, so it merges
  clean, so the resolver never sees it. `scripts/check_deleted_paths.py`
  covers that gap (see below).
- **Release workflow:** preserve the upstream pnpm action upgrade together
  with the fork's unchanged release-skip guard. Other commands, repositories,
  and conditions are not version-only changes.
- **Headless-shell:** the fork intentionally omits upstream's separate Chromium
  release job and downloads upstream assets instead.
- **Deleted publisher:** `.github/workflows/publish-version-metadata.yml` is
  intentionally removed (no fork Pages publisher). Only the expected upstream
  modify/fork-delete index stages may be resolved by deleting it.
- **Dependencies:** validate checksum lines and reject conflicting hashes for
  the same module/version, then union and `go mod tidy`. GoReleaser also runs
  tidy, so verify that its hook will not dirty the committed tree.

### A clean merge is not a correct merge

A rebase reports conflicts only where both sides touched the same lines. If
upstream **deletes** a file and a fork-only file still refers to it, nothing
conflicts: the fork-only file is not part of the merge, so git reports success
while the tree is broken. Upstream deleted `ui/.nvmrc` on 2026-10-28;
`release.yml` and `test.yml` conflicted and were handled, but the fork-only
`sync-upstream.yml` merged cleanly and kept reading `.nvmrc`.

`scripts/check_deleted_paths.py` closes that gap. It diffs the pre-rebase
revision against HEAD, takes the deleted paths, and fails if any *functional*
file — workflows, `Makefile`, `scripts/*`, `.goreleaser.yml`, `ui/package.json`,
`go.mod` — still refers to one. The sync runs it after resolving and before
pushing; `Test` runs it with the resolver tests.

Deliberate deletions are exempt because the checker reads the resolver's
`DELETED_PATHS`, so intent lives in one place. Prose mentions and undefined
output paths (`bin/shelley-*`, artifact directories) are ignored on purpose:
they cannot break a build, and flagging them would train people to bypass the
check. The files that exist to *describe* deletions are exempt for the same
reason.

```bash
python3 scripts/check_deleted_paths.py --base <pre-rebase-revision>
```

If it fails after an upstream change, the fix is to update the reference, not
to silence the check.

## Verify before pushing

Save a backup ref before rewriting history. Rehearse in a uniquely named
scratch worktree; do not reuse an old worktree without checking its state.

```bash
git branch backup/custom-before-DATE custom
git worktree add --detach /tmp/shelley-rehearse-DATE FAILED_HEAD_SHA
# Run rebase and reviewed conflict resolutions there, then inspect the result.
```

Rebase `custom` onto current `origin/main`, preserving deploy functionality and
upstream features. Inspect the server manager/routes, App modal/prop, chat event,
overflow item, and command-palette action. Textual presence checks can help,
but do not verify types, runtime behavior, or semantic correctness. Likewise,
grepping for rule anchors cannot prove a resolver is safe or future-proof.

### Toolchain and build order

Use the Node version in `ui/.nvmrc` and pnpm version in
`ui/package.json` (`packageManager`). Upstream's October 23 toolchain upgrade
also moved pnpm settings into `ui/pnpm-workspace.yaml`. Keep the fork-only sync
workflow aligned with those files, not an old hard-coded Node/pnpm version.

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
