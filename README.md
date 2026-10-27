# berth

A live Docker dashboard for the terminal. Everything you reach for `docker ps`, `docker logs` and `docker stats` to do, in one screen that updates as it happens — with history charts, alerts and the actions to fix what it shows you.

## What it does

- **Containers at a glance** — state, health, CPU, memory, network and uptime for every container, sortable and filterable.
- **Live history** — CPU, memory and network charts over 1 m · 5 m · 15 m · 1 h, with hover readouts.
- **Alerts** — crashes, out-of-memory kills, failing health checks and restart loops, flagged the moment they happen; jump straight to the logs at that moment.
- **Logs** — follow, search (regex), wrap and timestamp toggles.
- **Actions** — start, stop, restart, pause, kill, remove, exec a shell, attach, open published ports in the browser.
- **Compose** — up, down, pull, build, restart and logs for a whole project, or recreate one service.
- **Images, volumes, networks, events** — browse, inspect (including raw JSON), remove and prune.
- **Environment** — env vars per container, with passwords, tokens and keys masked until you ask.
- **Doctor** — run a project's own health checks (`.berth/doctor.yaml`) and apply their fixes.
- **Command palette, custom commands, full mouse support.**

## Install

### Download the binary

Linux / WSL — a single static binary, no dependencies:

```bash
mkdir -p ~/.local/bin
cp berth ~/.local/bin/berth
chmod +x ~/.local/bin/berth
```

Open a new terminal and run `berth`.

### Build from source

Needs Go 1.27 or newer.

```bash
git clone https://github.com/krish-vd/berth.git
cd berth
go build -o ~/.local/bin/berth .
```

## Requirements

- Docker reachable from where you run berth. Check with `docker ps` (without `sudo`). If that needs `sudo`, run `sudo usermod -aG docker $USER` and open a new terminal.
- A terminal at least 100 × 30 with true colour. Windows Terminal, iTerm2, WezTerm, Kitty and most Linux terminals work.

## Usage

```bash
berth                         # local Docker ($DOCKER_HOST, or the default socket)
berth --host ssh://me@server  # a remote Docker host over SSH
berth --host wsl://           # from Windows: Docker running inside WSL
berth doctor                  # run the project's doctor checks and exit
```

| Flag | |
|---|---|
| `--host` | Docker endpoint: `unix:///path`, `tcp://host:port`, `ssh://user@host`, `wsl://[distro]` |
| `--doctor` | doctor checks file (default: `.berth/doctor.yaml` in this folder or a parent) |
| `--tz` | timezone for displayed times, e.g. `Asia/Kolkata` |
| `--version` | print the version |

## Keys

Press `?` inside berth for the full list.

| Key | |
|---|---|
| `1`–`5` | containers · images · volumes · networks · events |
| `6` | doctor |
| `ctrl+p` or `:` | command palette |
| `s` `r` `p` `d` `K` | start/stop · restart · pause · remove · kill |
| `E` / `a` | exec a shell / attach |
| `w` | open the container's port in the browser |
| `C` | compose menu |
| `c` | custom commands |
| `!` | alerts |
| `[` `]` | previous / next detail tab |
| `t` `T` | chart window |
| `/` | filter the list, or search logs |
| `f` `z` `i` | follow · wrap · timestamps in logs |
| `v` | reveal masked env secrets |
| `q` | quit |

## Configuration

Custom commands live in `~/.config/berth/config.yaml` (`%AppData%\berth\config.yaml` on Windows) and are re-read each time you open the menu.

## Doctor

Press `6` (or run `berth doctor`) for a health check of the whole stack. Built-in checks work on any Docker host with nothing to configure — they find what to check from the running containers:

- **Services** — crashed or OOM-killed containers, restart loops, failing health checks, a process that died while its container still says *Up*, services stuck waiting for a debugger (`debugpy --wait-for-client`).
- **Config** — settings that must match across a compose project's services (`*JWT*`, `APP_ENV`, `ENVIRONMENT` …); secrets are compared by fingerprint, never shown.
- **Data** — Postgres accepting connections, idle replication slots holding WAL, Redis answering, Elasticsearch cluster health, disk space.
- **Migrations** — Django migrations not applied on any configured database, and model changes with no migration.
- **Workers** — Celery worker and beat running, and workers still running code older than the files on disk.

Checks with a fix offer it on `f`, after you confirm.

### Your own checks

A project can replace the built-in set with a `.berth/doctor.yaml` (found in the folder you run berth from, or any parent; or pass `--doctor`). Each check is a shell script run on the Docker host: exit `0` = pass, `2` = warning, anything else = fail; the first line of output is the summary.

```yaml
title: My stack
checks:
  - group: Data
    name: Queue not backed up
    run: |
      n=$(docker exec redis redis-cli llen jobs)
      [ "$n" -lt 100 ] && { echo "$n jobs waiting"; exit 0; }
      echo "$n jobs waiting"; exit 2
    fixHint: Scale the workers up.
```
