#!/usr/bin/env bash
# Compatibility entrypoint for the svkexe platform's Linux agent artifact.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AGENT_PROFILE=svkexe
export AGENT_GOOS="${AGENT_GOOS:-linux}"
export AGENT_SOURCE_DIR="${AGENT_SOURCE_DIR:-$ROOT/bin/agent-source}"
export AGENT_OUTPUT="${AGENT_OUTPUT:-$ROOT/bin/picoclaw}"
exec "$ROOT/agent/build.sh" "$@"
