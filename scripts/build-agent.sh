#!/usr/bin/env bash
# Reproducible PicoClaw build with the preserved Shelley application shell.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/agent/upstream.env"
MODE="${1:-build}"
case "$MODE" in build|test|prepare) ;; *) echo "usage: $0 [build|test|prepare]" >&2; exit 2;; esac
for cmd in go git node npm python3 make; do
    command -v "$cmd" >/dev/null || { echo "Required build tool missing: $cmd" >&2; exit 1; }
done
BUILD_DIR="$ROOT/bin/agent-source"
# Hash every overlay/lock input so stale sources cannot silently enter a build.
FINGERPRINT="$(python3 - "$ROOT/agent" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
h = hashlib.sha256()
for path in sorted(root.rglob('*')):
    if path.is_file():
        h.update(str(path.relative_to(root)).encode())
        h.update(path.read_bytes())
print(h.hexdigest())
PY
)"
if [[ ! -f "$BUILD_DIR/.svkexe-fingerprint" ]] || [[ "$(cat "$BUILD_DIR/.svkexe-fingerprint")" != "$FINGERPRINT" ]]; then
    mkdir -p "$ROOT/bin"
    SOURCE_DIR="$(mktemp -d "$ROOT/bin/agent-source.XXXXXX")"
    trap 'rm -rf "$SOURCE_DIR"' EXIT
    git -C "$SOURCE_DIR" init -q
    git -C "$SOURCE_DIR" fetch -q --depth=1 https://github.com/boldsoftware/shelley.git "$SHELLEY_COMMIT"
    git -C "$SOURCE_DIR" checkout -q --detach FETCH_HEAD
    [[ "$(git -C "$SOURCE_DIR" rev-parse HEAD)" == "$SHELLEY_COMMIT" ]]
    git -C "$SOURCE_DIR" apply "$ROOT/agent/runtime.patch"
    cp "$ROOT/agent/go.mod.lock" "$SOURCE_DIR/go.mod"
    cp "$ROOT/agent/go.sum.lock" "$SOURCE_DIR/go.sum"
    for src in "$ROOT"/agent/overlay/loop/*.go.in; do
        cp "$src" "$SOURCE_DIR/loop/$(basename "${src%.in}")"
    done
    printf '%s\n' "$FINGERPRINT" > "$SOURCE_DIR/.svkexe-fingerprint"
    # Only the generated, ignored build directory is replaced.
    if [[ -e "$BUILD_DIR" ]]; then
        [[ -f "$BUILD_DIR/.svkexe-fingerprint" ]] || { echo "Refusing to replace unmarked $BUILD_DIR" >&2; exit 1; }
        if [[ -d "$BUILD_DIR/ui/node_modules" ]]; then mv "$BUILD_DIR/ui/node_modules" "$SOURCE_DIR/ui/node_modules"; fi
        if [[ -d "$BUILD_DIR/ui/dist" ]]; then mv "$BUILD_DIR/ui/dist" "$SOURCE_DIR/ui/dist"; fi
        rm -rf "$BUILD_DIR"
    fi
    mv "$SOURCE_DIR" "$BUILD_DIR"
    trap - EXIT
fi
cd "$BUILD_DIR"
[[ "$MODE" != prepare ]] || exit 0
# The fingerprint covers the overlay, so patched UI sources rebuild the bundle
# instead of silently reusing the carried-over dist directory.
UI_KEY="$SHELLEY_COMMIT:$NODE_VERSION:$PNPM_VERSION:$FINGERPRINT"
if [[ ! -f ui/dist/.svkexe-commit ]] || [[ "$(cat ui/dist/.svkexe-commit)" != "$UI_KEY" ]]; then
    # Use the pinned build-time Node even on Debian/Ubuntu with older Node.
    # Bootstrap from the project root, outside the upstream npm configuration.
    (cd "$ROOT" && npx --yes --package "node@$NODE_VERSION" --package "pnpm@$PNPM_VERSION" -- pnpm --dir "$BUILD_DIR/ui" install --frozen-lockfile)
    (cd "$ROOT" && npx --yes --package "node@$NODE_VERSION" --package "pnpm@$PNPM_VERSION" -- pnpm --dir "$BUILD_DIR/ui" run build)
    printf '%s\n' "$UI_KEY" > ui/dist/.svkexe-commit
fi
make templates
if [[ "$MODE" == test ]]; then
    make exe-scroll
    # Upstream path tests compare canonical paths; normalize a symlinked TMPDIR.
    export TMPDIR="$(python3 -c 'import os,tempfile; print(os.path.realpath(tempfile.gettempdir()))')"
    go test ./loop ./server ./modelsources ./db ./llm/...
else
    AGENT_GOOS="${AGENT_GOOS:-linux}"
    AGENT_GOARCH="${AGENT_GOARCH:-$(go env GOARCH)}"
    GOOS="$AGENT_GOOS" GOARCH="$AGENT_GOARCH" make exe-scroll
    # -buildvcs=false: the agent source tree is a throwaway git checkout whose
    # owner depends on who ran the previous build, and go's VCS stamping fails
    # on a repo owned by someone else. The version is set via -ldflags below.
    CGO_ENABLED=0 GOOS="$AGENT_GOOS" GOARCH="$AGENT_GOARCH" go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X shelley.exe.dev/version.Version=picoclaw-$PICOCLAW_VERSION-svkexe -X shelley.exe.dev/version.Customized=true" \
        -o "${AGENT_OUTPUT:-$ROOT/bin/picoclaw}" ./cmd/shelley
fi
