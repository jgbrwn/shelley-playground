#!/usr/bin/env bash
# Rebase HEAD onto ONTO the way the nightly sync does, writing reports to DIR.
#
#   sync_rebase.sh ONTO DIR
#
# Each stop goes to resolve_upstream_conflicts.py. What it refuses goes to
# copilot_resolve.py when COPILOT_GITHUB_TOKEN is set, at most MAX_ROUNDS
# times per rebase. Anything still unresolved aborts the rebase and exits 1,
# with DIR/sync-report.md explaining why. Copilot's decisions are recorded in
# DIR/ai-resolution.md, and fork commits dropped because upstream already has
# their changes in DIR/dropped.txt. DIR/stops.txt lists every commit that
# stopped on a conflict.
#
# Run a copy from outside the worktree, together with the two Python scripts:
# rebasing rewrites scripts/ underneath a running shell.
set -euo pipefail

MAX_ROUNDS=5
onto=$1
dir=$2
tools=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$dir"
rounds=0

GIT_EDITOR=true git rebase "$onto" && exit 0
while true; do
  git log -1 --format='%h %s' REBASE_HEAD >> "$dir/stops.txt"
  if ! python3 "$tools/resolve_upstream_conflicts.py" 2> "$dir/refused.txt"; then
    cat "$dir/refused.txt" >&2
    replaying=$(git log -1 --format='%h %s' REBASE_HEAD)
    copilot=""
    if [ -n "${COPILOT_GITHUB_TOKEN:-}" ] && [ "$rounds" -lt "$MAX_ROUNDS" ]; then
      rounds=$((rounds + 1))
      echo "Asking Copilot to resolve the refused conflicts (round $rounds)" >&2
      if python3 "$tools/copilot_resolve.py" "$dir/ai-resolution.md" 2> "$dir/copilot.err"; then
        copilot=resolved
      else
        cat "$dir/copilot.err" >&2
        copilot=failed
      fi
    fi
    if [ "$copilot" != resolved ]; then
      {
        echo "The resolver refused a conflict while replaying \`$replaying\`:"
        echo
        echo '```'
        cat "$dir/refused.txt"
        echo '```'
        if [ "$copilot" = failed ]; then
          echo
          echo "Copilot could not resolve it either:"
          echo
          echo '```'
          tail -c 3000 "$dir/copilot.err"
          echo '```'
        fi
        echo
        echo '<details><summary><code>git diff --cc</code></summary>'
        echo
        echo '```diff'
        git diff --cc | head -c 40000
        echo '```'
        echo '</details>'
      } > "$dir/sync-report.md"
      git rebase --abort
      exit 1
    fi
  fi
  if git diff --cached --quiet HEAD; then
    # Every change in this fork commit is already upstream, typically because
    # upstream adopted it. An empty commit is dropped whoever resolved it.
    echo "::notice::Dropping $(git log -1 --format='%h %s' REBASE_HEAD): already upstream" | tee -a "$dir/dropped.txt" >&2
    GIT_EDITOR=true git rebase --skip && break
  else
    GIT_EDITOR=true git rebase --continue && break
  fi
done
