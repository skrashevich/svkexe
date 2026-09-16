#!/usr/bin/env bash
# Merge a newer upstream Shelley commit into the vendored shelley/ tree.
#
# shelley/ is a squashed git subtree of boldsoftware/shelley; the platform's
# changes to it are ordinary commits in this repository. Updating upstream is
# therefore a three-way merge: git stops on the files where upstream and the
# platform changed the same lines, and the conflict is resolved in place like
# any other merge. Re-run the script after committing the resolution; it then
# only records the new pin in upstream.env.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPSTREAM=https://github.com/boldsoftware/shelley.git
COMMIT="${1:-}"
[[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || { echo "usage: $0 <full 40-hex upstream commit>" >&2; exit 2; }
TOPLEVEL="$(git -C "$ROOT" rev-parse --show-toplevel 2>/dev/null)" || { echo "Run from a git checkout of the repository" >&2; exit 1; }
cd "$TOPLEVEL"
PREFIX="$(git -C "$ROOT/shelley" rev-parse --show-prefix)"
PREFIX="${PREFIX%/}"
[[ -z "$(git status --porcelain --untracked-files=no)" ]] || { echo "Commit or stash your changes first" >&2; exit 1; }

# The last squash commit records which upstream commit the tree contains.
vendored() {
    git log -1 --format=%B --grep="^git-subtree-dir: $PREFIX\$" | sed -n 's/^git-subtree-split: //p'
}

[[ -n "$(vendored)" ]] || {
    echo "No subtree squash commit for $PREFIX is reachable from HEAD; a shallow clone needs 'git fetch --unshallow' first" >&2
    exit 1
}

if [[ "$(vendored)" != "$COMMIT" ]]; then
    if ! git subtree pull --prefix="$PREFIX" --squash -m "build: merge Shelley ${COMMIT:0:12} into agent/shelley" "$UPSTREAM" "$COMMIT"; then
        if [[ -n "$(git ls-files --unmerged)" ]]; then
            cat >&2 <<MSG

The merge stopped on conflicts. Resolve them under $PREFIX, 'git add' the
resolved files and 'git commit' the merge, then re-run:
    $0 $COMMIT
MSG
        fi
        exit 1
    fi
fi
[[ "$(vendored)" == "$COMMIT" ]] || { echo "shelley/ does not record $COMMIT after the merge" >&2; exit 1; }

if ! grep -qx "SHELLEY_COMMIT=$COMMIT" "$ROOT/upstream.env"; then
    python3 - "$ROOT/upstream.env" "$COMMIT" <<'PY'
import re, sys
path, commit = sys.argv[1], sys.argv[2]
text = open(path).read()
open(path, "w").write(re.sub(r"^SHELLEY_COMMIT=.*$", f"SHELLEY_COMMIT={commit}", text, flags=re.M))
PY
    git add -- "$ROOT/upstream.env"
    git commit -q -m "build: pin Shelley ${COMMIT:0:12} in agent/upstream.env" -- "$ROOT/upstream.env"
fi
echo "shelley/ is at upstream $COMMIT"
