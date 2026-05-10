# Auto-start launcher for ffstream (test phone + prod)

Both the **local adb test phone** (stock Android, Termux + Termux:Boot)
and the **prod phone** (`172.29.222.3`, ubuntu chroot, `rc.local`) use
the *same* supervised launcher. The trigger is the only thing that
differs per host.

## Layout

```
scripts/
  loop-run-ffstream.sh        # common supervised launcher (POSIX sh)
  rc.local.snippet            # prod /etc/rc.local launcher additions
  termux-boot/
    start-ffstream            # Termux:Boot trigger for ffstream (test phone)
    start-ubuntu              # Termux:Boot trigger for ubuntu-chroot (prod phone)
    README.md                 # this file
```

`loop-run-ffstream.sh` is the **single source of truth** for the
ffstream daemon flag set. Both trigger wrappers just exec it:

- prod (`/etc/rc.local`) → `/usr/local/bin/loop-run-ffstream.sh`
- test phone (Termux:Boot) → `/data/local/tmp/loop-run-ffstream.sh`

The Termux:Boot triggers themselves are split per host:

- **Test phone**: `start-ffstream` fires `loop-run-ffstream.sh`
  directly under Android (no chroot). ffstream runs natively.
- **Prod phone**: `start-ubuntu` fires `/data/ubuntu/start.sh`
  under root (`su 0 sh ...`), which bind-mounts the ubuntu chroot
  and execs `/etc/rc.local` inside it. The chroot's `rc.local` is
  what launches `loop-run-ffstream.sh` on prod. Without this
  trigger an operator has to re-run `start.sh` by hand after every
  reboot.

## What the launcher does

1. Waits up to ~50 s for the `ffstream` binary to appear at
   `${FFSTREAM_BIN:-/data/local/tmp/ffstream}` (deploys can race
   with boot).
2. Exports `LD_LIBRARY_PATH` (prepends `${FFMPEG_LIBS:-/data/local/tmp/ffmpeg-bin/lib}`).
3. Sets `GOMEMLIMIT` (default `256MiB`).
4. Execs the daemon with the agreed flag set. Notable flags:
   - `-listen_control tcp+ssl:0.0.0.0:3593` — wingout's TLS client
     connects here.
   - `-mux_mode different_outputs_same_tracks` — the default
     (`forbid`) breaks wingout's runtime codec switch / output
     replacement.
   - `-f null -` — no RTMP egress at boot. wingout sets the real
     destination URL via the `SetOutputURL` RPC after gRPC connect.
   - `rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3` registered as
     a priority-10 fallback for the DJI MIMO ingest path.

## Test phone deploy

Push the common launcher and the Termux:Boot trigger:

```sh
# 1. Common launcher (world-readable + exec).
adb push scripts/loop-run-ffstream.sh /data/local/tmp/loop-run-ffstream.sh
adb shell chmod 755 /data/local/tmp/loop-run-ffstream.sh

# 2. Termux:Boot trigger. Termux home is owned by u0_a190; staged
#    via /data/local/tmp + run-as because adb shell can't write
#    into the Termux home directly.
adb push scripts/termux-boot/start-ffstream /data/local/tmp/start-ffstream
adb shell run-as com.termux mkdir -p /data/data/com.termux/files/home/.termux/boot
adb shell run-as com.termux cp /data/local/tmp/start-ffstream \
    /data/data/com.termux/files/home/.termux/boot/start-ffstream
adb shell run-as com.termux chmod 755 \
    /data/data/com.termux/files/home/.termux/boot/start-ffstream
```

`run-as com.termux` requires the Termux app to be debuggable.
F-Droid builds of Termux are debuggable by default; Play Store
builds are not. If `run-as` is denied, fall back to copying the
file inside a Termux session (`cp /sdcard/Download/start-ffstream
~/.termux/boot/`).

### Prerequisites (test phone)

- **Termux** installed (`com.termux`).
- **Termux:Boot** APK installed (`com.termux.boot`). Install from
  F-Droid; not safe to sideload from this repo.
- The Termux:Boot app launched at least once and granted
  *"Run on boot"* in Android Settings → Apps → Termux:Boot.
- `ffstream` binary deployed to `/data/local/tmp/ffstream`
  (not packaged here — see `wingout`'s deploy scripts).
- ffmpeg shared libs deployed to `/data/local/tmp/ffmpeg-bin/lib/`.

### Smoke test (no reboot)

```sh
adb shell pkill -f /data/local/tmp/ffstream || true
adb shell sh /data/local/tmp/loop-run-ffstream.sh &
sleep 5
adb shell ps -A | grep ffstream | grep -v grep
adb shell /data/local/tmp/ffstreamctl \
    --remote-addr tcp+ssl:127.0.0.1:3593 inputs info
```

### Re-deploy after a wipe

The phone gets re-flashed periodically. After every wipe:

1. Re-install Termux + Termux:Boot from F-Droid.
2. Open Termux:Boot once and grant *"Run on boot"*.
3. Re-push `loop-run-ffstream.sh` and `start-ffstream` per the
   **Test phone deploy** section above.
4. Re-push the `ffstream` binary and ffmpeg libs to
   `/data/local/tmp/`.

## Prod phone deploy (Termux:Boot trigger for ubuntu-chroot)

The prod phone runs ffstream inside a ubuntu chroot. After every
Android reboot the chroot must be re-mounted and `rc.local` re-fired
inside it. Without auto-start, an operator has to ssh in and run
`/data/ubuntu/start.sh` by hand after every boot — which the prod
phone has been doing operationally up to now.

`scripts/termux-boot/start-ubuntu` is the Termux:Boot trigger that
auto-runs `/data/ubuntu/start.sh` under root after Android boot
finishes.

### Scope and verified-on caveat

The script's default `SU=/system/xbin/su` was empirically verified on
the **akita-userdebug Android 16 test phone** only. Modern Android
images vary on `su` location:

- Magisk installs typically → `/system/bin/su`
- userdebug builds → `/system/xbin/su`
- Some custom ROMs → `/sbin/su` or other paths

**Before deploying on the prod phone**, verify the actual `su` path:

```sh
adb shell which su
adb shell ls -l /system/xbin/su /system/bin/su /sbin/su 2>/dev/null
```

If the prod-phone path differs from `/system/xbin/su`, either:

- Edit `scripts/termux-boot/start-ubuntu` in place and update the
  `SU=...` default, or
- Stage a Termux:Boot prelude that exports `SU=<actual-path>` before
  Termux:Boot fires `start-ubuntu` (Termux:Boot runs scripts in
  alphabetical order; a `00-env` prelude file works).

### Deploy commands

Same `adb push` + `run-as com.termux` pattern as the test-phone
deploy above:

```sh
# 1. Verify the prod-phone su path first (see "Scope" above).
adb shell which su

# 2. Termux:Boot trigger. Termux home is owned by u0_a190; staged via
#    /data/local/tmp + run-as because adb shell can't write into the
#    Termux home directly.
adb push scripts/termux-boot/start-ubuntu /data/local/tmp/start-ubuntu
adb shell run-as com.termux mkdir -p /data/data/com.termux/files/home/.termux/boot
adb shell run-as com.termux cp /data/local/tmp/start-ubuntu \
    /data/data/com.termux/files/home/.termux/boot/start-ubuntu
adb shell run-as com.termux chmod 755 \
    /data/data/com.termux/files/home/.termux/boot/start-ubuntu

# 3. Verify the file landed with the right perms inside Termux home.
adb shell run-as com.termux ls -la \
    /data/data/com.termux/files/home/.termux/boot/start-ubuntu
```

### Log path (read via run-as or su)

The default `START_UBUNTU_LOG` path is
`/data/data/com.termux/files/home/start-ubuntu-boot.log` — i.e.
`$HOME/start-ubuntu-boot.log` from Termux:Boot's perspective.

This is **not** under `/data/local/tmp/`: when Termux:Boot fires the
script the SELinux domain is `u:r:untrusted_app_*` which cannot
even *search* `/data/local/tmp/` (label `shell_test_data_file`),
so an `exec` redirect to that directory aborts the script silently.
The Termux home dir is the only domain-writable choice for the
production boot path.

To read the log:

```sh
# Option A: run-as (works on debuggable Termux — F-Droid default).
adb shell run-as com.termux cat \
    /data/data/com.termux/files/home/start-ubuntu-boot.log

# Option B: su (works on userdebug devices; non-debuggable Termux).
adb shell su 0 cat \
    /data/data/com.termux/files/home/start-ubuntu-boot.log
```

### Smoke test (no reboot)

`run-as` lets you exec the trigger as the Termux user without
rebooting. Useful for catching trigger-side regressions before a
real reboot test:

```sh
adb shell run-as com.termux \
    /data/data/com.termux/files/home/.termux/boot/start-ubuntu
adb shell run-as com.termux cat \
    /data/data/com.termux/files/home/start-ubuntu-boot.log
adb shell cat /proc/mounts | grep ubuntu
```

If `run-as` is denied (non-debuggable Termux), invoke the trigger
via `su` and override the log path to `/data/local/tmp/` — the
shell SELinux domain has the right label there, useful purely for
the smoke-test convenience:

```sh
adb shell su 0 \
    sh -c 'START_UBUNTU_LOG=/data/local/tmp/start-ubuntu-smoke.log \
        /data/data/com.termux/files/home/.termux/boot/start-ubuntu'
adb shell cat /data/local/tmp/start-ubuntu-smoke.log
```

The log should show the `[timestamp] start-ubuntu: invoked` +
`exec ... /data/ubuntu/start.sh` lines, and `/proc/mounts` should
list the chroot bind mounts.

### Reboot test

```sh
adb reboot
# wait for boot complete
adb wait-for-device
adb shell getprop sys.boot_completed   # → 1 when ready
sleep 30                                # let Termux:Boot fire + start.sh settle

# Read the log from Termux home (NOT /data/local/tmp/ — see "Log path"
# above for the SELinux rationale).
adb shell su 0 cat \
    /data/data/com.termux/files/home/start-ubuntu-boot.log

adb shell cat /proc/mounts | grep ubuntu
adb shell pidof ffstream                # → non-empty when chroot rc.local fired
```

### Prerequisites (prod phone)

- **Termux** + **Termux:Boot** installed and granted *"Run on boot"*
  (same as the test phone — see "Prerequisites (test phone)" above).
- The ubuntu chroot already deployed at `/data/ubuntu/` with a
  working `start.sh` (operator-owned; not vendored from this repo).
- A working `su` binary that accepts `su 0 sh <script>` form.

## Prod deploy (deferred)

Deferred until prod cutover authorization. The Termux:Boot trigger
above is independent of this section: it only ensures the chroot is
re-mounted on boot, *not* that ffstream runs on the prod host. The
chroot's existing `/etc/rc.local` already handles ffstream
supervision today.

When prod cutover is authorized (replacing the chroot's existing
launcher with this repo's vendored one):

1. Copy `scripts/loop-run-ffstream.sh` to
   `/usr/local/bin/loop-run-ffstream.sh` on the prod host
   (`172.29.222.3`, inside the ubuntu chroot). `chmod 755`.
2. Append the contents of `scripts/rc.local.snippet` to
   `/etc/rc.local` (before `exit 0`) so rc.local starts both the mediamtx
   ffstream supervisor and the camera ffstream supervisor.
3. The flag set is the *same* one the test phone uses — change it
   in `loop-run-ffstream.sh`, not in the trigger.
