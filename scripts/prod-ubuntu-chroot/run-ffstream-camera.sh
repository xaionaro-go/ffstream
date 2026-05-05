#!/bin/bash
set -o pipefail
#
# run-ffstream-camera.sh launches one IDLE-mode ffstream daemon instance
# alongside the mediamtx-side run-ffstream.sh daemon. Keeps the shared
# low-latency input defaults, hwaccel, auto-bitrate, retry behaviour,
# scheduling/affinity, keeps mission output defaults, and
# drops only inputs / output URLs (those are added at runtime via wingout's gRPC):
#   AddInput(camera) + AddInput(mic) + SetOutputURL + SwitchOutputByProps.
#
# SwitchOutputByProps carries the per-Activate output URL and bitrate. The
# daemon also keeps a baked 1920x1920 auto-bitrate resolution default so rate
# bands are anchored to the built-in camera geometry before the first Activate
# RPC arrives.
#
# Split from the mediamtx-side script — both daemons co-exist on one phone:
#   - Listens on tcp:127.0.0.1:3594 (mediamtx-side: 3593)
#   - pprof on 0.0.0.0:8239         (mediamtx-side: 8238)
#   - Logs to /data/ubuntu/tmp/ffstream-camera.log
#
# -mux_mode different_outputs_same_tracks_split_av makes the daemon emit two
# separate publishes per Activate: one video-only RTMP connection and one
# audio-only RTMP connection, each routed by the avd builtincamera templates.
#
# Mission: see /home/streaming/go/src/github.com/xaionaro-go/mission.md

: "${FFSTREAM_CAMERA_PATH:=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin}"
export PATH="$FFSTREAM_CAMERA_PATH"

readonly FFSTREAM_CANONICAL_BIN=/data/user/0/com.termux/files/usr/bin/ffstream

fail_config() {
	echo "ffstream-camera config error: $*" >&2
	exit 78
}

require_config_var() {
	local name=$1
	local value=${!name-}
	if [ -z "$value" ]; then
		fail_config "required variable $name is empty or unset"
	fi
}

decimal_at_least() {
	local value=$1
	local minimum=$2
	value="${value#"${value%%[!0]*}"}"
	if [ -z "$value" ]; then
		value=0
	fi
	if [ "${#value}" -gt "${#minimum}" ]; then
		return 0
	fi
	if [ "${#value}" -lt "${#minimum}" ]; then
		return 1
	fi
	[[ "$value" > "$minimum" || "$value" == "$minimum" ]]
}

validate_ram_cap_as() {
	local minimum=${FFSTREAM_MIN_RAM_CAP_AS:-1099511627776}
	case "${FFSTREAM_RAM_CAP_AS:-}" in
		unlimited)
			return
			;;
		"")
			fail_config "FFSTREAM_RAM_CAP_AS is empty; use unlimited or at least $minimum bytes"
			;;
		*[!0-9]*)
			fail_config "FFSTREAM_RAM_CAP_AS must be unlimited or integer bytes >= $minimum"
			;;
	esac
	if ! decimal_at_least "$FFSTREAM_RAM_CAP_AS" "$minimum"; then
		fail_config "FFSTREAM_RAM_CAP_AS=$FFSTREAM_RAM_CAP_AS is below Termux ffstream VAS minimum $minimum"
	fi
}

validate_ffstream_binary() {
	if ! command -v "$FFSTREAM_BIN_RUNNER" >/dev/null 2>&1; then
		fail_config "FFSTREAM_BIN_RUNNER not found: $FFSTREAM_BIN_RUNNER"
	fi
	if ! "$FFSTREAM_BIN_RUNNER" test -x "$FFSTREAM_CANONICAL_BIN"; then
		fail_config "canonical ffstream binary is missing or not executable in the Termux namespace: $FFSTREAM_CANONICAL_BIN"
	fi
}

: "${FFSTREAM_CAMERA_STREAMING_ENV_FILE:=/etc/streaming.env}"
if [ ! -r "$FFSTREAM_CAMERA_STREAMING_ENV_FILE" ]; then
	fail_config "missing or unreadable config file: $FFSTREAM_CAMERA_STREAMING_ENV_FILE"
fi
if ! bash -n "$FFSTREAM_CAMERA_STREAMING_ENV_FILE"; then
	fail_config "invalid config syntax: $FFSTREAM_CAMERA_STREAMING_ENV_FILE"
fi
if ! . "$FFSTREAM_CAMERA_STREAMING_ENV_FILE"; then
	fail_config "unable to source config file: $FFSTREAM_CAMERA_STREAMING_ENV_FILE"
fi
require_config_var FFSTREAM_LOG_LEVEL
require_config_var ACODEC

: "${FFSTREAM_BIN_RUNNER:=termux-root}"
: "${FFSTREAM_RAM_CAP_AS:=unlimited}"
: "${FFSTREAM_END_MARKER_FILE:=/data/ubuntu/tmp/ffstream-camera.intentional-end}"
: "${FFSTREAM_END_MARKER_FILE_CHROOT:=/android/data/ubuntu/tmp/ffstream-camera.intentional-end}"
: "${FFSTREAM_CAMERA_LOG_FILE:=/data/ubuntu/tmp/ffstream-camera.log}"
validate_ram_cap_as
validate_ffstream_binary
export FFSTREAM_END_MARKER_FILE

# Mic-fix daemon (PulseAudio mic plumbing). Backgrounded like the mediamtx side.
fix-mic &

export PULSE_SERVER=127.0.0.1
export GOMEMLIMIT="${FFSTREAM_GOMEMLIMIT:-15GiB}"

# Tell the OOM killer to prefer this process if memory pressure hits;
# wingout / Android system processes must outlive us.
echo 1000 > /proc/self/oom_score_adj

# All flags below are GLOBAL defaults (apply to every later AddInput RPC's
# AVFormatContext) — preserved verbatim from run-ffstream.sh except for
# the per-input/per-output flags that wingout drives via gRPC at
# user-tap-Activate time. A clean End returns status 0 to the outer supervisor,
# which relaunches a fresh idle daemon for the next Activate. Dropped relative
# to the mediamtx-side launcher:
#   -i <url>                    (replaced by AddInput RPC)
#   -fallback_priority <n>      (per-input; AddInput carries Priority)
#   -itsoffset <ts>             (per-input; AddInput as needed)
#   -video_size <WxH>           (per-input; AddInput's CustomOptions)
#   -auto_bitrate_resolution    (kept as built-in camera default below)
#   -b:v/-bufsize/-g/-r (per-output bitrate/framerate tuning)
#   -f <fmt> <DST_URL>          (per-output; SetOutputURL + SwitchOutputByProps)
#
# hardened_malloc (preloaded inside termux via the canonical ffstream binary)
# reserves ~1TB of virtual address space at startup. The Ubuntu chroot runs
# Termux binaries through termux-root, so paths observed by ffstream are in the
# Android namespace; the marker check uses the chroot-visible /android mirror.
# The binary argument stays canonical.
# The inherited RLIMIT_AS is too small without explicit lifting — without
# prlimit, the binary aborts with "fatal allocator error: failed to reserve
# allocator state" at process entry. Mirrors the mediamtx-side run-ffstream.sh.
if [ -e "$FFSTREAM_END_MARKER_FILE_CHROOT" ]; then
	if ! rm -f -- "$FFSTREAM_END_MARKER_FILE_CHROOT"; then
		echo "unable to remove stale ffstream-camera End marker: $FFSTREAM_END_MARKER_FILE_CHROOT" >&2
		exit 74
	fi
fi
prlimit --as="$FFSTREAM_RAM_CAP_AS" -- \
	nice -n -15 \
	taskset -c 6-8 \
	"$FFSTREAM_BIN_RUNNER" env \
			FFSTREAM_END_MARKER_FILE="$FFSTREAM_END_MARKER_FILE" \
		"$FFSTREAM_CANONICAL_BIN" \
			-v "$FFSTREAM_LOG_LEVEL" \
			-retry_input_timeout_on_failure 1s \
			-retry_output_timeout_on_failure 0 \
			-exit_on_last_input_removed false \
			-quiet_on_open_failure true \
			-hwaccel mediacodec \
			-c:v av1_mediacodec \
			-af aresample=48000 \
			-ar 48000 \
			-ac 1 \
			-sample_fmt fltp \
			-c:a "$ACODEC" \
			-auto_bitrate true \
			-auto_bitrate_resolution 1920x1920 \
		-auto_bitrate_auto_bypass false \
		-mux_mode different_outputs_same_tracks_split_av \
		-listen_control tcp:127.0.0.1:3594 \
		-listen_net_pprof 0.0.0.0:8239 \
		-fflags nobuffer \
		-flags low_delay \
		-rtbufsize 5M \
		-probesize 32768 \
		-analyzeduration 200000 \
		2>&1 | tee "$FFSTREAM_CAMERA_LOG_FILE"
status=$?
if [ -f "$FFSTREAM_END_MARKER_FILE_CHROOT" ]; then
	if ! rm -f -- "$FFSTREAM_END_MARKER_FILE_CHROOT"; then
		echo "unable to remove consumed ffstream-camera End marker: $FFSTREAM_END_MARKER_FILE_CHROOT" >&2
		exit 74
	fi
	exit 0
fi
if [ "$status" -eq 0 ]; then
	echo "ffstream-camera exited with status 0 without an End marker; treating as restartable failure" >&2
	exit 70
fi
exit "$status"
