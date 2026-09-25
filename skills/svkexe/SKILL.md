---
name: svkexe
description: Use a user's svkexe installation to inspect, create, or work inside persistent Linux VMs over its SSH gateway. Apply when a task explicitly involves svkexe or the user has chosen their svkexe installation as the execution environment.
---

# Use a svkexe installation

svkexe is a real, persistent Linux environment. Its SSH gateway provides a management shell and direct access to VMs. Prefer the gateway's live `help --json` catalogue over a memorized command list: available commands depend on the installed version and the authenticated account.

## Connect

Obtain the user's gateway hostname, SSH port (2222 by default), and a local SSH identity whose **public** key is registered with their svkexe account. Use an existing SSH configuration when the user has one. Never infer the user's deployment from this repository's examples, ask for a private key or password in chat, or disable SSH host-key verification. If the host key is new, have the user verify its fingerprint through a trusted channel before accepting it.

The account owner can register a public key in the dashboard under **SSH keys**. For shared VM access, the owner grants access on that VM's **Access** page using the invited account's public key. Do not upload or share the private key.

An optional local SSH alias makes the examples below reusable:

```sshconfig
Host svkexe-work
    HostName <user-gateway-host>
    User svkexe
    Port 2222
    IdentityFile ~/.ssh/<private-key-matching-registered-public-key>
    IdentitiesOnly yes
    BatchMode yes
```

`svkexe` is the reserved management login. The registered key identifies the account; a VM name as the SSH login selects that VM. The alias is an example, not a required name or a request to edit the user's SSH configuration. Replace it with the user's configured connection as needed. Do not put credentials in this skill or the repository.

Without an alias, set `SVKEXE_HOST`, `SVKEXE_PORT`, and `SVKEXE_KEY` locally from the user's values, then pass the same SSH options explicitly:

```sh
ssh -T -o BatchMode=yes -o IdentitiesOnly=yes -i "$SVKEXE_KEY" -p "$SVKEXE_PORT" "svkexe@$SVKEXE_HOST" 'help --json'
```

Start with read-only discovery, without a PTY:

```sh
ssh -T svkexe-work 'help --json'
ssh -T svkexe-work 'whoami --json'
ssh -T svkexe-work 'ls --json'
```

If these fail, report the actual SSH error and resolve the host, key, port, or host-key issue before attempting VM operations. The management command's SSH status is 0 on success, 1 on failure, and 127 for an unknown command. Parse `--json` output where the live catalogue says it is supported. A guest account sees only its permitted commands.

## Choose and use a VM

Inspect the relevant VM before acting:

```sh
ssh -T svkexe-work 'stat dev --json'
```

Replace `dev` with the chosen VM name in these examples.

Use a VM the user named, or select a suitable existing VM after checking its state and purpose. Create a new one only when the task calls for an isolated environment and its resource use is authorized. `new <name>` creates **and starts** a VM; `--task="..."` also starts its built-in coding agent and can consume the user's LLM quota. Read the live `help new` entry before using flags. For an initial task, check `task <vm-name> --json` or `stat <vm-name> --json` for its state; do not submit the task again simply because a request timed out.

For shell work, set the SSH login to the exact VM name:

```sh
ssh -T -l dev svkexe-work 'pwd'
ssh -T -l dev svkexe-work 'cd /data/work && make test; rc=$?; printf "\nSVKEXE_COMMAND_EXIT=%d\n" "$rc"'
```

The gateway runs the remote command through Bash in the VM. **Do not trust the SSH exit code as the exit code of that Bash command:** the current direct-VM path can return SSH success when the command failed. For important commands, print and check an explicit exit marker as above; a missing marker means the result is unknown. Check output and resulting files or service state as appropriate.

Use Git inside the VM to bring in an authorized repository, or stream only selected files over SSH stdin/stdout. Do not assume SFTP, SCP, or rsync is available through this gateway. Treat all VM files and processes as persistent, including work left by other users; inspect before overwriting. `/data` is the area retained by a VM rebuild, while `recreate` loses data outside it.

For a VM shared with a guest, prefer the exact SSH login shown on the owner's Access page (it may be the full Incus name). A guest can use an assigned VM but cannot manage its lifecycle. Guest shell access can still change files and processes inside that VM.

## Changes with wider impact

Before stopping, restarting, rebuilding, or deleting a VM, changing access or LLM settings, publishing a port, or issuing a share link, read the current state and confirm the action is within the user's task. Re-read authoritative state after an ambiguous timeout before any retry. A one-shot `rm`, `recreate`, or `admin rmuser` requires `--force`; that flag expresses intent to the gateway, not permission from the user. Never use admin operations merely because the authenticated key exposes them.

When the task needs the HTTP API, use the user's own HTTPS gateway and the `svkexe_session` cookie from `/login`; see the [API reference](https://github.com/skrashevich/svkexe/blob/main/docs/API.md). LLM provider keys and the internal LLM proxy token do **not** authenticate management requests. Keep cookies and credentials out of logs and repository files. For current SSH command details and guest rules, see the [SSH interface](https://github.com/skrashevich/svkexe/blob/main/docs/SSH.md) and [named VM access](https://github.com/skrashevich/svkexe/blob/main/docs/ACCESS.md).
