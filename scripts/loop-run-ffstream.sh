#!/bin/sh
#
# Common supervised launcher for ffstream.
#
# Single source of truth for daemon flags. Used on both:
#   - prod (172.29.222.3, ubuntu chroot)  — invoked by /etc/rc.local
#   - local adb test phone (Android)      — invoked by Termux:Boot
#
# The trigger wrappers (rc.local, termux-boot/start-ffstream) just
# exec this script. Keep this script POSIX sh.
#
# Inputs (env, with defaults below):
#   FFSTREAM_BIN        path to the ffstream binary
#   FFMPEG_LIBS         directory holding ffmpeg shared libs
#   GOMEMLIMIT          Go runtime soft memory limit
#   LD_LIBRARY_PATH     extra library search path (prepended to FFMPEG_LIBS)
#   FFSTREAM_INPUT_URL  primary -i URL (defaults to the on-device DJI proxy
#                       endpoint historically baked into this script). Set
#                       this when running on a host that does not expose the
#                       Termux MediaMTX proxy on 127.0.0.1:1935.
#
set -eu

FFSTREAM_BIN="${FFSTREAM_BIN:-/data/local/tmp/ffstream}"
FFMPEG_LIBS="${FFMPEG_LIBS:-/data/local/tmp/ffmpeg-bin/lib}"
GOMEMLIMIT="${GOMEMLIMIT:-256MiB}"
FFSTREAM_INPUT_URL="${FFSTREAM_INPUT_URL:-rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3?fallback_priority=10}"

# Wait for the binary to be present (deploy may race with boot).
i=0
while [ "$i" -lt 10 ]; do
    [ -x "$FFSTREAM_BIN" ] && break
    i=$((i + 1))
    sleep 5
done
[ -x "$FFSTREAM_BIN" ] || {
    echo "ffstream not found at $FFSTREAM_BIN" >&2
    exit 1
}

if [ -n "${LD_LIBRARY_PATH:-}" ]; then
    LD_LIBRARY_PATH="$FFMPEG_LIBS:$LD_LIBRARY_PATH"
else
    LD_LIBRARY_PATH="$FFMPEG_LIBS"
fi
export LD_LIBRARY_PATH
export GOMEMLIMIT

# No hardcoded -f flv rtmp:// destination: wingout sets the real
# output URL via the SetOutputURL RPC after gRPC connect.
#
# Supervisor loop: restart on any non-zero exit. The script's name
# starts with "loop-" — make that real instead of single-shot exec.
# `set -e` would abort on a non-zero rc here; disable it inside the
# loop so we observe rc and respawn.
set +e
while true; do
    "$FFSTREAM_BIN" \
        -listen_control tcp+ssl:0.0.0.0:3593 \
        -listen_net_pprof 127.0.0.1:8238 \
        -retry_input_timeout_on_failure 1s \
        -mux_mode different_outputs_same_tracks \
        -hwaccel mediacodec ndk_codec=1 \
        -i "$FFSTREAM_INPUT_URL" \
        -s 1920x1080 \
        -c:v av1_mediacodec -b:v 8000000 \
        -c:a aac -ar 48000 -ac 1 -b:a 128000 \
        -f null -
    rc=$?
    echo "[$(date -Iseconds)] ffstream exited rc=$rc; restarting in 2s" >&2
    sleep 2
done
