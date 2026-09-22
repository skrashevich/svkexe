# README media

These assets use the real `internal/dashboard` templates and `internal/sshgw`
server with a temporary SQLite database. `dev`, `staging` and `sandbox` are
sample records, not running containers. No production credentials, Incus socket
or LLM provider are used. The screenshots show the gateway dashboard, not an
agent conversation or a working VM shell.

## Requirements

- The Go version required by the root `go.mod`.
- Bash, curl, OpenSSH, and a browser for screenshots.
- [VHS](https://github.com/charmbracelet/vhs), ttyd, Chromium and ffmpeg for GIFs.
  The checked-in GIF was recorded with **VHS 0.11.0**. VHS 0.12.0 cancels the
  render context before invoking ffmpeg and can exit successfully without
  producing a GIF; use 0.11.0 or a version containing a fix. The script checks
  that the output exists before replacing the checked-in GIF.
- The dashboard loads fonts and htmx from its usual public CDNs, so screenshot
  capture needs network access. VHS uses the browser’s monospace font.

Run commands from the repository root. Ports `127.0.0.1:18080` and
`127.0.0.1:22222` must be free. Stop the preview before recording.

## Screenshots

```bash
bash docs/demo/serve.sh
```

Open <http://127.0.0.1:18080/dashboard/vms> at a **1440 × 850 CSS pixel** viewport.
The fixture automatically supplies the demo user on loopback. HTTP writes are
disabled; VM runtime operations return an explicit error.

1. Capture the viewport as `docs/media/webui-vms.png` after fonts load.
2. Click **+ New VM**. Set **Name** to `preview` and the initial task to
   `Build a Go API on port 3000 with a health endpoint and a small status page.`
3. Capture `docs/media/webui-create.png` without submitting the form.
4. Stop the server with Ctrl+C. The temporary database and client key are removed.

For automated capture, use Playwright with the same viewport and
`page.screenshot({path: ..., scale: 'css'})`. Capture the actual page; do not
replace templates with a separate mockup.

## SSH animation and transcript

```bash
bash docs/demo/serve.sh record
```

The script generates an ephemeral SSH key, starts the local gateway, waits for
HTTP and SSH readiness, runs `ssh.tape`, then saves a separate plain-text
transcript of `ls`, `stat dev`, `help new` and `stat dev --json`. Both are real
SSH responses from the application, not typed or hard-coded output.

`svkexe-demo` is a temporary SSH alias for `127.0.0.1:22222`; the tape's hidden
setup defines a shell function to pass that isolated config to OpenSSH. It does
not change `~/.ssh/config`. The animation demonstrates the interactive shell
and a one-shot JSON query. The text transcript additionally includes command
help for readers who prefer text.

Review the resulting GIF and both PNGs before committing. Keep media small
(the current set is under 2 MB) and retain this fixture and tape beside it so
UI and command changes can be reflected in the README.
