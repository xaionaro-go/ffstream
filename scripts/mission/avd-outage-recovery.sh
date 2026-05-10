#!/usr/bin/env bash
set -Eeuo pipefail

# Goal 5 — AVD outage recovery: simulate ~2-min AVD daemon outage; verify both
# AVD endpoints (DJI + builtin) recover after AVD restarts, with no operator
# intervention beyond the AVD restart itself.
#
# Pre-condition: AVD running with PID file recorded; activation completed (Goal 2)
# so /pixel/builtincamera-merged is publishing; DJI camera streaming so
# /pixel/dji-osmo-pocket-3-merged is publishing.
#
# Mission: stop AVD (SIGTERM); wait AVD_OUTAGE_SECONDS; restart AVD via the
# same setsid+nohup launch pattern as goal1-normal-topology.sh; wait for AVD
# port-listeners ready; probe both endpoints with full 10s integrity gates.
#
# Per Goal 5 brief acceptance: wingout/ffstream survive the outage (they keep
# retrying); AVD back up → both endpoints publish again to recovered AVD
# without operator intervention.
#
# Spec lineage: AVD lifecycle helpers + ffprobe integrity-gate format mirror
# scripts/mission/goal1-normal-topology.sh. UI/state helpers mirror
# builtincamera-ui-activation.sh + builtincamera-ui-deactivation.sh patterns.

PHONE_SERIAL="${PHONE_SERIAL:-41041JEKB08092}"
RUN_ROOT="${RUN_ROOT:-${HOME}/tmp/test-avd-outage-recovery-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="${RUN_DIR:-$RUN_ROOT/avd-outage-recovery-$(date -u +%Y%m%dT%H%M%SZ)}"
AVD_BIN="${AVD_BIN:-/home/streaming/go/bin/avd}"
AVD_CONFIG="${AVD_CONFIG:-/home/streaming/.avd.conf}"
AVD_MAX_VRAM_MIB="${AVD_MAX_VRAM_MIB:-4096}"
AVD_HOST="${AVD_HOST:-127.0.0.1}"
AVD_CONSUMER_PORT="${AVD_CONSUMER_PORT:-1945}"
AVD_PUBLISHER_PORT="${AVD_PUBLISHER_PORT:-1946}"
AVD_PID_FILE="${AVD_PID_FILE:-${HOME}/tmp/avd.pid}"
AVD_READY_TIMEOUT_SECONDS="${AVD_READY_TIMEOUT_SECONDS:-30}"
AVD_OUTAGE_SECONDS="${AVD_OUTAGE_SECONDS:-120}"
AVD_STOP_GRACE_SECONDS="${AVD_STOP_GRACE_SECONDS:-10}"
BUILTIN_CAMERA_CONSUMER_URL="${BUILTIN_CAMERA_CONSUMER_URL:-rtmp://$AVD_HOST:$AVD_CONSUMER_PORT/pixel/builtincamera-merged}"
DJI_CAMERA_CONSUMER_URL="${DJI_CAMERA_CONSUMER_URL:-rtmp://$AVD_HOST:$AVD_CONSUMER_PORT/pixel/dji-osmo-pocket-3-merged}"
CAPTURE_SECONDS="${CAPTURE_SECONDS:-10}"
DEACTIVATION_SCRIPT="${DEACTIVATION_SCRIPT:-$(dirname "${BASH_SOURCE[0]}")/builtincamera-ui-deactivation.sh}"

record_command() {
	local name="$1"
	shift
	printf '%q' "$1" >"$RUN_DIR/$name.cmd"
	shift
	for arg in "$@"; do
		printf ' %q' "$arg" >>"$RUN_DIR/$name.cmd"
	done
	printf '\n' >>"$RUN_DIR/$name.cmd"
}

run_cmd() {
	local name="$1"
	shift
	record_command "$name" "$@"
	set +e
	"$@" >"$RUN_DIR/$name.stdout" 2>"$RUN_DIR/$name.stderr"
	local status="$?"
	set -e
	printf '%s\n' "$status" >"$RUN_DIR/$name.status"
	return "$status"
}

# avd_listening: returns 0 if BOTH consumer + publisher ports are listening.
avd_listening() {
	local label="$1"
	run_cmd "ss-$label" ss -ltn || true
	rg -q ":$AVD_CONSUMER_PORT[^0-9]" "$RUN_DIR/ss-$label.stdout" || return 1
	rg -q ":$AVD_PUBLISHER_PORT[^0-9]" "$RUN_DIR/ss-$label.stdout" || return 1
	return 0
}

# avd_pid_alive: returns 0 if PID file refers to a live process.
# Regex `^[1-9][0-9]*$` rejects "0" (and other leading-zero forms) because
# `kill -0 0` succeeds on every UNIX (signals the caller's process group),
# which would false-positive a stale-PID file containing "0".
avd_pid_alive() {
	[ -s "$AVD_PID_FILE" ] || return 1
	local pid
	pid="$(cat "$AVD_PID_FILE" 2>/dev/null || true)"
	[ -n "$pid" ] || return 1
	[[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
	kill -0 "$pid" 2>/dev/null
}

# stop_avd_with_grace: SIGTERM, wait grace, SIGKILL if still alive.
stop_avd_with_grace() {
	local label="$1"
	if ! avd_pid_alive; then
		printf 'AVD pid not alive at stop attempt\n' >"$RUN_DIR/stop-$label-skipped.txt"
		return 1
	fi
	local pid
	pid="$(cat "$AVD_PID_FILE")"
	run_cmd "stop-term-$label" kill -TERM "$pid" || true
	local i
	for i in $(seq 1 "$AVD_STOP_GRACE_SECONDS"); do
		sleep 1
		if ! kill -0 "$pid" 2>/dev/null; then
			printf 'graceful exit at +%ss\n' "$i" >"$RUN_DIR/stop-$label-exit.txt"
			return 0
		fi
	done
	# Grace exhausted; SIGKILL.
	run_cmd "stop-kill-$label" kill -KILL "$pid" || true
	sleep 1
	if kill -0 "$pid" 2>/dev/null; then
		printf 'SIGKILL did not reap pid=%s\n' "$pid" >"$RUN_DIR/stop-$label-kill-failed.txt"
		return 1
	fi
	printf 'killed forcefully after %ss grace\n' "$AVD_STOP_GRACE_SECONDS" \
		>"$RUN_DIR/stop-$label-exit.txt"
	return 0
}

# start_avd: setsid+nohup launch pattern from goal1; records new PID to PID file.
start_avd() {
	local label="$1"
	printf '%q --config-path %q --max-vram-mib %q\n' \
		"$AVD_BIN" "$AVD_CONFIG" "$AVD_MAX_VRAM_MIB" >"$RUN_DIR/start-$label.cmd"
	# Use setsid+nohup so AVD persists across this script's lifetime.
	/usr/bin/nohup /usr/bin/setsid "$AVD_BIN" \
		--config-path "$AVD_CONFIG" \
		--max-vram-mib "$AVD_MAX_VRAM_MIB" \
		>"$RUN_DIR/avd-$label.log" 2>&1 &
	local new_pid="$!"
	printf '%s\n' "$new_pid" >"$AVD_PID_FILE"
	printf '%s\n' "$new_pid" >"$RUN_DIR/start-$label.pid"
	# Detach fully (don't keep in our job table).
	disown "$new_pid" 2>/dev/null || true
}

# wait_for_avd_ready: poll up to AVD_READY_TIMEOUT_SECONDS for both ports listening.
wait_for_avd_ready() {
	local label="$1"
	local i
	for i in $(seq 1 "$AVD_READY_TIMEOUT_SECONDS"); do
		if avd_listening "ready-$label-$i"; then
			printf 'ready at +%ss\n' "$i" >"$RUN_DIR/ready-$label.txt"
			return 0
		fi
		sleep 1
	done
	return 1
}

# probe_endpoint_publishing: ffprobe + 10s ffmpeg capture + integrity gates.
# Goal-5 augmented gates per Task #4 acceptance (≥3 keyframes >20KB,
# mean_volume > -90dB, luminance evidence).
#
# Returns 0 iff ALL of:
#   - capture has h264|av1 video + aac audio
#   - frame counts in 30fps/48kHz ranges (250-600 video, 300-700 audio for 10s)
#   - ≥250 video packets total + ≥3 keyframes over 20KB (per goal1 pattern)
#   - audio mean_volume > -90dB (volumedetect)
#   - luminance range > 50 (signalstats yavg/ymin/ymax across ≥5 samples)
probe_endpoint_publishing() {
	local label="$1"
	local url="$2"
	local out_dir="$RUN_DIR/$label"
	mkdir -p "$out_dir"
	local capture_media="$out_dir/capture-${CAPTURE_SECONDS}s.mkv"

	set +e
	timeout 20 ffprobe \
		-v error \
		-rw_timeout 8000000 \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels \
		-of json \
		"$url" >"$out_dir/live-ffprobe.stdout" 2>"$out_dir/live-ffprobe.stderr"
	local live_status="$?"
	set -e
	printf '%s\n' "$live_status" >"$out_dir/live-ffprobe.status"
	if [ "$live_status" -ne 0 ]; then
		return 1
	fi

	set +e
	timeout "$((CAPTURE_SECONDS + 25))" ffmpeg \
		-hide_banner \
		-y \
		-rw_timeout 8000000 \
		-t "$CAPTURE_SECONDS" \
		-i "$url" \
		-map 0:v:0 \
		-map 0:a:0 \
		-c copy \
		"$capture_media" >"$out_dir/capture.stdout" 2>"$out_dir/capture.stderr"
	local capture_status="$?"
	set -e
	printf '%s\n' "$capture_status" >"$out_dir/capture.status"
	if [ "$capture_status" -ne 0 ] || [ ! -s "$capture_media" ]; then
		return 1
	fi

	set +e
	timeout 20 ffprobe \
		-v error \
		-count_frames \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels,nb_read_frames,duration \
		-of json \
		"$capture_media" >"$out_dir/capture-ffprobe.stdout" 2>"$out_dir/capture-ffprobe.stderr"
	local probe_status="$?"
	set -e
	printf '%s\n' "$probe_status" >"$out_dir/capture-ffprobe.status"
	if [ "$probe_status" -ne 0 ]; then
		return 1
	fi

	# Goal-5 augmented gates (per Task #4 acceptance):

	# Video packets + keyframe sizes.
	set +e
	timeout 20 ffprobe \
		-v error \
		-select_streams v:0 \
		-show_packets \
		-show_entries packet=flags,size,pts_time,dts_time \
		-of json \
		"$capture_media" >"$out_dir/video-packets.stdout" 2>"$out_dir/video-packets.stderr"
	local pkt_status="$?"
	set -e
	printf '%s\n' "$pkt_status" >"$out_dir/video-packets.status"
	if [ "$pkt_status" -ne 0 ]; then
		return 1
	fi

	# Audio mean_volume via volumedetect (writes to stderr per ffmpeg convention).
	set +e
	timeout 25 ffmpeg \
		-hide_banner \
		-i "$capture_media" \
		-map 0:a:0 \
		-af volumedetect \
		-vn \
		-f null \
		- >"$out_dir/volumedetect.stdout" 2>"$out_dir/volumedetect.stderr"
	local vol_status="$?"
	set -e
	printf '%s\n' "$vol_status" >"$out_dir/volumedetect.status"

	# Luminance via signalstats metadata=print:file=...
	set +e
	timeout 30 ffmpeg \
		-hide_banner \
		-i "$capture_media" \
		-map 0:v:0 \
		-vf "signalstats,metadata=mode=print:file=$out_dir/video-signalstats.txt" \
		-frames:v 60 \
		-f null \
		- >"$out_dir/signalstats.stdout" 2>"$out_dir/signalstats.stderr"
	local sig_status="$?"
	set -e
	printf '%s\n' "$sig_status" >"$out_dir/signalstats.status"

	# Combined integrity check (mirrors goal1 media-check.py validators).
	CAPTURE_FFPROBE="$out_dir/capture-ffprobe.stdout" \
		PACKETS="$out_dir/video-packets.stdout" \
		VOLUMEDETECT="$out_dir/volumedetect.stderr" \
		SIGNALSTATS="$out_dir/video-signalstats.txt" \
		python3 <<'PY'
import json
import os
import re
import statistics
import sys

with open(os.environ["CAPTURE_FFPROBE"], encoding="utf-8") as f:
    try:
        data = json.load(f)
    except json.JSONDecodeError:
        sys.exit(1)
if not isinstance(data, dict):
    sys.exit(1)
streams = data.get("streams") or []
if not isinstance(streams, list):
    sys.exit(1)

def find(codec_type):
    for s in streams:
        if s.get("codec_type") == codec_type:
            return s
    return None

video = find("video")
audio = find("audio")
if not video or not audio:
    sys.exit(1)
if (video.get("codec_name") not in ("h264", "av1")
        or audio.get("codec_name") != "aac"):
    sys.exit(1)
video_frames = int(video.get("nb_read_frames") or 0)
audio_frames = int(audio.get("nb_read_frames") or 0)
if not (250 <= video_frames <= 600):
    sys.exit(1)
if not (300 <= audio_frames <= 700):
    sys.exit(1)

# Keyframe gate: ≥3 keyframes over 20KB, ≥250 video packets total.
with open(os.environ["PACKETS"], encoding="utf-8") as f:
    try:
        pkt_data = json.load(f)
    except json.JSONDecodeError:
        sys.exit(1)
if not isinstance(pkt_data, dict):
    sys.exit(1)
packets = pkt_data.get("packets") or []
if not isinstance(packets, list):
    sys.exit(1)
keyframe_sizes = [
    int(p.get("size") or 0)
    for p in packets
    if "K" in str(p.get("flags") or "")
]
large_keyframes = [s for s in keyframe_sizes if s > 20_000]
if len(packets) < 250 or len(large_keyframes) < 3:
    sys.exit(1)

# Volume gate: mean_volume > -90 dB (volumedetect on stderr).
with open(os.environ["VOLUMEDETECT"], encoding="utf-8", errors="replace") as f:
    vol_text = f.read()
match = re.search(r"mean_volume:\s*(-?\d+(?:\.\d+)?) dB", vol_text)
if not match:
    sys.exit(1)
mean_volume = float(match.group(1))
if mean_volume <= -90.0:
    sys.exit(1)

# Luminance gate: signalstats metadata file with luma range > 50 across ≥5 samples.
with open(os.environ["SIGNALSTATS"], encoding="utf-8", errors="replace") as f:
    sig_text = f.read()
yavg = [float(v) for v in re.findall(r"lavfi\.signalstats\.YAVG=([0-9.]+)", sig_text)]
ymin = [float(v) for v in re.findall(r"lavfi\.signalstats\.YMIN=([0-9.]+)", sig_text)]
ymax = [float(v) for v in re.findall(r"lavfi\.signalstats\.YMAX=([0-9.]+)", sig_text)]
if len(yavg) < 5 or not ymin or not ymax:
    sys.exit(1)
luma_range = max(ymax) - min(ymin)
if luma_range <= 50.0:
    sys.exit(1)

sys.exit(0)
PY
}

finish_with_result() {
	local result="$1"
	local exit_code="$2"
	printf '%s\n' "$result" >"$RUN_DIR/result.txt"
	printf 'RUN_DIR=%s\n' "$RUN_DIR"
	cat "$RUN_DIR/result.txt"
	exit "$exit_code"
}

main() {
	mkdir -p "$RUN_DIR"
	printf '%s\n' "$RUN_DIR" >"$RUN_ROOT/latest-avd-outage-recovery-run.txt"

	date -u +%Y-%m-%dT%H:%M:%SZ >"$RUN_DIR/started-at.txt"
	printf 'PHONE_SERIAL=%s\n' "$PHONE_SERIAL" >"$RUN_DIR/config.txt"
	printf 'RUN_DIR=%s\n' "$RUN_DIR" >>"$RUN_DIR/config.txt"
	printf 'BUILTIN_CAMERA_CONSUMER_URL=%s\n' "$BUILTIN_CAMERA_CONSUMER_URL" >>"$RUN_DIR/config.txt"
	printf 'DJI_CAMERA_CONSUMER_URL=%s\n' "$DJI_CAMERA_CONSUMER_URL" >>"$RUN_DIR/config.txt"
	printf 'AVD_OUTAGE_SECONDS=%s\n' "$AVD_OUTAGE_SECONDS" >>"$RUN_DIR/config.txt"

	# Pre-condition: AVD alive + both endpoints publishing.
	if ! avd_pid_alive; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_AVD_NOT_RUNNING_PRE_OUTAGE" 60
	fi
	if ! probe_endpoint_publishing "pre-outage-builtin" "$BUILTIN_CAMERA_CONSUMER_URL"; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_BUILTIN_NOT_PUBLISHING_PRE_OUTAGE" 61
	fi
	if ! probe_endpoint_publishing "pre-outage-dji" "$DJI_CAMERA_CONSUMER_URL"; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_DJI_NOT_PUBLISHING_PRE_OUTAGE" 62
	fi

	# Outage simulation: stop AVD, wait, restart AVD.
	if ! stop_avd_with_grace "outage"; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_AVD_STOP" 63
	fi

	printf 'sleeping %ss\n' "$AVD_OUTAGE_SECONDS" >"$RUN_DIR/outage.txt"
	sleep "$AVD_OUTAGE_SECONDS"

	start_avd "post-outage"
	if ! wait_for_avd_ready "post-outage"; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_AVD_READY_TIMEOUT" 64
	fi

	# Recovery verification: both endpoints must publish again with full integrity gates.
	if ! probe_endpoint_publishing "post-outage-builtin" "$BUILTIN_CAMERA_CONSUMER_URL"; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_BUILTIN_DID_NOT_RECOVER" 65
	fi
	if ! probe_endpoint_publishing "post-outage-dji" "$DJI_CAMERA_CONSUMER_URL"; then
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_DJI_DID_NOT_RECOVER" 66
	fi

	# Phase 4: post-recovery deactivate via UI; both AVD-side gates must hold
	# (builtin route empty + DJI continuity preserved). Delegates to the
	# canonical builtincamera-ui-deactivation.sh which encapsulates the full
	# UI tap + status-poll + AVD probe contract.
	if [ -x "$DEACTIVATION_SCRIPT" ]; then
		set +e
		RUN_ROOT="$RUN_DIR/post-recovery-deactivation" \
			BUILTIN_CAMERA_CONSUMER_URL="$BUILTIN_CAMERA_CONSUMER_URL" \
			DJI_CAMERA_CONSUMER_URL="$DJI_CAMERA_CONSUMER_URL" \
			"$DEACTIVATION_SCRIPT" >"$RUN_DIR/post-recovery-deactivate.stdout" \
			2>"$RUN_DIR/post-recovery-deactivate.stderr"
		local deact_status="$?"
		set -e
		printf '%s\n' "$deact_status" >"$RUN_DIR/post-recovery-deactivate.status"
		if [ "$deact_status" -ne 0 ]; then
			finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_POST_RECOVERY_DEACTIVATE" 67
		fi
	else
		printf '%s\n' "DEACTIVATION_SCRIPT not executable: $DEACTIVATION_SCRIPT" \
			>"$RUN_DIR/post-recovery-deactivate-skipped.txt"
		finish_with_result "AVD_OUTAGE_RECOVERY_FAIL_DEACTIVATION_SCRIPT_MISSING" 68
	fi

	finish_with_result "AVD_OUTAGE_RECOVERY_PASS" 0
}

# Sourcability guard — allows test fixtures to source helpers without triggering main flow.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
	main "$@"
fi
