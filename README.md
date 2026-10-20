# vps-fleet

**Your whole server fleet in one terminal window.**

vps-fleet is a fast, keyboard-driven terminal UI for people who run a handful of
VPS and are tired of juggling SSH tabs. See every server's health at a glance,
drill into live metrics and processes, manage services, move files and run
commands — all over plain SSH, with nothing to install on your servers.

```
  vps-fleet v0.1.0 · default            web-1 [203.0.113.10]                  2 servers · 2 online
 ─────────────────────────────────────────────────────────────────────────────────────────────────
  NAME              STATUS     CPU         MEM         DISK        LOAD   UPTIME   ALERTS
 ────────────────────────────────────────────────────────────────────────────────────────────
 ▌web-1             ● online   ████░  82%  █████  97%  █░░░░  23%  0.42   6d 4h    mem high    ▐
  db-1              ● online   █░░░░  12%  ██░░░  41%  ███░░  60%  0.05   6h 13m   reboot 12 upd
```

## Why vps-fleet?

- **Agentless.** If you can `ssh` into it, vps-fleet can manage it. No daemons, no
  exporters, no ports to open — just the tools every Linux box already has.
- **See problems before they bite.** Live CPU, memory and disk bars for every
  server, plus alerts for failed systemd units, full disks, high load, pending
  updates and required reboots.
- **Go from overview to root cause in two keys.** Open a dashboard with trends,
  disk and network I/O, and a live top-style process list — then kill the
  runaway process right there.
- **Everything you'd SSH in for, built in.** A command runner with history,
  reverse search and tab completion; a systemd service manager; and a two-pane
  file manager for uploads and downloads.
- **Made for the keyboard.** Every action is a single key, and the footer
  always shows what's available.

## Features

**Fleet overview**
- Table or card view (`d`), with groups, live filter (`/`) and sorting (`o`)
- Health status, load, uptime and alerts per server
- Profiles for separate server lists — work, home, customers (`P`, `-p name`)

**Server dashboard** (`enter`)
- CPU, memory and disk gauges with 5-minute trend lines
- Disk and network throughput, load average, OS and uptime
- A top-like process list, sortable by CPU or memory

**Top view** (`t`)
- Full-screen process list with an htop-style summary line
- Send any signal to any process (`k`), with the signal list and numbers at hand

**Tools**
- **SSH** (`s`) — run commands with persistent working directory, aliases,
  history (`↑/↓`), reverse search (`ctrl+r`) and path completion (`tab`)
- **Systemd** (`v`) — list units, start, stop and restart services
- **Files** (`f`) — local and server side by side: upload and download with a
  progress bar, view, delete, chmod/chown/chgrp, filter, and jump to any path
  with live tab completion

**Make it yours** (`c`)
- Bar colours (default, greyscale or your own gradient), default kill signal
  and default sort order

## Getting started

Requires Go 1.26+ to build.

```sh
git clone https://github.com/realitytoday/vps-fleet-tui.git
cd vps-fleet-tui
go build -o vps-fleet ./cmd/vps-fleet
./vps-fleet
```

On first start vps-fleet creates an example `~/.config/vps-fleet/config.yaml`.
Add your servers:

```yaml
servers:
  - id: web-1
    name: web-1
    host: 203.0.113.10
    user: root
    port: 22
    keypath: ~/.ssh/id_ed25519
    group: prod          # optional: groups the fleet view
```

More server lists go into `~/.config/vps-fleet/profiles/<name>.yaml` (same
format); start one with `vps-fleet -p <name>` or switch with `P`.

### Server requirements

Any Linux server reachable over SSH. The richer views use standard tools:
`top`/`ps` (procps), GNU `find`, `df`, `free` and `systemctl`. Debian and Ubuntu
work out of the box; on minimal systems like Alpine some views fall back to
simpler output.

## Security notes

- **Host keys are verified.** Servers are checked against `~/.ssh/known_hosts`
  and vps-fleet's own `~/.config/vps-fleet/known_hosts`. A server seen for the
  first time is remembered (trust on first use); a server whose key has changed
  is refused with a clear error. If you reinstalled a server on purpose, delete
  its line from the known_hosts file.
- **Prefer keys over passwords.** `password:` is supported, but it is stored in
  plain text in the config file. vps-fleet writes its config files readable by
  you only (mode 0600).
- vps-fleet runs the commands you ask for (and a few read-only ones for the
  dashboard) on your servers. Treat it with the same care as an SSH session.

## Keys

| Key | Action | Key | Action |
|---|---|---|---|
| `↑/↓` | select server | `enter` | dashboard |
| `t` | top view | `s` | SSH |
| `v` | systemd | `f` | files |
| `/` | filter | `o` | sort |
| `d` | table / cards | `P` | profiles |
| `c` | settings | `ctrl+r` | reload config |
| `esc` | back | `q` | quit |

## Build your own

The prompts, architecture notes and lessons learned behind this tool are in the blueprint repo
[vps-fleet](https://github.com/realitytoday/vps-fleet): hand them to your coding agent to build a
version tuned to your own servers.

## License

Code: [MIT](LICENSE). Documentation and prompts: [CC BY 4.0](LICENSE-DOCS.md).

## Built with

[Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss) by Charm, and
[golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh).
