# prod-ubuntu-chroot — versioned launcher scripts

Production launcher chain for ffstream running inside the Ubuntu chroot on the prod phone (`root@172.29.222.3`, formerly `172.29.221.28`). Source-of-truth lives here; prod is a checkout target.

## Why this directory exists

Critic B (ECI iter-1) finding **F7**: `run-ffstream.sh` was hand-edited on the device with no version control. `loop-run-ffstream.sh` had a versioned cousin under `scripts/loop-run-ffstream.sh` for the test-phone path, but the prod chroot variant diverged. Result: drift, no review trail, lost edits.

This directory is the SSOT for everything that runs in `/usr/local/bin/` and `/etc/` of the prod chroot. Hand edits on the device are bugs to be reconciled here first.

## Files

| File | Deploys to | Purpose |
|---|---|---|
| `run-ffstream.sh` | `/usr/local/bin/run-ffstream.sh` | Per-launch mediamtx-side wrapper; sources env, validates caps, runs Termux `ffstream` |
| `loop-run-ffstream.sh` | `/usr/local/bin/loop-run-ffstream.sh` | Supervisor loop (`while sleep 0.1; do run-ffstream.sh; done`) |
| `run-ffstream-camera.sh` | `/usr/local/bin/run-ffstream-camera.sh` | Per-launch wrapper for the idle ffstream-camera daemon on port 3594 |
| `loop-run-ffstream-camera.sh` | `/usr/local/bin/loop-run-ffstream-camera.sh` | rc.local-owned camera supervisor |
| `streaming.env.template` | `/etc/streaming.env` (only if absent) | Env defaults; sourced by both per-launch wrappers |
| `mediamtx.yml` | `/etc/mediamtx/mediamtx.yml` | mediamtx config |
| `rc.local.fragment` | append to `/etc/rc.local` (manual) | rc.local launcher line + cleanup |
| `deploy.sh` | n/a | Push files to prod via scp |

## Path boundaries

Deploy/edit paths are inside the Ubuntu chroot: `/usr/local/bin/`,
`/etc/streaming.env`, `/etc/mediamtx/mediamtx.yml`, and `/etc/rc.local`.

Runtime-only log and marker paths live under Android data storage and are
created by the running launchers, not by `deploy.sh`:

- `/data/ubuntu/tmp/ffstream.log`
- `/data/ubuntu/tmp/ffstream-camera.log`
- `/data/ubuntu/tmp/loop-run-ffstream.log`
- `/data/ubuntu/tmp/loop-run-ffstream-camera.log`
- `/data/ubuntu/tmp/ffstream-camera.intentional-end`
- `/android/data/ubuntu/tmp/ffstream-camera.intentional-end` (chroot-visible
  mirror checked before and after each camera daemon run)

## Env-var contract

Set in `/etc/streaming.env`; read by both per-launch wrappers:

| Var | Default | Effect |
|---|---|---|
| `FFSTREAM_BIN` | `/data/user/0/com.termux/files/usr/bin/ffstream` | Canonical Termux ffstream binary path passed through `termux-root` |
| `FFSTREAM_BIN_RUNNER` | `termux-root` | Runner used by the Ubuntu chroot wrappers to execute the Termux binary |
| `FFSTREAM_RAM_CAP_AS` | `unlimited` | `prlimit --as=` value (RLIMIT_AS / virtual address space); numeric values below `1099511627776` fail setup with status 78 |
| `FFSTREAM_GOMEMLIMIT` | `15GiB` | Exported to ffstream; Go runtime soft memory target |

`FFSTREAM_RAM_CAP_AS` defaults to `unlimited` because the Termux ffstream
process reserves roughly 1 TiB of virtual address space at startup. A too-low
numeric cap is a terminal setup error; supervisors stop on status 78 instead
of looping.

Header rule: `oom_score_adj=1000` is set unconditionally (kill-first OOM target). `nice -n -15` and `taskset -c 6-8` are also unconditional.

## Deploy

```sh
./deploy.sh                       # default: root@172.29.222.3
./deploy.sh root@<other-host>     # override target
```

`deploy.sh` is non-destructive for `/etc/streaming.env` — it stages a copy at `/tmp/streaming.env.staged` and only installs if the prod copy is absent. Operator edits to env are preserved; reconcile by hand and update the template here.

The deploy includes both rc.local-owned supervisors:
`/usr/local/bin/loop-run-ffstream.sh` for the mediamtx-side daemon and
`/usr/local/bin/loop-run-ffstream-camera.sh` for the built-in camera daemon.
The app configures and Ends only the camera daemon through gRPC; rc.local owns
starting both loops.

## Applying after deploy

`deploy.sh` does not restart the supervisors. On prod, reboot after deploy to
apply rc.local startup changes.

## Hard constraints

- Only paths inside the Ubuntu chroot. Never `/android`, `/data`, `/system`, `/apex`.
- Never `adb root`, never restart `adbd`.
- All edits land here first, then `deploy.sh`. Hand edits on prod are bugs.
