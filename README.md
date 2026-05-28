# becs-runner

A small, self-contained web app for running TCL batch scripts across multiple
remote environments without the manual SSH + `curl` dance.

If you run the same batch scripts against more than one environment, you
probably know the routine: SSH into a box, `curl` a login, `curl` the batch run,
poll for completion, fetch the result, `curl` a logout — then repeat for the
next environment. becs-runner does that for you from a single page.

It talks to each environment over its JSON-RPC API (port 4499), establishes any
required SSH or WireGuard tunnel first, runs the script, polls for completion,
and stores each result as a job log you can review later.

## What it does

- **Environment management** — add/edit/remove target environments (host, port,
  credentials, tunnel settings) through a web UI. Credentials are encrypted at
  rest.
- **Tunnels** — connects through an **SSH** port-forward or a **userspace
  WireGuard** tunnel before reaching the API. No root, no kernel module,
  no TUN device — WireGuard runs in-process via `wireguard/tun/netstack`.
- **Batch execution** — runs a TCL batch script against the environments you
  select, one at a time, polling until each finishes.
- **Job log** — every run is saved as a JSON file under `jobs/`, with output and
  timing, viewable and downloadable from the UI.

## Who it's for

This is a **niche operations tool** for engineers who run the same TCL batch
scripts against one or more remote environments and are tired of doing it by
hand. It's the kind of thing you run on your own workstation or a small internal
box to save yourself the repetitive SSH/curl loop.

It is **not** a general-purpose automation platform, and it is **not hardened for
public/multi-user deployment** (see caveats below). It assumes the target
environments expose a JSON-RPC batch API of the shape it was built against — if
that doesn't describe your setup, this tool isn't aimed at you.

## Honest status & caveats

This is a personal project. The core works end-to-end, but go in with eyes open:

- **No web authentication.** Anyone who can reach the listen address can use it
  and read job output. Run it on localhost or behind your own access control.
  Auth is a planned Phase 2 item, not done.
- **The script is currently hardwired** to `batch_accounting.tcl` with
  `svc_list` / `parentoid` inputs on the dashboard. Running arbitrary scripts
  isn't exposed in the UI yet.
- **Serial execution only** — one environment (and one tunnel) at a time, by
  design. This keeps tunnel lifecycle simple, especially for WireGuard.
- **Lightly tested.** It's been exercised against real environments, but there's
  no automated test suite and the tunnel code in particular hasn't seen broad
  use. Expect rough edges.
- **A few protocol questions are still open** (e.g. exact file-read encoding,
  session timeout behavior). These don't block normal use but are noted in the
  spec.

## How it works

```
Browser ──HTMX──> becs-runner (Go) ──tunnel?──> Target JSON-RPC API (:4499)
                       │
                       ├─ config.yaml   (environments; passwords encrypted)
                       └─ jobs/*.json   (one file per job)
```

No database and no JS build step: the UI is Go `html/template` + HTMX +
PicoCSS. State lives in `config.yaml` (environments) and the `jobs/` directory
(results).

## Requirements

- Go 1.22+
- Network reachability to your target environments (directly or via the
  configured SSH/WireGuard tunnel)

## Build & run

```bash
# build
go build -o becs-runner ./cmd        # use ./cmd; on Windows add .exe

# run
./becs-runner --listen :8080 --data-dir .
```

| Flag         | Default | Purpose                                    |
|--------------|---------|--------------------------------------------|
| `--listen`   | `:8080` | Address to bind the web server             |
| `--data-dir` | `.`     | Directory holding `config.yaml` and `jobs/`|

Then open <http://127.0.0.1:8080>.

## Configuration

### Master key

Stored credentials (login passwords, SSH passwords, WireGuard keys) are
encrypted at rest using AES-256-GCM with a key derived from a passphrase via
PBKDF2. Set the passphrase in `BECS_RUNNER_KEY` before starting:

```powershell
# Windows / PowerShell — current session
$env:BECS_RUNNER_KEY = "your-master-passphrase"
```

```bash
# Linux / macOS
export BECS_RUNNER_KEY="your-master-passphrase"
```

Use the **same** passphrase every time — if it changes, previously saved
credentials can no longer be decrypted. If it's unset, the app still starts but
logs a warning and credential encryption/decryption will fail.

### Environments

Add environments through the UI (`/environments`). They're written to
`config.yaml` in the data directory, with secrets stored encrypted. Job results
accumulate as JSON files in `jobs/`.

## Tech stack

- **Go 1.22**, standard-library HTTP server and `html/template`
- **HTMX 2.x** for interactivity, **PicoCSS** for styling — no JS build step
- `golang.org/x/crypto/ssh` for SSH tunnels
- `golang.zx2c4.com/wireguard` (+ `tun/netstack`) for userspace WireGuard
- AES-256-GCM + PBKDF2 from the standard library for credential encryption

## Roadmap (not yet implemented)

- Web authentication (basic/session) for non-local use
- Selecting/running scripts other than `batch_accounting.tcl` from the UI
- Tests around the tunnel and API client paths

## License

Do not distribute.
