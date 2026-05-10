#!/usr/bin/env bash
set -Eeuo pipefail

PHONE_SERIAL="${PHONE_SERIAL:-41041JEKB08092}"
RUN_ROOT="${RUN_ROOT:-${HOME}/tmp/test-phone-activation-fix-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="${RUN_DIR:-$RUN_ROOT/goal1-normal-$(date -u +%Y%m%dT%H%M%SZ)}"
AVD_BIN="${AVD_BIN:-/home/streaming/go/bin/avd}"
AVD_CONFIG="${AVD_CONFIG:-/home/streaming/.avd.conf}"
AVD_MAX_VRAM_MIB="${AVD_MAX_VRAM_MIB:-4096}"
AVD_READY_TIMEOUT_SECONDS="${AVD_READY_TIMEOUT_SECONDS:-20}"
AVD_PREVIOUS_PID_FILE="${AVD_PREVIOUS_PID_FILE:-}"
FFSTREAMCTL_BIN="${FFSTREAMCTL_BIN:-/home/streaming/go/bin/ffstreamctl}"
SOURCE_FORWARD_PORT="${SOURCE_FORWARD_PORT:-19350}"
CONTROL_FORWARD_PORT="${CONTROL_FORWARD_PORT:-23593}"
PHONE_MEDIAMTX_PORT="${PHONE_MEDIAMTX_PORT:-1935}"
PHONE_FFSTREAM_CONTROL_PORT="${PHONE_FFSTREAM_CONTROL_PORT:-3593}"
AVD_CONSUMER_PORT="${AVD_CONSUMER_PORT:-1945}"
AVD_PUBLISHER_PORT="${AVD_PUBLISHER_PORT:-1946}"
GOAL1_ENDPOINT="${GOAL1_ENDPOINT:-dji-osmo-pocket-3-merged}"
SPLIT_VIDEO_ROUTE="${SPLIT_VIDEO_ROUTE:-pixel/dji-osmo-pocket-3-av1-1080}"
SPLIT_VIDEO_READY_TIMEOUT_SECONDS="${SPLIT_VIDEO_READY_TIMEOUT_SECONDS:-30}"
POLL_COUNT="${POLL_COUNT:-30}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-3}"
KEEP_AVD_ON_PASS="${KEEP_AVD_ON_PASS:-1}"
KEEP_SYNTHETIC_ON_PASS="${KEEP_SYNTHETIC_ON_PASS:-1}"
CAPTURE_SECONDS="${CAPTURE_SECONDS:-10}"

mkdir -p "$RUN_DIR"
printf '%s\n' "$RUN_DIR" >"$RUN_ROOT/latest-goal1-run.txt"

result="INCOMPLETE"

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

run_shell() {
	local name="$1"
	shift
	printf '%s\n' "$*" >"$RUN_DIR/$name.cmd"
	set +e
	bash -lc "$*" >"$RUN_DIR/$name.stdout" 2>"$RUN_DIR/$name.stderr"
	local status="$?"
	set -e
	printf '%s\n' "$status" >"$RUN_DIR/$name.status"
	return "$status"
}

record_artifact_command() {
	local artifact_dir="$1"
	local name="$2"
	shift 2
	printf '%q' "$1" >"$artifact_dir/$name.cmd"
	shift
	for arg in "$@"; do
		printf ' %q' "$arg" >>"$artifact_dir/$name.cmd"
	done
	printf '\n' >>"$artifact_dir/$name.cmd"
}

run_artifact_cmd() {
	local artifact_dir="$1"
	local name="$2"
	shift 2
	record_artifact_command "$artifact_dir" "$name" "$@"
	set +e
	"$@" >"$artifact_dir/$name.stdout" 2>"$artifact_dir/$name.stderr"
	local status="$?"
	set -e
	printf '%s\n' "$status" >"$artifact_dir/$name.status"
	return "$status"
}

is_pid_alive() {
	local pid="$1"
	[[ "$pid" =~ ^[0-9]+$ ]] && [ -d "/proc/$pid" ]
}

avd_pid_matches_command() {
	local pid="$1"
	local expected_bin expected_config actual_bin actual_cmdline

	expected_bin="$(readlink -f "$AVD_BIN")"
	expected_config="$(readlink -f "$AVD_CONFIG")"
	actual_bin="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
	actual_cmdline="$(tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null || true)"

	printf '%s\n' "$actual_bin" >"$RUN_DIR/avd-pid-$pid-exe.txt"
	printf '%s\n' "$actual_cmdline" >"$RUN_DIR/avd-pid-$pid-cmdline.txt"

	[ "$actual_bin" = "$expected_bin" ] || return 1
	[[ "$actual_cmdline" == *"--config-path"* ]] || return 1
	[[ "$actual_cmdline" == *"$AVD_CONFIG"* || "$actual_cmdline" == *"$expected_config"* ]] || return 1
}

stop_recorded_avd_pid() {
	local pid_file="$1"
	local label="$2"

	[ -n "$pid_file" ] || return 0
	[ -s "$pid_file" ] || return 0

	local pid
	pid="$(head -n 1 "$pid_file")"
	printf '%s\n' "$pid" >"$RUN_DIR/$label-pid.txt"

	if ! is_pid_alive "$pid"; then
		printf '%s\n' "recorded AVD pid is not running: $pid" >"$RUN_DIR/$label-skipped.txt"
		return 0
	fi

	if ! avd_pid_matches_command "$pid"; then
		printf '%s\n' "recorded pid does not match expected avd executable/config: $pid" >"$RUN_DIR/$label-refused.txt"
		return 1
	fi

	printf 'kill %q\n' "$pid" >"$RUN_DIR/$label.cmd"
	kill "$pid" >"$RUN_DIR/$label.stdout" 2>"$RUN_DIR/$label.stderr" || true
	for _ in $(seq 1 20); do
		if ! is_pid_alive "$pid"; then
			printf '%s\n' "stopped" >"$RUN_DIR/$label.result"
			return 0
		fi
		sleep 0.25
	done
	printf '%s\n' "still running after TERM" >"$RUN_DIR/$label.result"
	return 1
}

collect_avd_state() {
	local label="$1"
	pgrep -x avd >"$RUN_DIR/$label-pgrep-avd.txt" 2>"$RUN_DIR/$label-pgrep-avd.stderr" || true
	ss -ltnp "( sport = :$AVD_CONSUMER_PORT or sport = :$AVD_PUBLISHER_PORT )" \
		>"$RUN_DIR/$label-ss-listeners.txt" 2>"$RUN_DIR/$label-ss-listeners.stderr" || true
	if [ -f "$RUN_DIR/avd.log" ]; then
		tail -n 300 "$RUN_DIR/avd.log" >"$RUN_DIR/$label-avd-log-tail.txt" || true
	fi
}

avd_listeners_ready() {
	local out_file="$1"
	ss -ltnp "( sport = :$AVD_CONSUMER_PORT or sport = :$AVD_PUBLISHER_PORT )" \
		>"$out_file" 2>"$out_file.stderr" || return 1
	grep -q ":$AVD_CONSUMER_PORT" "$out_file" || return 1
	grep -q ":$AVD_PUBLISHER_PORT" "$out_file" || return 1
}

assert_avd_ready() {
	local label="$1"
	local pid
	pid="$(cat "$RUN_DIR/avd.pid")"
	if ! is_pid_alive "$pid"; then
		collect_avd_state "$label-avd-down"
		printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_AVD_PID_EXITED" >"$RUN_DIR/result.txt"
		result="FAIL"
		exit 20
	fi
	if ! avd_listeners_ready "$RUN_DIR/$label-avd-listeners.txt"; then
		collect_avd_state "$label-avd-listeners-down"
		printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_AVD_LISTENER_DOWN" >"$RUN_DIR/result.txt"
		result="FAIL"
		exit 21
	fi
}

avd_log_has_split_video_route() {
	[ -f "$RUN_DIR/avd.log" ] || return 1
	rg -q \
		"routePath == '$SPLIT_VIDEO_ROUTE'|RTMP publisher route path resolved.*$SPLIT_VIDEO_ROUTE|src_path=$SPLIT_VIDEO_ROUTE|path=$SPLIT_VIDEO_ROUTE" \
		"$RUN_DIR/avd.log"
}

avd_log_has_split_video_packet() {
	[ -f "$RUN_DIR/avd.log" ] || return 1
	rg -q \
		"first (input|output) packet.*mediaType=video.*src_path=$SPLIT_VIDEO_ROUTE|src_path=$SPLIT_VIDEO_ROUTE.*first (input|output) packet.*mediaType=video" \
		"$RUN_DIR/avd.log"
}

write_split_video_avd_evidence() {
	local name="$1"
	run_shell "$name" \
		"rg -n 'route path resolved|routePath == '\''$SPLIT_VIDEO_ROUTE'\''|$SPLIT_VIDEO_ROUTE|first (input|output) packet|mediaType=video|1946' '$RUN_DIR/avd.log' || true"
}

wait_for_split_video_route() {
	local i
	for i in $(seq 1 "$SPLIT_VIDEO_READY_TIMEOUT_SECONDS"); do
		assert_avd_ready "split-video-$i"
		if avd_log_has_split_video_route && avd_log_has_split_video_packet; then
			write_split_video_avd_evidence "split-video-materialized"
			return 0
		fi
		sleep 1
	done

	write_split_video_avd_evidence "split-video-not-materialized-avd"
	return 1
}

fail_split_video_not_materialized() {
	collect_phone_state "after-split-video-not-materialized"
	printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_SPLIT_VIDEO_NOT_MATERIALIZED" >"$RUN_DIR/result.txt"
	printf '%s\n' "1" >"$RUN_DIR/dji-fail.flag"
	result="FAIL"
	printf 'RUN_DIR=%s\n' "$RUN_DIR"
	cat "$RUN_DIR/result.txt"
	exit 32
}

start_avd_detached() {
	collect_avd_state "before-avd-start"
	if ss -ltn "( sport = :$AVD_CONSUMER_PORT or sport = :$AVD_PUBLISHER_PORT )" | grep -qE ":($AVD_CONSUMER_PORT|$AVD_PUBLISHER_PORT)"; then
		printf '%s\n' "AVD ports are already in use; provide AVD_PREVIOUS_PID_FILE for safe recorded-pid cleanup" >"$RUN_DIR/avd-start-refused.txt"
		printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_AVD_PORTS_ALREADY_IN_USE" >"$RUN_DIR/result.txt"
		result="FAIL"
		exit 22
	fi

	printf '/usr/bin/nohup /usr/bin/setsid %q --config-path %q --log-level debug --max-vram-mib %q < /dev/null >%q 2>&1 &\n' \
		"$AVD_BIN" "$AVD_CONFIG" "$AVD_MAX_VRAM_MIB" "$RUN_DIR/avd.log" >"$RUN_DIR/avd-start.cmd"
	/usr/bin/nohup /usr/bin/setsid "$AVD_BIN" \
		--config-path "$AVD_CONFIG" \
		--log-level debug \
		--max-vram-mib "$AVD_MAX_VRAM_MIB" \
		< /dev/null >"$RUN_DIR/avd.log" 2>&1 &
	printf '%s\n' "$!" >"$RUN_DIR/avd.pid"

	for i in $(seq 1 "$AVD_READY_TIMEOUT_SECONDS"); do
		if is_pid_alive "$(cat "$RUN_DIR/avd.pid")" && avd_listeners_ready "$RUN_DIR/avd-listeners-ready-$i.txt"; then
			cp "$RUN_DIR/avd-listeners-ready-$i.txt" "$RUN_DIR/avd-listeners-after-start.txt"
			return 0
		fi
		sleep 1
	done

	collect_avd_state "after-avd-start-timeout"
	printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_AVD_NOT_READY" >"$RUN_DIR/result.txt"
	result="FAIL"
	exit 23
}

json_has_audio_video() {
	local json_path="$1"
	python3 - "$json_path" <<'PY'
import json
import sys

path = sys.argv[1]
try:
    data = json.load(open(path, encoding="utf-8"))
except Exception as exc:
    print(f"json_error={exc}")
    sys.exit(1)

streams = data.get("streams") or []
has_video = any(s.get("codec_type") == "video" for s in streams)
has_audio = any(s.get("codec_type") == "audio" for s in streams)
print(f"streams={len(streams)} has_video={has_video} has_audio={has_audio}")
for stream in streams:
    print(json.dumps(stream, sort_keys=True))
sys.exit(0 if has_video and has_audio else 1)
PY
}

capture_goal1_media() {
	local capture_dir="$RUN_DIR/dji-${CAPTURE_SECONDS}s-capture-$(date -u +%Y%m%dT%H%M%SZ)"
	local url="rtmp://127.0.0.1:$AVD_CONSUMER_PORT/pixel/$GOAL1_ENDPOINT"
	local capture_media="$capture_dir/dji-merged-${CAPTURE_SECONDS}s.mkv"
	mkdir -p "$capture_dir"

	run_artifact_cmd "$capture_dir" started-at date -u +%Y-%m-%dT%H:%M:%SZ || true
	cp "$capture_dir/started-at.stdout" "$capture_dir/started-at.txt" || true
	run_artifact_cmd "$capture_dir" avd-listeners-before ss -ltnp \
		"( sport = :$AVD_CONSUMER_PORT or sport = :$AVD_PUBLISHER_PORT )" || true
	run_artifact_cmd "$capture_dir" host-processes-before pgrep -af \
		'(^|/)avd( |$)|ffmpeg.*dji-osmo-pocket3' || true
	run_artifact_cmd "$capture_dir" phone-processes-before timeout 10 adb -s "$PHONE_SERIAL" shell \
		'ps -A -o PID,PPID,NAME,ARGS | grep -E "mediamtx|ffstream|loop-run|crash_dump64"' || true

	run_artifact_cmd "$capture_dir" live-ffprobe timeout 20 ffprobe \
		-v error \
		-rw_timeout 8000000 \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels \
		-of json \
		"$url" || true
	cp "$capture_dir/live-ffprobe.stdout" "$capture_dir/live-ffprobe.json" || true
	local live_status
	live_status="$(cat "$capture_dir/live-ffprobe.status")"

	run_artifact_cmd "$capture_dir" capture timeout "$((CAPTURE_SECONDS + 25))" ffmpeg \
		-hide_banner \
		-y \
		-rw_timeout 8000000 \
		-t "$CAPTURE_SECONDS" \
		-i "$url" \
		-map 0:v:0 \
		-map 0:a:0 \
		-c copy \
		"$capture_media" || true
	local capture_status
	capture_status="$(cat "$capture_dir/capture.status")"

	run_artifact_cmd "$capture_dir" capture-ffprobe timeout 20 ffprobe \
		-v error \
		-count_frames \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels,nb_read_frames,duration \
		-of json \
		"$capture_media" || true
	cp "$capture_dir/capture-ffprobe.stdout" "$capture_dir/capture-ffprobe.json" || true
	local capture_probe_status
	capture_probe_status="$(cat "$capture_dir/capture-ffprobe.status")"

	run_artifact_cmd "$capture_dir" video-packets timeout 20 ffprobe \
		-v error \
		-select_streams v:0 \
		-show_packets \
		-show_entries packet=flags,size,pts_time,dts_time \
		-of json \
		"$capture_media" || true
	local video_packets_status
	video_packets_status="$(cat "$capture_dir/video-packets.status")"

	run_artifact_cmd "$capture_dir" capture-frame timeout 20 ffmpeg \
		-hide_banner \
		-y \
		-i "$capture_media" \
		-map 0:v:0 \
		-frames:v 1 \
		"$capture_dir/frame-001.png" || true
	local frame_status
	frame_status="$(cat "$capture_dir/capture-frame.status")"

	run_artifact_cmd "$capture_dir" signalstats timeout 30 ffmpeg \
		-hide_banner \
		-i "$capture_media" \
		-map 0:v:0 \
		-vf "signalstats,metadata=mode=print:file=$capture_dir/video-signalstats.txt" \
		-frames:v 60 \
		-f null \
		- || true
	local signalstats_status
	signalstats_status="$(cat "$capture_dir/signalstats.status")"

	run_artifact_cmd "$capture_dir" volumedetect timeout 25 ffmpeg \
		-hide_banner \
		-i "$capture_media" \
		-map 0:a:0 \
		-af volumedetect \
		-vn \
		-f null \
		- || true
	local audio_status
	audio_status="$(cat "$capture_dir/volumedetect.status")"

	cat >"$capture_dir/media-check.py" <<'PY'
import json
import math
import os
import re
import statistics
import sys


def load_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def parse_rate(value):
    if not value or value == "0/0":
        return 0.0
    if "/" not in value:
        return float(value)
    numerator, denominator = value.split("/", 1)
    denominator_value = float(denominator)
    if denominator_value == 0:
        return 0.0
    return float(numerator) / denominator_value


def find_stream(streams, codec_type):
    for stream in streams:
        if stream.get("codec_type") == codec_type:
            return stream
    raise AssertionError(f"missing {codec_type} stream")


def validate_streams(label, data, require_frame_counts):
    streams = data.get("streams") or []
    video = find_stream(streams, "video")
    audio = find_stream(streams, "audio")
    print(
        f"{label} streams={len(streams)} "
        f"video={video.get('codec_name')} {video.get('width')}x{video.get('height')} "
        f"rate={video.get('r_frame_rate')} "
        f"audio={audio.get('codec_name')} sample_rate={audio.get('sample_rate')}"
    )
    assert video.get("codec_name") == "h264", f"{label}: video codec is not h264"
    assert int(video.get("width") or 0) == 1920, f"{label}: video width is not 1920"
    assert int(video.get("height") or 0) == 1080, f"{label}: video height is not 1080"
    assert math.isclose(parse_rate(video.get("r_frame_rate")), 30.0, rel_tol=0.05), (
        f"{label}: video frame rate is not 30fps"
    )
    assert audio.get("codec_name") == "aac", f"{label}: audio codec is not aac"
    assert int(audio.get("sample_rate") or 0) == 48000, f"{label}: audio sample rate is not 48000"
    if require_frame_counts:
        video_frames = int(video.get("nb_read_frames") or 0)
        audio_frames = int(audio.get("nb_read_frames") or 0)
        print(f"{label} video_frames={video_frames} audio_frames={audio_frames}")
        assert 250 <= video_frames <= 600, f"{label}: video frame count outside 10s range"
        assert 300 <= audio_frames <= 700, f"{label}: audio frame count outside 10s range"


def validate_keyframes(path):
    packets = load_json(path).get("packets") or []
    keyframe_sizes = [
        int(packet.get("size") or 0)
        for packet in packets
        if "K" in str(packet.get("flags") or "")
    ]
    large_keyframes = [size for size in keyframe_sizes if size > 20_000]
    print(
        f"video_packets={len(packets)} keyframes={len(keyframe_sizes)} "
        f"large_keyframes={len(large_keyframes)}"
    )
    assert len(packets) >= 250, "not enough video packets in 10s capture"
    assert len(large_keyframes) >= 3, "fewer than 3 keyframes over 20KB"


def validate_volume(path):
    text = open(path, encoding="utf-8", errors="replace").read()
    match = re.search(r"mean_volume:\s*(-?\d+(?:\.\d+)?) dB", text)
    assert match, "volumedetect did not report mean_volume"
    mean_volume = float(match.group(1))
    print(f"mean_volume_db={mean_volume}")
    assert mean_volume > -90.0, "audio mean_volume is too low"


def validate_luminance(path):
    text = open(path, encoding="utf-8", errors="replace").read()
    yavg = [float(value) for value in re.findall(r"lavfi\.signalstats\.YAVG=([0-9.]+)", text)]
    ymin = [float(value) for value in re.findall(r"lavfi\.signalstats\.YMIN=([0-9.]+)", text)]
    ymax = [float(value) for value in re.findall(r"lavfi\.signalstats\.YMAX=([0-9.]+)", text)]
    assert len(yavg) >= 5, "signalstats produced too few luma samples"
    luma_stddev = statistics.pstdev(yavg)
    luma_range = max(ymax) - min(ymin)
    print(
        f"luma_samples={len(yavg)} "
        f"luma_yavg_stddev={luma_stddev:.3f} luma_range={luma_range:.3f}"
    )
    assert luma_range > 50.0, "luminance range is too low"


live_path, capture_path, media_path, frame_path, packet_path, volume_path, signalstats_path = sys.argv[1:]
validate_streams("live", load_json(live_path), False)
validate_streams("capture", load_json(capture_path), True)
print(
    f"capture_size={os.path.getsize(media_path)} "
    f"frame_size={os.path.getsize(frame_path)}"
)
assert os.path.getsize(media_path) > 0, "capture media is empty"
assert os.path.getsize(frame_path) > 0, "extracted frame is empty"
validate_keyframes(packet_path)
validate_volume(volume_path)
validate_luminance(signalstats_path)
PY

	record_artifact_command "$capture_dir" media-check python3 \
		"$capture_dir/media-check.py" \
		"$capture_dir/live-ffprobe.json" \
		"$capture_dir/capture-ffprobe.json" \
		"$capture_media" \
		"$capture_dir/frame-001.png" \
		"$capture_dir/video-packets.stdout" \
		"$capture_dir/volumedetect.stderr" \
		"$capture_dir/video-signalstats.txt"
	set +e
	python3 "$capture_dir/media-check.py" \
		"$capture_dir/live-ffprobe.json" \
		"$capture_dir/capture-ffprobe.json" \
		"$capture_media" \
		"$capture_dir/frame-001.png" \
		"$capture_dir/video-packets.stdout" \
		"$capture_dir/volumedetect.stderr" \
		"$capture_dir/video-signalstats.txt" \
		>"$capture_dir/media-check.stdout" 2>"$capture_dir/media-check.stderr"
	local media_status="$?"
	set -e
	printf '%s\n' "$media_status" >"$capture_dir/media-check.status"

	run_artifact_cmd "$capture_dir" avd-listeners-after ss -ltnp \
		"( sport = :$AVD_CONSUMER_PORT or sport = :$AVD_PUBLISHER_PORT )" || true
	run_artifact_cmd "$capture_dir" host-processes-after pgrep -af \
		'(^|/)avd( |$)|ffmpeg.*dji-osmo-pocket3' || true
	run_artifact_cmd "$capture_dir" crash_dump64 timeout 10 adb -s "$PHONE_SERIAL" shell pgrep -a crash_dump64 || true

	if [ "$live_status" -eq 0 ] &&
		[ "$capture_status" -eq 0 ] &&
		[ "$capture_probe_status" -eq 0 ] &&
		[ "$video_packets_status" -eq 0 ] &&
		[ "$frame_status" -eq 0 ] &&
		[ "$signalstats_status" -eq 0 ] &&
		[ "$audio_status" -eq 0 ] &&
		[ "$media_status" -eq 0 ]; then
		printf '%s\n' "DJI_CAPTURE_PASS" >"$capture_dir/result.txt"
		printf '%s\n' "$capture_dir" >"$RUN_DIR/dji-capture-dir.txt"
		return 0
	fi

	printf '%s\n' "DJI_CAPTURE_FAIL" >"$capture_dir/result.txt"
	printf '%s\n' "$capture_dir" >"$RUN_DIR/dji-capture-dir.txt"
	return 1
}

collect_phone_state() {
	local label="$1"
	timeout 10 adb -s "$PHONE_SERIAL" shell \
		'ps -A -o USER,PID,PPID,NAME,ARGS | grep -E "mediamtx|ffstream|crash_dump64|wingout"' \
		>"$RUN_DIR/$label-phone-ps.stdout" 2>"$RUN_DIR/$label-phone-ps.stderr" || true
	timeout 20 adb -s "$PHONE_SERIAL" shell \
		'for x in /data/ubuntu/tmp/ffstream.log /data/ubuntu/tmp/loop-run.log /data/ubuntu/tmp/mediamtx.log; do echo =====$x; tail -300 "$x" 2>/dev/null || true; done' \
		>"$RUN_DIR/$label-phone-logs.stdout" 2>"$RUN_DIR/$label-phone-logs.stderr" || true
}

cleanup() {
	local status="$?"
	if [ "$result" != "PASS" ]; then
		if [ -f "$RUN_DIR/synthetic-ffmpeg.pid" ]; then
			local synthetic_pid
			synthetic_pid="$(cat "$RUN_DIR/synthetic-ffmpeg.pid")"
			if is_pid_alive "$synthetic_pid"; then
				kill "$synthetic_pid" >"$RUN_DIR/synthetic-ffmpeg-kill.stdout" 2>"$RUN_DIR/synthetic-ffmpeg-kill.stderr" || true
			fi
		fi
		adb -s "$PHONE_SERIAL" forward --remove-all >"$RUN_DIR/adb-forward-remove-all-cleanup.stdout" 2>"$RUN_DIR/adb-forward-remove-all-cleanup.stderr" || true
		adb -s "$PHONE_SERIAL" reverse --remove-all >"$RUN_DIR/adb-reverse-remove-all-cleanup.stdout" 2>"$RUN_DIR/adb-reverse-remove-all-cleanup.stderr" || true
	fi

	if [ -f "$RUN_DIR/avd.pid" ]; then
		case "$result:$KEEP_AVD_ON_PASS" in
		PASS:1)
			;;
		*)
			stop_recorded_avd_pid "$RUN_DIR/avd.pid" "cleanup-avd-stop" || true
			;;
		esac
	fi

	if [ "$result:$KEEP_SYNTHETIC_ON_PASS" != "PASS:1" ] && [ -f "$RUN_DIR/synthetic-ffmpeg.pid" ]; then
		local synthetic_pid
		synthetic_pid="$(cat "$RUN_DIR/synthetic-ffmpeg.pid")"
		if is_pid_alive "$synthetic_pid"; then
			kill "$synthetic_pid" >"$RUN_DIR/synthetic-ffmpeg-kill-final.stdout" 2>"$RUN_DIR/synthetic-ffmpeg-kill-final.stderr" || true
		fi
	fi

	date -u +%Y-%m-%dT%H:%M:%SZ >"$RUN_DIR/finished-at.txt"
	exit "$status"
}
trap cleanup EXIT

date -u +%Y-%m-%dT%H:%M:%SZ >"$RUN_DIR/started-at.txt"
printf '%s\n' "PHONE_SERIAL=$PHONE_SERIAL" >"$RUN_DIR/config.txt"
printf '%s\n' "AVD_BIN=$AVD_BIN" >>"$RUN_DIR/config.txt"
printf '%s\n' "AVD_CONFIG=$AVD_CONFIG" >>"$RUN_DIR/config.txt"
printf '%s\n' "RUN_DIR=$RUN_DIR" >>"$RUN_DIR/config.txt"

stop_recorded_avd_pid "$AVD_PREVIOUS_PID_FILE" "previous-avd-stop"

run_cmd adb-forward-remove-all adb -s "$PHONE_SERIAL" forward --remove-all || true
run_cmd adb-reverse-remove-all adb -s "$PHONE_SERIAL" reverse --remove-all || true
run_cmd adb-forward-source adb -s "$PHONE_SERIAL" forward "tcp:$SOURCE_FORWARD_PORT" "tcp:$PHONE_MEDIAMTX_PORT"
run_cmd adb-forward-control adb -s "$PHONE_SERIAL" forward "tcp:$CONTROL_FORWARD_PORT" "tcp:$PHONE_FFSTREAM_CONTROL_PORT"
run_cmd adb-forward-list adb -s "$PHONE_SERIAL" forward --list
run_cmd adb-reverse-list adb -s "$PHONE_SERIAL" reverse --list || true

start_avd_detached
assert_avd_ready "pre-source"

collect_phone_state "before-source"
run_cmd ffstreamctl-inputs-before "$FFSTREAMCTL_BIN" --remote-addr "127.0.0.1:$CONTROL_FORWARD_PORT" inputs info || true
run_cmd ffstreamctl-pipelines-before "$FFSTREAMCTL_BIN" --remote-addr "127.0.0.1:$CONTROL_FORWARD_PORT" pipelines get || true

cat >"$RUN_DIR/synthetic-ffmpeg.cmd" <<EOF
nohup ffmpeg -hide_banner -re -f lavfi -i testsrc2=size=1920x1080:rate=30 -f lavfi -i sine=frequency=1000:sample_rate=48000 -map 0:v:0 -map 1:a:0 -c:v libx264 -preset ultrafast -tune zerolatency -profile:v baseline -pix_fmt yuv420p -b:v 6M -maxrate 6M -bufsize 12M -g 30 -keyint_min 30 -sc_threshold 0 -r 30 -c:a aac -b:a 128k -ar 48000 -ac 1 -f flv rtmp://127.0.0.1:$SOURCE_FORWARD_PORT/proxy/dji-osmo-pocket3
EOF
nohup ffmpeg -hide_banner -re \
	-f lavfi -i testsrc2=size=1920x1080:rate=30 \
	-f lavfi -i sine=frequency=1000:sample_rate=48000 \
	-map 0:v:0 -map 1:a:0 \
	-c:v libx264 -preset ultrafast -tune zerolatency -profile:v baseline \
	-pix_fmt yuv420p -b:v 6M -maxrate 6M -bufsize 12M \
	-g 30 -keyint_min 30 -sc_threshold 0 -r 30 \
	-c:a aac -b:a 128k -ar 48000 -ac 1 \
	-f flv "rtmp://127.0.0.1:$SOURCE_FORWARD_PORT/proxy/dji-osmo-pocket3" \
	>"$RUN_DIR/synthetic-ffmpeg.stdout" 2>"$RUN_DIR/synthetic-ffmpeg.stderr" </dev/null &
printf '%s\n' "$!" >"$RUN_DIR/synthetic-ffmpeg.pid"

sleep 7
assert_avd_ready "after-source"
run_cmd ffstreamctl-inputs-after-source "$FFSTREAMCTL_BIN" --remote-addr "127.0.0.1:$CONTROL_FORWARD_PORT" inputs info || true
run_cmd ffstreamctl-pipelines-after-source "$FFSTREAMCTL_BIN" --remote-addr "127.0.0.1:$CONTROL_FORWARD_PORT" pipelines get || true
wait_for_split_video_route || fail_split_video_not_materialized

for i in $(seq 1 "$POLL_COUNT"); do
	poll_dir="$RUN_DIR/poll-$i"
	mkdir -p "$poll_dir"
	date -u +%Y-%m-%dT%H:%M:%SZ >"$poll_dir/started-at.txt"
	assert_avd_ready "poll-$i"

	set +e
	timeout 12 ffprobe \
		-v error \
		-rw_timeout 5000000 \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels \
		-of json \
		"rtmp://127.0.0.1:$AVD_CONSUMER_PORT/pixel/$GOAL1_ENDPOINT" \
		>"$poll_dir/ffprobe.json" 2>"$poll_dir/ffprobe.stderr"
	ffprobe_status="$?"
	set -e
	printf '%s\n' "$ffprobe_status" >"$poll_dir/ffprobe.status"

	set +e
	json_has_audio_video "$poll_dir/ffprobe.json" >"$poll_dir/stream-check.txt" 2>"$poll_dir/stream-check.stderr"
	stream_status="$?"
	set -e
	printf '%s\n' "$stream_status" >"$poll_dir/stream-check.status"
	printf 'poll-%s ffprobe_status=%s stream_check=%s %s\n' \
		"$i" "$ffprobe_status" "$stream_status" "$(tr '\n' ' ' <"$poll_dir/stream-check.txt")" \
		>>"$RUN_DIR/poll-summary.txt"

	if [ "$ffprobe_status" -eq 0 ] && [ "$stream_status" -eq 0 ]; then
		cp "$poll_dir/ffprobe.json" "$RUN_DIR/ffprobe-first-success.json"
		cp "$poll_dir/stream-check.txt" "$RUN_DIR/stream-check-first-success.txt"
		if ! capture_goal1_media; then
			printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_CAPTURE" >"$RUN_DIR/result.txt"
			printf '%s\n' "1" >"$RUN_DIR/dji-fail.flag"
			result="FAIL"
			collect_phone_state "after-capture-fail"
			run_shell avd-route-and-bytes "rg -n 'route path resolved|onInitFinished|StartForwarding|dji-osmo-pocket-3|received [0-9]+ bytes|Unable|error|failed|1945|1946' '$RUN_DIR/avd.log' || true"
			printf 'RUN_DIR=%s\n' "$RUN_DIR"
			cat "$RUN_DIR/result.txt"
			exit 31
		fi
		printf '%s\n' "DJI_NORMAL_TOPOLOGY_PASS" >"$RUN_DIR/result.txt"
		printf '%s\n' "1" >"$RUN_DIR/dji-success.flag"
		result="PASS"
		collect_phone_state "after-pass"
		run_shell avd-route-and-bytes "rg -n 'route path resolved|onInitFinished|StartForwarding|dji-osmo-pocket-3|received [0-9]+ bytes|Unable|error|failed|1945|1946' '$RUN_DIR/avd.log' || true"
		printf 'RUN_DIR=%s\n' "$RUN_DIR"
		cat "$RUN_DIR/result.txt"
		exit 0
	fi

	sleep "$POLL_INTERVAL_SECONDS"
done

collect_phone_state "after-red"
run_shell avd-route-and-bytes "rg -n 'route path resolved|onInitFinished|StartForwarding|dji-osmo-pocket-3|received [0-9]+ bytes|Unable|error|failed|1945|1946' '$RUN_DIR/avd.log' || true"
printf '%s\n' "DJI_NORMAL_TOPOLOGY_FAIL_NO_AUDIO_VIDEO" >"$RUN_DIR/result.txt"
printf '%s\n' "1" >"$RUN_DIR/dji-fail.flag"
result="FAIL"
printf 'RUN_DIR=%s\n' "$RUN_DIR"
cat "$RUN_DIR/result.txt"
exit 30
