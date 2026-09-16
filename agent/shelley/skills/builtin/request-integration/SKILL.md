---
name: request-integration
description: Optionally connecting an external service credential (API key, database, OAuth service) this exe.dev VM doesn't have attached. Emits a clickable exe.dev connect link the user clicks to grant it.
when: exe.dev
---

## Steps

1. Check what's already attached (see the `reflection-integration` skill):
   ```
   curl https://reflection.int.exe.xyz/integrations
   ```

2. Find the service's catalog handle (e.g. `stripe`, `gmail`, `github`):
   ```
   curl https://exe.dev/docs/integrations-catalog.md
   ```
   If that page 404s or the service isn't listed, use your best-guess handle —
   an unknown handle just opens the catalog pre-filled with a search.

3. Get this VM's name (the `.name` field):
   ```
   curl https://reflection.int.exe.xyz/
   ```

4. Post the connect link in conversation, with a short plain-language note on
   what you'll use it for:
   ```
   https://exe.dev/integrations/add?service=<handle>&attach=vm:<this-vm>&for=<duration>&source=shelley
   ```
   - `for=<duration>`: a Go duration (`2h`, `45m`, `24h`). Ask for the shortest
     window that covers the task; the grant lapses at expiry. Omit only if the
     user wants a permanent grant.
   - `source=shelley`: leave as-is.

   The user clicks through, pastes the credential (or authorizes OAuth), and
   submits. Nothing is granted until they submit.

5. Once the user says they've connected, re-run the step 1 curl to confirm
   before proceeding.

## Notes

- For non-credential CLI suggestions, `https://exe.dev/suggest?command=<url-encoded-command>`
  also works (allowlisted commands only, never secrets).
