# ctop

A top-like terminal UI for Docker, in the spirit of htop/btop, with the
container workflow of k9s: live stats, compose-aware grouping, logs, shell,
attach, and start/stop/delete for containers, images, networks and volumes.
It also works against remote daemons over ssh.

```
 ctop  ssh://node · node · docker 29.9.3 · 8 cpu · 31.2G                         12:03:44
 CPU ██▍················  12.1%   MEM █████··············  7.9G/31.2G    ● 14 running  ○ 3 stopped
  1 Containers   2 Images   3 Networks   4 Volumes   5 System            all · grouped · 21 rows
 NAME▲                 STATE         CPU          MEM          NET ↓/↑   IMAGE              STATUS
 ▾ immich              ■ 4/4 up         Σ 31.0%      Σ 2.1G    1.2M/80K  /srv/immich
 ├ server-1            ● healthy     █▌···  29.8% █····   1.4G   1.1M/72K  ghcr.io/immich…    up 3d
 ├ redis-1             ● running     ·····   0.4% ·····  12.0M    2.1K/1K  redis:7           up 3d
 └ db-1                ● running     ▏····   0.8% ·····   690M   80K/7.2K  postgres:16       up 3d
 traefik               ● running     ·····   0.2% ·····  40.1M  14K/120K   traefik:v3        up 9d
```

## Install

```sh
go install github.com/Arctize/ctop@latest   # or: go build -o ctop .
```

Requires Go 1.26+. The docker CLI does not need to be installed locally, but
it must be on the remote host when you use ssh.

## Usage

```sh
ctop                   # local daemon (DOCKER_HOST, DOCKER_CONTEXT or current docker context)
ctop -H node           # ssh://node: docker on 'node' via ssh, honoring ~/.ssh/config
ctop -H me@box:2222    # ssh with user and port
ctop -H tcp://10.0.0.5:2375
ctop -c mycontext      # a docker context
```

A host without a scheme is treated as an ssh destination. For ssh, ctop runs
`ssh <host> docker system dial-stdio`, just like the docker CLI does. It
connects before entering the TUI, so password, passphrase and host-key
prompts work normally. Every connection after that shares one ssh
ControlMaster (`/tmp/ctop-ssh-%C`, kept for 60s after the last use), so
polling stays cheap. Pass `--no-ssh-mux` to turn this off. Without mux,
ctop needs key or agent auth, because later connections can't prompt.

| Flag | |
|---|---|
| `-H, --host` | docker host (`name` → `ssh://name`, `ssh://`, `tcp://`, `unix://`) |
| `-c, --context` | docker context |
| `-i, --interval` | refresh interval (default `2s`) |
| `-r, --running` | start with stopped containers hidden |
| `--no-group` | start without compose grouping |
| `--tail` | log lines to load when opening logs (default 1000) |
| `--no-ssh-mux` | don't share an ssh ControlMaster |
| `--no-mouse` | disable mouse support |

## Keys

**Global:** `1`–`5` / `tab` / `←→` switch view · `↑↓ jk pgup pgdn home end` move ·
`/` filter (space-separated terms, all must match) · `esc` clear / back ·
`<` `>` sort column · `i` invert sort · `ctrl+r` refresh · `?` help · `q` quit

**Containers**

| Key | Action |
|---|---|
| `enter` | inspect container (on a project header: fold or unfold) |
| `d` | inspect (on a project header: project overview) |
| `l` | logs (on a project header: merged, time-ordered logs of all services) |
| `s` | shell (`bash`, then `ash`, then `sh`) |
| `a` | attach (detach with `ctrl-p ctrl-q`, or `ctrl-c` for non-TTY containers) |
| `u` / `x` / `r` | start / stop / restart (on a project header: the whole project) |
| `p` | pause / unpause |
| `ctrl+k` | kill (asks first) |
| `ctrl+d`, `D` | remove, forced (asks first) |
| `X` | prune stopped containers |
| `.` | show or hide stopped containers |
| `g` | toggle compose grouping |
| `space` / `z` | fold project / fold all projects |
| `P` `M` `N` `T` | sort by cpu / mem / name / age |

**Images, networks, volumes:** `enter` inspect · `ctrl+d` remove · `F` force-remove
an image · `X` prune

**Logs:** `f` follow · `w` wrap · `t` timestamps · `/` filter · `←→` pan ·
`g`/`G` top or bottom · `c` clear

**Inspect:** `/` search · `n`/`N` next or previous match · `←→` pan

## Notes

- CPU % follows `docker stats`: 100% means one core. The header meter is
  relative to all host CPUs. Memory excludes page cache, as `docker stats` does.
- Shell and attach use the Engine API directly (no local docker CLI needed).
  They work over ssh too.
- Volume sizes come from `docker system df` and load when you open the
  Volumes tab, because computing them can be slow on big hosts.
