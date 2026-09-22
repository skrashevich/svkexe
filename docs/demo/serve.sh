#!/usr/bin/env bash
# Run from any directory; all credentials and database files are temporary.
set -euo pipefail
cd "$(dirname "$0")/../.."
demo_dir=$(mktemp -d)
demo_pid=
cleanup() {
  if [[ -n "$demo_pid" ]]; then kill "$demo_pid" 2>/dev/null || true; wait "$demo_pid" 2>/dev/null || true; fi
  rm -rf "$demo_dir"
}
trap cleanup EXIT
ssh-keygen -q -t ed25519 -N '' -f "$demo_dir/client"
go build -o "$demo_dir/server" ./docs/demo
cat > "$demo_dir/ssh_config" <<CONFIG
Host svkexe-demo
  HostName 127.0.0.1
  Port 22222
  User svkexe
  IdentityFile $demo_dir/client
  IdentitiesOnly yes
  UserKnownHostsFile $demo_dir/known_hosts
  StrictHostKeyChecking accept-new
  LogLevel ERROR
CONFIG
"$demo_dir/server" -public-key "$demo_dir/client.pub" &
demo_pid=$!
export SVKEXE_DEMO_SSH_CONFIG="$demo_dir/ssh_config"
for attempt in {1..100}; do
  if curl -fsS http://127.0.0.1:18080/dashboard/vms >/dev/null 2>&1 && ssh -F "$SVKEXE_DEMO_SSH_CONFIG" svkexe-demo 'ls' >/dev/null 2>&1; then break; fi
  kill -0 "$demo_pid"
  sleep 0.2
done
# Fail rather than record a broken session.
ssh -F "$SVKEXE_DEMO_SSH_CONFIG" svkexe-demo 'ls' >/dev/null
if [[ "${1:-}" == record ]]; then
  vhs -o "$demo_dir/session.gif" docs/demo/ssh.tape
  test -s "$demo_dir/session.gif" || { echo "VHS produced no GIF; see docs/demo/README.md" >&2; exit 1; }
  mv "$demo_dir/session.gif" docs/media/ssh-session.gif
  { for command in 'ls' 'stat dev' 'help new' 'stat dev --json'; do
      printf '\n$ ssh svkexe-demo "%s"\n' "$command"
      ssh -F "$SVKEXE_DEMO_SSH_CONFIG" svkexe-demo "$command"
    done; } > docs/media/ssh-session.txt
else
  printf 'Open http://127.0.0.1:18080/dashboard/vms; Ctrl+C to stop.\n'
  wait "$demo_pid"
fi
