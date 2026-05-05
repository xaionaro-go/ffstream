# prod-ubuntu-chroot — versioned launcher scripts

Production launcher chain for ffstream running inside the Ubuntu chroot on the prod phone (`root@172.29.222.3`, formerly `172.29.221.28`). Source-of-truth lives here; prod is a checkout target.

## Why this directory exists

Critic B (ECI iter-1) finding **F7**: `run-ffstream.sh` was hand-edited on the device with no version control. `loop-run-ffstream.sh` had a versioned cousin under `scripts/loop-run-ffstream.sh` for the test-phone path, but the prod chroot variant diverged. Result: drift, no review trail, lost edits.

This directory is the SSOT for everything that runs in `/usr/local/bin/` and `/etc/` of the prod chroot. Hand edits on the device are bugs to be reconciled here first.

## Files

| File | Deploys to | Purpose |
|---|---|---|
| `run-ffstream.sh` | `/usr/local/bin/run-ffstream.sh` | Per-launch wrapper; sources env, applies caps, execs `ffstream` |
| `loop-run-ffstream.sh` | `/usr/local/bin/loop-run-ffstream.sh` | Supervisor loop (`while sleep 0.1; do run-ffstream.sh; done`) |
| `run-ffstream-camera.sh` | `/usr/local/bin/run-ffstream-camera.sh` | Per-launch wrapper for the idle ffstream-camera daemon on port 3594 |
| `loop-run-ffstream-camera.sh` | `/usr/local/bin/loop-run-ffstream-camera.sh` | Supervisor used by Wing Out's Android restart hook |
| `streaming.env.template` | `/etc/streaming.env` (only if absent) | Env defaults; sourced by `run-ffstream.sh` |
| `mediamtx.yml` | `/etc/mediamtx/mediamtx.yml` | mediamtx config |
| `rc.local.fragment` | append to `/etc/rc.local` (manual) | rc.local launcher line + cleanup |
| `deploy.sh` | n/a | Push files to prod via scp |

## Env-var contract (RAM caps — gate retry 2)

Set in `/etc/streaming.env`; read by `run-ffstream.sh`:

| Var | Default | Effect |
|---|---|---|
| `FFSTREAM_RAM_CAP_AS` | `21474836480` (20 GiB) | `prlimit --as=` value (RLIMIT_AS / virtual address space) |
| `FFSTREAM_GOMEMLIMIT` | `15GiB` | Exported to ffstream; Go runtime soft memory target |

Header rule: `oom_score_adj=1000` is set unconditionally (kill-first OOM target). `nice -n -15` and `taskset -c 6-8` are also unconditional.

## Deploy

```sh
./deploy.sh                       # default: root@172.29.222.3
./deploy.sh root@<other-host>     # override target
```

`deploy.sh` is non-destructive for `/etc/streaming.env` — it stages a copy at `/tmp/streaming.env.staged` and only installs if the prod copy is absent. Operator edits to env are preserved; reconcile by hand and update the template here.

The deploy includes `/usr/local/bin/run-ffstream-camera.sh` and
`/usr/local/bin/loop-run-ffstream-camera.sh`; `platform_android.cpp` invokes
the latter from Wing Out's built-in camera restart hook.

## Triggering respawn after deploy

`deploy.sh` does not restart ffstream. Trigger explicitly:

```sh
ssh root@172.29.222.3 'kill $(pidof ffstream)'
# loop-run-ffstream.sh respawns within ~100ms
```

## Hard constraints

- Only paths inside the Ubuntu chroot. Never `/android`, `/data`, `/system`, `/apex`.
- Never `adb root`, never restart `adbd`.
- All edits land here first, then `deploy.sh`. Hand edits on prod are bugs.
