# tcl-script-runner

A small, self-contained web app for running TCL batch scripts across multiple
remote environments without the manual SSH + `curl` labor.

It talks to each environment over its JSON-RPC API (port 4499), establishes any
required SSH or WireGuard tunnel first, runs the script, polls for completion,
and stores each result as a job log you can review later.

## What it does

- **Environment management:** add/edit/remove target environments (host, port,
  credentials, tunnel settings) through a web UI. Credentials are encrypted at
  rest.
- **Tunnels:** connects through an **SSH** port-forward or a **userspace
  WireGuard** tunnel before reaching the API. No root, no kernel module, and no
  TUN device, because WireGuard runs in-process via `wireguard/tun/netstack`.
- **Batch execution:** runs a TCL batch script against the environments you
  select, one at a time, polling until each finishes.
- **Job log:** every run is saved as a JSON file under `jobs/`, with output and
  timing, viewable and downloadable from the UI.

## Who it's for

This is a niche operations tool for engineers who run the same TCL batch scripts
against one or more remote environments and are tired of doing it by hand. It's
the kind of thing you run on your own workstation or a small internal box to
save yourself the repetitive SSH/curl loop.

It is **not** a general-purpose automation platform, and it is **not hardened for
public/multi-user deployment** (see caveats below). It assumes the target
environments expose a JSON-RPC batch API of the shape it was built against. If
that doesn't describe your setup, this tool isn't aimed at you.

## Honest status & caveats

This is a personal project. The core works end-to-end, but go in with eyes open:

- **No web authentication.** Anyone who can reach the listen address can use it
  and read job output. Run it on localhost or behind your own access control.
  Auth is a planned Phase 2 item, not done.
- **The script is currently hardwired** to `batch_accounting.tcl` with
  `svc_list` / `parentoid` inputs on the dashboard. Running arbitrary scripts
  isn't exposed in the UI yet.
- **Serial execution only.** One environment (and one tunnel) at a time, by
  design. This keeps tunnel lifecycle simple, especially for WireGuard.
- **Lightly tested.** It's been exercised against real environments, but there's
  no automated test suite and the tunnel code in particular hasn't seen broad
  use. Expect rough edges.
- **A few protocol questions are still open** (for example, exact file-read
  encoding and session timeout behavior). These don't block normal use but are
  noted in the spec.

## How it works

```
Browser ──HTMX──> tcl-script-runner (Go) ──tunnel?──> Target JSON-RPC API (:4499)
                       │
                       ├─ config.yaml   (environments; passwords encrypted)
                       └─ jobs/*.json   (one file per job)
```

No database and no JS build step: the UI is Go `html/template` + HTMX +
PicoCSS. State lives in `config.yaml` (environments) and the `jobs/` directory
(results).

### Tunnels / VPN

Many environments aren't reachable directly: the JSON-RPC API sits behind a jump
host or a VPN. tcl-script-runner brings the tunnel up *before* a run and tears it
down *after*, and because runs are serial there's only ever one tunnel active at
a time. Three modes:

- **None:** connect straight to `becs_host:becs_port`.

- **SSH:** a port-forward built with `golang.org/x/crypto/ssh` (pure Go, no
  shelling out to a system `ssh` binary). It binds a local port and forwards
  `localhost:<local_port>` to the SSH host and on to
  `<remote_host>:<remote_port>`; the API client then just talks to the local
  end. Supports password auth, key auth (including passphrase-protected keys),
  and an optional **ProxyJump** hop: when a jump host is configured, the tool
  dials the jump first and opens the connection to the final host through it,
  reusing the same key/passphrase for both hops automatically.

- **WireGuard:** a fully **userspace** tunnel via
  [`wireguard-go`](https://git.zx2c4.com/wireguard-go/)'s `tun/netstack`
  package, which runs on top of **gVisor's netstack**, a userspace TCP/IP stack.
  This is the key trick: there's no kernel module, no `/dev/net/tun` device, no
  system network interface, and **no root / `CAP_NET_ADMIN`** required. The
  netstack `Net` object hands back a `net.Conn` from its `DialContext`, which is
  wired into a custom `http.Transport`. The practical upshot is that only
  tcl-script-runner's own API traffic goes through the tunnel; your machine's
  routing table is never touched, so nothing else on the host is affected.

Tunnel credentials (SSH passwords, SSH key passphrases, WireGuard
private/preshared keys) are encrypted at rest the same way the API login
passwords are.

## Requirements

- Go 1.22+
- Network reachability to your target environments (directly or via the
  configured SSH/WireGuard tunnel)

## Build & run

```bash
# build
go build -o tcl-script-runner ./cmd        # use ./cmd; on Windows add .exe

# run
./tcl-script-runner --listen :8080 --data-dir .
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
# Windows / PowerShell, current session
$env:BECS_RUNNER_KEY = "your-master-passphrase"
```

```bash
# Linux / macOS
export BECS_RUNNER_KEY="your-master-passphrase"
```

Use the **same** passphrase every time. If it changes, previously saved
credentials can no longer be decrypted. If it's unset, the app still starts but
logs a warning and credential encryption/decryption will fail.

### Environments

Add environments through the UI (`/environments`). They're written to
`config.yaml` in the data directory, with secrets stored encrypted. Job results
accumulate as JSON files in `jobs/`.

## Tech stack

- **Go 1.22**, standard-library HTTP server and `html/template`
- **HTMX 2.x** for interactivity, **PicoCSS** for styling, no JS
- `golang.org/x/crypto/ssh` for SSH tunnels
- `golang.zx2c4.com/wireguard` (+ `tun/netstack`) for userspace WireGuard
- AES-256-GCM + PBKDF2 from the standard library for credential encryption

## Roadmap (not yet implemented)

- Web authentication (basic/session) for non-local use
- Selecting/running scripts other than `batch_accounting.tcl` from the UI
- Tests around the tunnel and API client paths

## License

Do not distribute.
