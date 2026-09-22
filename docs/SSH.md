---
title: SSH interface
description: The gateway's SSH management shell — connecting, the command surface, and the machine-readable catalogue for LLM agents
---

# SSH interface

The gateway listens on port 2222 (`SSH_ADDR`) and offers two things over one
connection: a management shell covering everything the web dashboard does, and
direct shell access into your VMs.

Authentication is by SSH key only. A key is registered from the dashboard
(**SSH keys**) or from the shell itself, and it is the key — never the login
name — that decides who you are.

Guest accounts and named VM members have use-only permissions; see [ACCESS.md](ACCESS.md).
Use your own `DOMAIN` in place of `example.com` in every example.

## Connecting

```bash
# The management shell. Any login name works; the key identifies you.
ssh -p 2222 example.com
ssh -p 2222 anything@example.com

# A login that names one of your own VMs goes straight into it.
ssh -p 2222 dev@example.com

# One command per connection, for scripts and agents.
ssh -p 2222 example.com "ls --json"

# A command with a VM login runs inside that VM.
ssh -p 2222 dev@example.com "systemctl status myapp"
```

A login that does not name one of your VMs is not an error: it lands on the
management shell. This matters because `ssh -p 2222 example.com` sends your local username
by default, and that is rarely one of your VM names.

One login is reserved: `svkexe@` always opens the management shell, even when a
VM of yours happens to be named after your local account.

## For LLM agents

The shell is meant to be driven by programs as well as people.

- **`help --json`** prints the entire catalogue as one JSON document: every
  command available to the calling key, with its usage, description, arguments,
  flags, subcommands and examples, plus how the gateway itself is invoked. It
  is the only thing an agent needs to read before acting.
- A session that does not request a PTY — which is what a script or an agent
  gets — is greeted with a short brief naming the gateway, the authenticated
  account and that command, instead of an ASCII banner.
- Read commands accept **`--json`** and then emit a single line of JSON built
  from typed structures, with no terminal escapes and no carriage returns.
- A one-shot invocation exits **0** on success, **1** when the command failed
  and **127** when the command does not exist.
- An unrecognised flag is refused by name rather than ignored, so a typo is
  visible instead of silently changing what the command did. Flags belong to the
  action, not to the word it is grouped under: `share list` takes `--json` and
  not `--ttl`, `share revoke` takes neither.
- A destructive command — `rm`, `recreate`, `admin rmuser` — has to be told
  `--force` when it arrives as a one-shot. The reason is the login: `ssh
  dev@example.com "rm foo"` deletes a file inside the VM while `dev` exists, and
  deletes the VM named `foo` once it does not.
- An argument that would otherwise be read as an action word is escaped with
  `--`: a VM named `retry` is addressed as `task -- retry`.

```bash
ssh -p 2222 example.com "help --json" | jq '.commands[] | {name, usage, summary}'
ssh -p 2222 example.com "ls --json"   | jq '.[] | select(.status == "running") | .name'
```

## The command surface

Commands are grouped; `help` lists the groups, `help <command>` explains one and
`help <command> <action>` one of its actions. An administrator additionally sees
the `admin` group; for everyone else those commands are neither listed, nor
accepted, nor described by an error.

### VM lifecycle

| Command | Does |
| --- | --- |
| `ls [--json]` | List your VMs |
| `new <name> [--cpu=N] [--memory=MB] [--disk=GB] [--task="..."]` | Create and start a VM |
| `recreate <name> [--force]` | Rebuild a VM from the latest image, keeping /data |
| `rename <old> <new>` | Rename a VM |
| `restart <name>` | Restart a VM |
| `rm <name> [--force]` | Delete a VM |
| `ssh <name>` | Open a shell inside a VM |
| `start <name>` | Start a VM |
| `stat <name> [--json]` | Show everything known about a VM |
| `stop <name>` | Stop a VM |

### VM configuration

| Command | Does |
| --- | --- |
| `agent <vm> [--json]` | Show or install the PicoClaw agent build |
| `agent update <vm>` | install the gateway's agent build into the VM |
| `nesting <vm> [on\|off] [--json]` | Show or set whether the VM may run containers |
| `publish <vm> [port] [public\|private] [--json]` | Show or set the port the VM serves on |
| `share <vm> [--ttl=DURATION] [--json]` | Create, list or revoke a shareable link |
| `share list <vm> [--json]` | list the VM's share links |
| `share revoke <token>` | delete one share link by its token |
| `task <vm> [--json]` | Show or retry the VM's initial task |
| `task retry <vm>` | hand a failed task to the agent again |
| `url <vm> [--json]` | Print every address this VM answers on |

### Custom domains

| Command | Does |
| --- | --- |
| `domain` | Point your own hostnames at a VM |
| `domain list <vm> [--json]` | list a VM's custom domains and whether each is routed |
| `domain add <vm> <hostname>` | claim a hostname for a VM and check it at once |
| `domain verify <vm> <hostname\|alias-id>` | re-run the DNS check, after fixing the record |
| `domain rm <vm> <hostname\|alias-id>` | release a hostname, freeing it to be claimed again |

### Account

| Command | Does |
| --- | --- |
| `llm [list\|add\|rm\|models\|default] [arguments]` | Manage the LLM connections your agents use |
| `llm list [--json]` | list your connections, without their credentials |
| `llm add <provider> [key] [--base-url=U] [--models=a,b] [--protocol=P]` | store or replace one provider's settings |
| `llm rm <provider>` | delete that provider's connection |
| `llm models [--json]` | list the models you can put a VM on |
| `llm default <model\|auto>` | choose the model your VMs open on |
| `ssh-key [list\|add\|remove] [arguments]` | Manage the SSH keys that reach this account |
| `ssh-key list [--json]` | list your keys by name and fingerprint |
| `ssh-key add <name> <public-key>` | register an OpenSSH public key |
| `ssh-key remove <name>` | remove the key with that name |
| `passwd` | Set your web login password interactively (requires a PTY: `ssh -t -p 2222 svkexe@example.com passwd`) |
| `whoami [--json]` | Show who you are and what this account holds |

### Session

| Command | Does |
| --- | --- |
| `exit` | Close the session |
| `help [command] [action] [--json]` | List the commands available to you |

### Administration

| Command | Does |
| --- | --- |
| `admin` | Platform-wide operator actions (admin only) |
| `admin users [--json]` | Every account with its role, VM count and creation date |
| `admin rmuser <id\|email> [--force]` | Delete an account and every record the gateway keeps for it |
| `admin vms [--json]` | Every VM on the platform with the account that owns it |
| `admin nesting [on\|off]` | Report or set the deployment-wide ceiling on nested containers |
| `admin domains [--json]` | Every custom domain claimed on the platform, with its VM and owner |
| `admin release <hostname>` | Free a hostname platform-wide so another account can claim it |
| `admin update [status\|check\|start] [--force] [--json]` | The running build, what is available upstream, and the update itself |

## Relationship to the dashboard and the API

All three surfaces act on the same data through the same code paths: a VM
created over SSH is identical to one created from the dashboard, a published
port set here refreshes the agent's guide inside the VM exactly as the web form
does, and a custom domain added here goes through the same DNS verification.
Management commands require ownership. Use commands also allow named VM
members; a VM outside both scopes is not accessible.
