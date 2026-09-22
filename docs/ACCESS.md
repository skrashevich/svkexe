# Named access to individual VMs

Open **Access** on the owner's VM card. Enter the user's email and one OpenSSH
public key, then select **Grant access**. Repeat on each VM that person needs.
The owner can remove a grant on the same page with **Revoke access**.

New accounts receive the `guest` role. They can see and use only assigned VMs:
SSH, the web terminal, private applications, explicit application ports, and
PicoClaw. They cannot create VMs, start/stop/rebuild/delete them through the
platform, change their settings, issue share links, or manage access. Guests
can manage their own SSH keys and web password.

For an existing account, supply a key already registered on that account.
Inviting someone never adds a key to an existing identity or changes its role.
Existing regular users retain management rights over their own VMs; shared VMs
remain use-only.

## Connecting

Use the exact SSH command shown on the access page, for example:

```sh
ssh -p 2222 svkexe-OWNER_ID-VM_NAME@gateway.example
```

The key identifies the account. The SSH username selects the VM. Display names
also work when unambiguous; the Incus name works when several shared VMs have
the same display name. Ambiguous web hostnames are refused rather than routed
to an arbitrary VM.

For web access, set a password through the SSH gateway:

```sh
ssh -t -p 2222 svkexe@gateway.example passwd
```

The gateway prompts twice without echoing the password. Sign in on the gateway
with the invited email and that password, then open **Terminal**, **PicoClaw**,
or **Open** from the assigned VM's card. Substitute the deployment's SSH port
if it differs from the default 2222. No invitation email is sent automatically.

## Revocation and scope

Revocation blocks subsequent authorized requests and closes active shared SSH,
web-terminal and proxied connections after the next authorization check
(normally within one second). It does not undo commands or terminate detached
processes already started inside the VM. Shell access still permits changes
inside that VM; the restricted rights concern platform management.

Public apps and independently issued share links retain their existing behavior.
Custom-domain access retains the existing public/share-link authentication;
private authenticated app access uses the gateway's VM hostname.

The gateway creates `container_access` during the normal idempotent database
migration. User creation, SSH key registration and the first grant are atomic.
Deleting a VM or user removes its access grants. Stored ownership does not change.
