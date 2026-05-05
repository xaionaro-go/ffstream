#!/bin/bash
set -o pipefail

: "${FFSTREAM_PATH:=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin}"
export PATH="$FFSTREAM_PATH"

fail_config() {
	echo "ffstream config error: $*" >&2
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
	if ! "$FFSTREAM_BIN_RUNNER" test -x "$FFSTREAM_BIN"; then
		fail_config "FFSTREAM_BIN is missing or not executable in the Termux namespace: $FFSTREAM_BIN"
	fi
}

validate_video_codec() {
	if [ "$VCODEC" != "av1_mediacodec" ]; then
		fail_config "VCODEC must be av1_mediacodec for the mediamtx-side daemon; got: $VCODEC"
	fi
}

: "${FFSTREAM_STREAMING_ENV_FILE:=/etc/streaming.env}"
if [ ! -r "$FFSTREAM_STREAMING_ENV_FILE" ]; then
	fail_config "missing or unreadable config file: $FFSTREAM_STREAMING_ENV_FILE"
fi
if ! bash -n "$FFSTREAM_STREAMING_ENV_FILE"; then
	fail_config "invalid config syntax: $FFSTREAM_STREAMING_ENV_FILE"
fi
if ! . "$FFSTREAM_STREAMING_ENV_FILE"; then
	fail_config "unable to source config file: $FFSTREAM_STREAMING_ENV_FILE"
fi
require_config_var WIDTH
require_config_var HEIGHT
require_config_var ACODEC
require_config_var VCODEC
require_config_var FRAMERATE
require_config_var KEYFRAME_INTERVAL
require_config_var ADDR_HOME
require_config_var FFSTREAM_LOG_LEVEL
require_config_var FFSTREAM_AUTO_BITRATE
require_config_var FFSTREAM_AUTOBITRATE_MAX_HEIGHT
require_config_var FFSTREAM_AUTOBITRATE_MIN_HEIGHT
require_config_var FFSTREAM_AUTO_BYPASS
validate_video_codec

: "${FFSTREAM_BIN:=/data/user/0/com.termux/files/usr/bin/ffstream}"
: "${FFSTREAM_BIN_RUNNER:=termux-root}"
: "${FFSTREAM_RAM_CAP_AS:=unlimited}"
: "${FFSTREAM_LOG_FILE:=/data/ubuntu/tmp/ffstream.log}"
validate_ram_cap_as
validate_ffstream_binary

fix-mic &

export PULSE_SERVER=127.0.0.1

DST=rtmp://"$ADDR_HOME":1946

export GOMEMLIMIT="${FFSTREAM_GOMEMLIMIT:-15GiB}"
echo 1000 > /proc/self/oom_score_adj
# Mediamtx-side daemon: consumes the DJI/mediamtx proxy input and republishes
# to avd. Built-in Android camera/mic inputs live in the separate
# ffstream-camera daemon on port 3594.
prlimit --as="$FFSTREAM_RAM_CAP_AS" -- \
	nice -n -15 \
	taskset -c 6-8 \
	"$FFSTREAM_BIN_RUNNER" "$FFSTREAM_BIN" \
		-v "$FFSTREAM_LOG_LEVEL" \
		-retry_input_timeout_on_failure 1s \
		-retry_output_timeout_on_failure 0 \
		-auto_bitrate "$FFSTREAM_AUTO_BITRATE" \
		-auto_bitrate_max_height "$FFSTREAM_AUTOBITRATE_MAX_HEIGHT" \
		-auto_bitrate_min_height "$FFSTREAM_AUTOBITRATE_MIN_HEIGHT" \
		-auto_bitrate_auto_bypass "$FFSTREAM_AUTO_BYPASS" \
		-hwaccel mediacodec \
		-mux_mode different_outputs_same_tracks_split_av \
		-listen_control tcp:127.0.0.1:3593 \
		-listen_net_pprof 0.0.0.0:8238 \
		-itsoffset 00:00:00.000 \
		-fflags nobuffer \
		-flags low_delay \
		-rtbufsize 5M \
		-probesize 32768 \
		-analyzeduration 200000 \
		-video_size "$WIDTH"x"$HEIGHT" \
		-fallback_priority 10 \
		-i rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3 \
		-s "$WIDTH"x"$HEIGHT" \
		-c:v "$VCODEC" \
		-ar 48000 \
		-ac 1 \
		-sample_fmt fltp \
		-c:a "$ACODEC" \
		-b:v 4M \
		-bufsize 4M \
		-g "$[ $FRAMERATE * $KEYFRAME_INTERVAL ]" \
		-r "$FRAMERATE" \
		-f flv \
		"$DST"'/pixel/dji-osmo-pocket-3-${v:0:codec}${a:0:codec}-${v:0:height}${a:0:rate}/' \
		2>&1 | tee "$FFSTREAM_LOG_FILE"
