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
REQUIRED=(python3)
[[ "$MODE" == prepare ]] || REQUIRED+=(go node npm npx make)
for cmd in "${REQUIRED[@]}"; do
    command -v "$cmd" >/dev/null || { echo "Required build tool missing: $cmd" >&2; exit 1; }
done
# Resolve caller-supplied paths before changing directories.
BUILD_DIR="$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "${AGENT_SOURCE_DIR:-$ROOT/bin/source-$PROFILE}")"
OUTPUT="$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "${AGENT_OUTPUT:-$ROOT/bin/picoclaw}")"
# shelley/ is the tracked source; the build works on a synced copy so that the
# profile overlay, the application name and build products never enter it.
UI_DIGEST="$(python3 "$ROOT/prepare.py" "$BUILD_DIR" "$PROFILE" "$APP_NAME")"
cd "$BUILD_DIR"
[[ "$MODE" != prepare ]] || exit 0
UI_KEY="$NODE_VERSION:$PNPM_VERSION:$UI_DIGEST"
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
