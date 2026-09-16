#!/usr/bin/env bash
# This directory is the complete build input; no parent checkout is required.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$ROOT/upstream.env"
MODE="${1:-build}"
case "$MODE" in build|test|prepare) ;; *) echo "usage: $0 [build|test|prepare]" >&2; exit 2;; esac
PROFILE="${AGENT_PROFILE:-standalone}"
case "$PROFILE" in
    standalone) DEFAULT_NAME=PicoClaw; VERSION_SUFFIX=""; LISTEN_HOST=127.0.0.1 ;;
    svkexe) DEFAULT_NAME=svkexe; VERSION_SUFFIX=-svkexe; LISTEN_HOST="" ;;
    *) echo "Unknown AGENT_PROFILE: $PROFILE (expected standalone or svkexe)" >&2; exit 2 ;;
esac
APP_NAME="${AGENT_APP_NAME:-$DEFAULT_NAME}"
for cmd in git python3; do
    command -v "$cmd" >/dev/null || { echo "Required build tool missing: $cmd" >&2; exit 1; }
done
if [[ "$MODE" != prepare ]]; then
    for cmd in go node npm npx make; do
        command -v "$cmd" >/dev/null || { echo "Required build tool missing: $cmd" >&2; exit 1; }
    done
fi
# Resolve caller-supplied paths before changing directories.
BUILD_DIR="$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "${AGENT_SOURCE_DIR:-$ROOT/bin/source-$PROFILE}")"
OUTPUT="$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "${AGENT_OUTPUT:-$ROOT/bin/picoclaw}")"
# Explicit inputs exclude bin/ and generated sources. Profile and name belong
# in the cache key so one project's UI cannot silently enter another's build.
FINGERPRINT="$(python3 - "$ROOT" "$PROFILE" "$APP_NAME" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
h = hashlib.sha256()
for value in sys.argv[2:]:
    h.update(value.encode() + b'\0')
inputs = ['build.sh', 'configure.py', 'upstream.env', 'runtime.patch', 'go.mod.lock', 'go.sum.lock', 'overlay', 'profiles/' + sys.argv[2]]
for entry in inputs:
    path = root / entry
    files = sorted(path.rglob('*')) if path.is_dir() else [path]
    for file in files:
        if file.is_file():
            h.update(str(file.relative_to(root)).encode() + b'\0')
            h.update(file.read_bytes())
print(h.hexdigest())
PY
)"
if [[ ! -f "$BUILD_DIR/.picoclaw-fingerprint" ]] || [[ "$(cat "$BUILD_DIR/.picoclaw-fingerprint")" != "$FINGERPRINT" ]]; then
    if [[ -e "$BUILD_DIR" ]] && [[ ! -f "$BUILD_DIR/.picoclaw-fingerprint" && ! -f "$BUILD_DIR/.svkexe-fingerprint" ]]; then
        echo "Refusing to replace unmarked $BUILD_DIR" >&2
        exit 1
    fi
    mkdir -p "$(dirname "$BUILD_DIR")"
    SOURCE_DIR="$(mktemp -d "${BUILD_DIR}.XXXXXX")"
    trap 'rm -rf "$SOURCE_DIR"' EXIT
    git -C "$SOURCE_DIR" init -q
    git -C "$SOURCE_DIR" fetch -q --depth=1 https://github.com/boldsoftware/shelley.git "$SHELLEY_COMMIT"
    git -C "$SOURCE_DIR" checkout -q --detach FETCH_HEAD
    [[ "$(git -C "$SOURCE_DIR" rev-parse HEAD)" == "$SHELLEY_COMMIT" ]]
    git -C "$SOURCE_DIR" apply "$ROOT/runtime.patch"
    cp "$ROOT/go.mod.lock" "$SOURCE_DIR/go.mod"
    cp "$ROOT/go.sum.lock" "$SOURCE_DIR/go.sum"
    python3 "$ROOT/configure.py" "$SOURCE_DIR" "$PROFILE" "$APP_NAME"
    printf '%s\n' "$FINGERPRINT" > "$SOURCE_DIR/.picoclaw-fingerprint"
    # Upgrade old marked caches too. Rebuild retained UI assets below.
    if [[ -e "$BUILD_DIR" ]]; then
        if [[ -d "$BUILD_DIR/ui/node_modules" ]]; then mv "$BUILD_DIR/ui/node_modules" "$SOURCE_DIR/ui/node_modules"; fi
        if [[ -d "$BUILD_DIR/ui/dist" ]]; then mv "$BUILD_DIR/ui/dist" "$SOURCE_DIR/ui/dist"; fi
        rm -rf "$BUILD_DIR"
    fi
    mv "$SOURCE_DIR" "$BUILD_DIR"
    trap - EXIT
fi
cd "$BUILD_DIR"
[[ "$MODE" != prepare ]] || exit 0
UI_KEY="$SHELLEY_COMMIT:$NODE_VERSION:$PNPM_VERSION:$FINGERPRINT"
if [[ ! -f ui/dist/.picoclaw-build ]] || [[ "$(cat ui/dist/.picoclaw-build)" != "$UI_KEY" ]]; then
    (cd "$ROOT" && npx --yes --package "node@$NODE_VERSION" --package "pnpm@$PNPM_VERSION" -- pnpm --dir "$BUILD_DIR/ui" install --frozen-lockfile)
    (cd "$ROOT" && npx --yes --package "node@$NODE_VERSION" --package "pnpm@$PNPM_VERSION" -- pnpm --dir "$BUILD_DIR/ui" run build)
    printf '%s\n' "$UI_KEY" > ui/dist/.picoclaw-build
fi
make templates
if [[ "$MODE" == test ]]; then
    make exe-scroll
    export TMPDIR="$(python3 -c 'import os,tempfile; print(os.path.realpath(tempfile.gettempdir()))')"
    go test ./loop ./server ./modelsources ./db ./llm/...
else
    AGENT_GOOS="${AGENT_GOOS:-$(go env GOOS)}"
    AGENT_GOARCH="${AGENT_GOARCH:-$(go env GOARCH)}"
    GOOS="$AGENT_GOOS" GOARCH="$AGENT_GOARCH" make exe-scroll
    mkdir -p "$(dirname "$OUTPUT")"
    CGO_ENABLED=0 GOOS="$AGENT_GOOS" GOARCH="$AGENT_GOARCH" go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X shelley.exe.dev/version.Version=picoclaw-$PICOCLAW_VERSION$VERSION_SUFFIX -X shelley.exe.dev/version.Customized=true -X main.defaultListenHost=$LISTEN_HOST" \
        -o "$OUTPUT" ./cmd/shelley
fi
