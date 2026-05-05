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
    start-ffstream            # Termux:Boot trigger (thin wrapper)
    README.md                 # this file
```

`loop-run-ffstream.sh` is the **single source of truth** for the
ffstream daemon flag set. Both trigger wrappers just exec it:

- prod (`/etc/rc.local`) → `/usr/local/bin/loop-run-ffstream.sh`
- test phone (Termux:Boot) → `/data/local/tmp/loop-run-ffstream.sh`

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

## Prod deploy (deferred)

Deferred until prod cutover authorization. When authorized:

1. Copy `scripts/loop-run-ffstream.sh` to
   `/usr/local/bin/loop-run-ffstream.sh` on the prod host
   (`172.29.222.3`, inside the ubuntu chroot). `chmod 755`.
2. Append the contents of `scripts/rc.local.snippet` to
   `/etc/rc.local` (before `exit 0`) so rc.local starts both the mediamtx
   ffstream supervisor and the camera ffstream supervisor.
3. The flag set is the *same* one the test phone uses — change it
   in `loop-run-ffstream.sh`, not in the trigger.
