# External service integrations

Connections belong to your account and are available to every VM you own. GitHub is the first supported provider, using a personal access token. Use the permissions and repository access required for your work when creating the PAT.

GitHub credentials are verified with an authenticated `GET https://api.github.com/user` before saving in web, SSH, or REST. A rejected token is not saved; if GitHub cannot be reached or rate-limits the request, verification fails and the previous connection remains unchanged. This verifies authentication, not access to every repository or operation.

Open **Dashboard → Integrations**, enter the PAT, and save. The same page replaces or disconnects a connection. Saved secrets are write-only. All processes inside your VMs can read the credentials you connect, including collaborators with access to those VMs.

## SSH

```sh
ssh -t -p 2222 svkexe@GATEWAY_HOST integration add github
```

Enter the token at the hidden prompt. For automation, send JSON on stdin (do not put secrets into command arguments or commit credential files):

```sh
ssh -p 2222 svkexe@GATEWAY_HOST integration add github < credentials.json
```

The input shape is `{"config":{},"secrets":{"token":"YOUR_PAT"}}`. Use `integration providers --json` for supported providers and their fields, `integration list --json` for configured connections, and `integration rm github` to disconnect.

## REST

Authenticated endpoints:

- `GET /api/integrations/providers`: provider descriptors, including field names and exported credential names.
- `GET /api/integrations`: configured connections and public configuration, without secrets.
- `PUT /api/integrations/{provider}`: replace the connection using the same JSON input as SSH. All required fields must be supplied.
- `DELETE /api/integrations/{provider}`: disconnect; safe to repeat.

## Use gh inside a VM

The metadata service publishes `/latest/meta-data/svkexe/integrations/{provider}/{credential}`. Directories can be discovered using GET. Reading a credential requires an IMDSv2 token bound to this VM:

```sh
imds_token=$(curl --noproxy '*' -fsS -X PUT \
  -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' \
  http://169.254.169.254/latest/api/token) || exit

github_token=$(curl --noproxy '*' -fsS \
  -H "X-aws-ec2-metadata-token: $imds_token" \
  http://169.254.169.254/latest/meta-data/svkexe/integrations/github/token) || exit
export GH_TOKEN="$github_token"
unset github_token imds_token
gh auth status
```

`gh` uses `GH_TOKEN` ([GitHub CLI documentation](https://cli.github.com/manual/gh_help_environment)); it does not fetch this metadata itself. No VM restart is needed after saving, replacing, or disconnecting. Disconnecting prevents future downloads; revoke the PAT at GitHub to invalidate copies already downloaded. Do not persist downloaded credentials in VM images or shell profiles. The metadata service and its host/guest routing must be enabled.

## Agent instructions

PicoClaw receives discovery and authorization instructions in `/etc/picoclaw/AGENTS.md`, including IMDSv2 and a GitHub `GH_TOKEN` example. The guide tells it to fetch secrets only when needed within one shell invocation, avoid printing/persisting them, and ask the owner to connect a missing service. Running VMs receive the guide during gateway startup reconciliation after an update; stopped VMs receive it on their next start. Instructions are included only when metadata is enabled.

## Add a provider

Implement `integrations.Provider`: `Descriptor`, `Validate(context.Context, Input)`, and `Credential`. The descriptor declares public configuration fields, write-only secret fields, and named credential leaves. Fields are strings; provider validation can enforce provider-specific formats. Use the request context for upstream verification and bound its duration. Return `ErrRejected` for rejected credentials, `ErrInvalid` for malformed input, and `ErrVerificationUnavailable` for upstream/network errors; error messages shown to users are fixed and never include credential values. `Credential` maps a connection into one exported value and can use several stored secret fields. Register the provider in the production registry in `integrations.New`.

The shared service handles validation of declared fields, owner scoping, AES-GCM storage, and credential reads. Stored credentials use authenticated context containing owner ID, provider ID, and schema version. Management adapters and metadata use provider descriptors, so no provider-specific changes to HTTP handlers, SSH commands, templates, or the database schema are needed. Tests can supply a custom registry to `integrations.New` and inject that service into adapters. Static credentials are supported initially; OAuth authorization and renewal belong in a future provider/service extension when needed.
