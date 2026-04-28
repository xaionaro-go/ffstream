#!/data/data/com.termux/files/usr/bin/sh
#
# Termux:Boot launcher for ffstream on the local adb test phone.
# Run as Termux UID (u0_a190 on this device). Reads ffstream binary
# from /data/local/tmp/ffstream where it lives between deploys.
#
set -eu

FFSTREAM_BIN="/data/local/tmp/ffstream"
FFSTREAM_LOG="/data/local/tmp/ffstream.log"
FFMPEG_LIBS="/data/local/tmp/ffmpeg-bin/lib"

# Wait for the binary to be present (deploy may race with boot).
for i in 1 2 3 4 5 6 7 8 9 10; do
    [ -x "$FFSTREAM_BIN" ] && break
    sleep 5
done
[ -x "$FFSTREAM_BIN" ] || { echo "ffstream not found at $FFSTREAM_BIN" >&2; exit 1; }

export LD_LIBRARY_PATH="$FFMPEG_LIBS"
export GOMEMLIMIT="256MiB"

exec "$FFSTREAM_BIN" \
    -listen_control tcp+ssl:0.0.0.0:3593 \
    -listen_net_pprof 127.0.0.1:8238 \
    -retry_input_timeout_on_failure 1s \
    -mux_mode different_outputs_same_tracks \
    -hwaccel mediacodec ndk_codec=1 \
    -i 'rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3?fallback_priority=10' \
    -s 1920x1080 \
    -c:v av1_mediacodec -b:v 8000000 \
    -c:a aac -ar 48000 -ac 1 -b:a 128000 \
    -f null - \
    >> "$FFSTREAM_LOG" 2>&1 < /dev/null
