# Termux:Boot launcher for ffstream (local adb test phone)

`start-ffstream.sh` is a Termux:Boot script that auto-starts the
`ffstream` daemon when the **local adb test phone** boots.

It is **not** for the production host `172.29.222.3` — that host runs
ffstream from an Ubuntu chroot via `rc.local`, an entirely separate
mechanism.

## What it does

When Android boots, Termux:Boot launches every executable file in
`~/.termux/boot/` as the `com.termux` UID (`u0_a190` on this device).
This script:

1. Waits up to 50 s for `/data/local/tmp/ffstream` to appear
   (deploys can race with boot).
2. Exports `LD_LIBRARY_PATH=/data/local/tmp/ffmpeg-bin/lib` so the
   ffmpeg shared libraries resolve.
3. Sets `GOMEMLIMIT=256MiB`.
4. Execs the daemon with the E2E-test flag set (see the script
   for details). Notable defaults:
   - `-listen_control tcp+ssl:0.0.0.0:3593` — wingout's TLS client
     connects here.
   - `-mux_mode different_outputs_same_tracks` — the default
     (`forbid`) breaks wingout's runtime codec switch / output
     replacement.
   - `-f null -` — no RTMP egress at boot. wingout sets the real
     destination URL via `SetOutputURL` RPC.
   - `rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3` registered as
     a priority-10 fallback for the DJI MIMO ingest path.

5. Appends stdout/stderr to `/data/local/tmp/ffstream.log`.

## Prerequisites

- **Termux** installed (`com.termux`).
- **Termux:Boot** APK installed (`com.termux.boot`). Install from
  F-Droid; not safe to sideload from this repo.
- The Termux:Boot app launched at least once and granted
  *"Run on boot"* in Android Settings → Apps → Termux:Boot.
- `ffstream` binary deployed separately to `/data/local/tmp/ffstream`
  (not packaged here — see `wingout`'s deploy scripts).
- ffmpeg shared libs deployed to `/data/local/tmp/ffmpeg-bin/lib/`.

## Deploy

The Termux home directory is owned by `u0_a190` (the `com.termux`
UID), so a direct `adb push` from the `shell` user is denied.
Two-step deploy via `run-as`:

```sh
adb push start-ffstream.sh /data/local/tmp/start-ffstream
adb shell run-as com.termux mkdir -p /data/data/com.termux/files/home/.termux/boot
adb shell run-as com.termux cp /data/local/tmp/start-ffstream \
    /data/data/com.termux/files/home/.termux/boot/start-ffstream
adb shell run-as com.termux chmod 755 \
    /data/data/com.termux/files/home/.termux/boot/start-ffstream
```

`run-as com.termux` requires the Termux app to be debuggable. F-Droid
builds of Termux are debuggable by default; Play Store builds are
not. If `run-as` is denied, the only fallback is to copy the file
manually inside Termux (open a Termux session on the phone and
`cp /sdcard/Download/start-ffstream ~/.termux/boot/`).

## Test (without rebooting)

```sh
adb shell pkill -f /data/local/tmp/ffstream || true
adb shell run-as com.termux sh \
    /data/data/com.termux/files/home/.termux/boot/start-ffstream &
sleep 5
adb shell ps -A | grep ffstream | grep -v grep
adb shell /data/local/tmp/ffstreamctl \
    --remote-addr tcp+ssl:127.0.0.1:3593 inputs info
```

## Re-deploy after a wipe

The phone gets re-flashed periodically. After every wipe:

1. Re-install Termux + Termux:Boot from F-Droid.
2. Open Termux:Boot once and grant *"Run on boot"*.
3. Re-push `start-ffstream.sh` per the **Deploy** section above.
4. Re-push the `ffstream` binary and ffmpeg libs to
   `/data/local/tmp/`.
