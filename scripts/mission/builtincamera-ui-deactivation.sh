#!/usr/bin/env bash
set -Eeuo pipefail

# Goal 3 — Wingout UI builtin camera Deactivate clean teardown.
#
# Pre-condition: builtincamera-ui-activation.sh just succeeded (BUILTINCAMERA_ACTIVATION_PASS).
# Mission: tap the Deactivate button, then prove via AVD endpoint integrity gates that:
#   1. /pixel/builtincamera-merged becomes empty/unavailable (publisher torn down)
#   2. /pixel/dji-osmo-pocket-3-merged still captures 10s of valid AV (DJI continuity preserved)
#
# Acceptance per Task #3 brief: "ffstream-camera disappears OR relaunches idle"; supervisor relaunch
# with idle daemon (no inputs, publishes nothing) is acceptable. The script is agnostic to which
# of the two paths the daemon takes — only the AVD-side observable matters (no publishing).
#
# Spec lineage: bash idiom + helper structure mirrors builtincamera-ui-activation.sh (Task #2 v20);
# AVD probe pattern mirrors goal1-normal-topology.sh capture_goal1_media.

PHONE_SERIAL="${PHONE_SERIAL:-41041JEKB08092}"
RUN_ROOT="${RUN_ROOT:-${HOME}/tmp/test-phone-deactivation-fix-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="${RUN_DIR:-$RUN_ROOT/builtincamera-ui-deactivation-$(date -u +%Y%m%dT%H%M%SZ)}"
FFSTREAMCTL_BIN="${FFSTREAMCTL_BIN:-/home/streaming/go/bin/ffstreamctl}"
CAMERA_CONTROL_FORWARD_PORT="${CAMERA_CONTROL_FORWARD_PORT:-23594}"
PHONE_CAMERA_CONTROL_PORT="${PHONE_CAMERA_CONTROL_PORT:-3594}"
WINGOUT_PACKAGE="${WINGOUT_PACKAGE:-center.dx.wingout}"
DEACTIVATION_POLL_COUNT="${DEACTIVATION_POLL_COUNT:-12}"
DEACTIVATION_POLL_INTERVAL_SECONDS="${DEACTIVATION_POLL_INTERVAL_SECONDS:-5}"
AVD_HOST="${AVD_HOST:-127.0.0.1}"
AVD_CONSUMER_PORT="${AVD_CONSUMER_PORT:-1945}"
BUILTIN_CAMERA_CONSUMER_URL="${BUILTIN_CAMERA_CONSUMER_URL:-rtmp://$AVD_HOST:$AVD_CONSUMER_PORT/pixel/builtincamera-merged}"
DJI_CAMERA_CONSUMER_URL="${DJI_CAMERA_CONSUMER_URL:-rtmp://$AVD_HOST:$AVD_CONSUMER_PORT/pixel/dji-osmo-pocket-3-merged}"
DJI_CAPTURE_SECONDS="${DJI_CAPTURE_SECONDS:-10}"
# Probe budget for the builtin-camera-emptiness check. Short timeout because the
# expected behavior is failure (route empty after deactivate); a healthy publisher
# would yield a ffprobe success well before this elapses.
BUILTIN_CAMERA_PROBE_TIMEOUT_SECONDS="${BUILTIN_CAMERA_PROBE_TIMEOUT_SECONDS:-8}"
BUILTIN_CAMERA_RW_TIMEOUT_USEC="${BUILTIN_CAMERA_RW_TIMEOUT_USEC:-5000000}"

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

dump_ui() {
	local label="$1"
	run_cmd "dump-$label" adb -s "$PHONE_SERIAL" shell uiautomator dump /sdcard/window.xml || true
	run_cmd "pull-$label" adb -s "$PHONE_SERIAL" pull /sdcard/window.xml "$RUN_DIR/uidump-$label.xml" || true
	if [ ! -s "$RUN_DIR/uidump-$label.xml" ]; then
		: >"$RUN_DIR/ui-targets-$label.txt"
		return 1
	fi
	python3 - "$RUN_DIR/uidump-$label.xml" >"$RUN_DIR/ui-targets-$label.txt" <<'PY'
import sys
import xml.etree.ElementTree as ET

root = ET.parse(sys.argv[1]).getroot()
for node in root.iter("node"):
    label = node.attrib.get("content-desc") or node.attrib.get("text") or ""
    if not label:
        continue
    print(
        f"{label!r}\t{node.attrib.get('bounds', '')}"
        f"\tenabled={node.attrib.get('enabled', '')}"
        f"\tclick={node.attrib.get('clickable', '')}"
        f"\tpackage={node.attrib.get('package', '')}"
    )
PY
}

deactivate_button_point_from_dump() {
	local dump_path="$1"
	python3 - "$dump_path" <<'PY'
import re
import sys
import xml.etree.ElementTree as ET

root = ET.parse(sys.argv[1]).getroot()
for node in root.iter("node"):
    label = node.attrib.get("content-desc") or node.attrib.get("text") or ""
    if label != "Deactivate":
        continue
    if node.attrib.get("clickable") != "true" or node.attrib.get("enabled") != "true":
        continue
    match = re.fullmatch(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]", node.attrib.get("bounds", ""))
    if not match:
        continue
    left, top, right, bottom = map(int, match.groups())
    x = (left + right) // 2
    y = top + max(1, (bottom - top) // 3)
    print(f"{x} {y} {node.attrib.get('bounds')}")
    sys.exit(0)
sys.exit(1)
PY
}

collect_process_window_state() {
	local label="$1"
	run_cmd "wingout-pid-$label" adb -s "$PHONE_SERIAL" shell pidof "$WINGOUT_PACKAGE" || true
	run_cmd "window-focus-$label" adb -s "$PHONE_SERIAL" shell dumpsys window || true
	run_cmd "activity-top-$label" adb -s "$PHONE_SERIAL" shell dumpsys activity top || true
}

collect_camera_state() {
	local label="$1"
	run_cmd "camera-inputs-$label" "$FFSTREAMCTL_BIN" \
		--remote-addr "127.0.0.1:$CAMERA_CONTROL_FORWARD_PORT" inputs info || true
	run_cmd "camera-pipelines-$label" "$FFSTREAMCTL_BIN" \
		--remote-addr "127.0.0.1:$CAMERA_CONTROL_FORWARD_PORT" pipelines get || true
}

# UI state classifiers — mirror activation script's wingout_window_is_live etc.
wingout_window_is_live() {
	local label="$1"
	[ -s "$RUN_DIR/wingout-pid-$label.stdout" ] || return 1
	rg -q "mCurrentFocus=.*$WINGOUT_PACKAGE|mFocusedApp=.*$WINGOUT_PACKAGE" \
		"$RUN_DIR/window-focus-$label.stdout"
}

# Status-row label drives the success signal. The Wingout UI shows
# "Status: Inactive" once _commitDeactivate fires (the post-Deactivate-success
# terminal callback per CamerasBuiltin.qml _doDeactivate flow).
ui_shows_inactive_status() {
	local label="$1"
	[ -s "$RUN_DIR/uidump-$label.xml" ] || return 1
	rg -q "text=\"Status: Inactive\"|content-desc=\"Status: Inactive\"" "$RUN_DIR/uidump-$label.xml"
}

ui_shows_active_status() {
	local label="$1"
	[ -s "$RUN_DIR/uidump-$label.xml" ] || return 1
	rg -q "text=\"Status: Active\"|content-desc=\"Status: Active\"" "$RUN_DIR/uidump-$label.xml"
}

deactivate_dialog_visible() {
	local label="$1"
	[ -s "$RUN_DIR/uidump-$label.xml" ] || return 1
	rg -q "Deactivate failed|stop command, but it did not complete cleanly|Operation timed out|Camera daemon stopped" \
		"$RUN_DIR/uidump-$label.xml"
}

# AVD endpoint probes.
#
# probe_builtin_camera_empty: ffprobe against /pixel/builtincamera-merged with a short
# rw_timeout. Expected outcome AFTER a clean Deactivate is failure (no streams) — that's
# the signal of a successful teardown. Returns 0 if route is empty/unavailable; 1 if
# route still publishes (Deactivate did not propagate).
probe_builtin_camera_empty() {
	local label="$1"
	local out_dir="$RUN_DIR/$label"
	mkdir -p "$out_dir"
	set +e
	timeout "$BUILTIN_CAMERA_PROBE_TIMEOUT_SECONDS" ffprobe \
		-v error \
		-rw_timeout "$BUILTIN_CAMERA_RW_TIMEOUT_USEC" \
		-show_entries stream=index,codec_type \
		-of json \
		"$BUILTIN_CAMERA_CONSUMER_URL" >"$out_dir/builtin-ffprobe.stdout" 2>"$out_dir/builtin-ffprobe.stderr"
	local probe_status="$?"
	set -e
	printf '%s\n' "$probe_status" >"$out_dir/builtin-ffprobe.status"

	# ffprobe failure (exit ≠ 0) OR empty streams array → route is empty/unavailable.
	if [ "$probe_status" -ne 0 ]; then
		return 0
	fi
	python3 - "$out_dir/builtin-ffprobe.stdout" <<'PY'
import json
import sys

try:
    with open(sys.argv[1], encoding="utf-8") as f:
        data = json.load(f)
except Exception:
    sys.exit(0)  # parse failure = treat as empty
streams = data.get("streams") or []
sys.exit(1 if streams else 0)  # non-empty = still publishing (FAIL)
PY
}

# probe_dji_continuity: ffprobe + ffmpeg capture against /pixel/dji-osmo-pocket-3-merged.
# Mirrors goal1-normal-topology.sh capture_goal1_media for integrity-gate format. Returns
# 0 if 10s capture has valid h264 + aac with frame counts in the 30fps/48kHz expected
# ranges (250-600 video frames, 300-700 audio frames). Returns 1 on any gate failure.
probe_dji_continuity() {
	local label="$1"
	local out_dir="$RUN_DIR/$label"
	mkdir -p "$out_dir"
	local capture_media="$out_dir/dji-merged-${DJI_CAPTURE_SECONDS}s.mkv"

	# Live ffprobe — confirms route is publishing right now.
	set +e
	timeout 20 ffprobe \
		-v error \
		-rw_timeout 8000000 \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels \
		-of json \
		"$DJI_CAMERA_CONSUMER_URL" >"$out_dir/dji-live-ffprobe.stdout" 2>"$out_dir/dji-live-ffprobe.stderr"
	local live_status="$?"
	set -e
	printf '%s\n' "$live_status" >"$out_dir/dji-live-ffprobe.status"
	if [ "$live_status" -ne 0 ]; then
		return 1
	fi

	# Capture 10s.
	set +e
	timeout "$((DJI_CAPTURE_SECONDS + 25))" ffmpeg \
		-hide_banner \
		-y \
		-rw_timeout 8000000 \
		-t "$DJI_CAPTURE_SECONDS" \
		-i "$DJI_CAMERA_CONSUMER_URL" \
		-map 0:v:0 \
		-map 0:a:0 \
		-c copy \
		"$capture_media" >"$out_dir/dji-capture.stdout" 2>"$out_dir/dji-capture.stderr"
	local capture_status="$?"
	set -e
	printf '%s\n' "$capture_status" >"$out_dir/dji-capture.status"
	if [ "$capture_status" -ne 0 ] || [ ! -s "$capture_media" ]; then
		return 1
	fi

	# Frame-count probe.
	set +e
	timeout 20 ffprobe \
		-v error \
		-count_frames \
		-show_entries stream=index,codec_type,codec_name,width,height,r_frame_rate,sample_rate,channels,nb_read_frames,duration \
		-of json \
		"$capture_media" >"$out_dir/dji-capture-ffprobe.stdout" 2>"$out_dir/dji-capture-ffprobe.stderr"
	local probe_status="$?"
	set -e
	printf '%s\n' "$probe_status" >"$out_dir/dji-capture-ffprobe.status"
	if [ "$probe_status" -ne 0 ]; then
		return 1
	fi

	# Validate codec + frame-count gates.
	python3 - "$out_dir/dji-capture-ffprobe.stdout" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    data = json.load(f)
streams = data.get("streams") or []

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
# 10s @ 30fps video, 48kHz audio in standard encoder packing — same gates as goal1.
if not (250 <= video_frames <= 600):
    sys.exit(1)
if not (300 <= audio_frames <= 700):
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
	printf '%s\n' "$RUN_DIR" >"$RUN_ROOT/latest-builtincamera-ui-deactivation-run.txt"

	date -u +%Y-%m-%dT%H:%M:%SZ >"$RUN_DIR/started-at.txt"
	printf 'PHONE_SERIAL=%s\n' "$PHONE_SERIAL" >"$RUN_DIR/config.txt"
	printf 'RUN_DIR=%s\n' "$RUN_DIR" >>"$RUN_DIR/config.txt"
	printf 'BUILTIN_CAMERA_CONSUMER_URL=%s\n' "$BUILTIN_CAMERA_CONSUMER_URL" >>"$RUN_DIR/config.txt"
	printf 'DJI_CAMERA_CONSUMER_URL=%s\n' "$DJI_CAMERA_CONSUMER_URL" >>"$RUN_DIR/config.txt"

	run_cmd adb-forward-camera-control adb -s "$PHONE_SERIAL" forward \
		"tcp:$CAMERA_CONTROL_FORWARD_PORT" "tcp:$PHONE_CAMERA_CONTROL_PORT" || true

	# Pre-condition: wingout in foreground + Status:Active visible.
	dump_ui "before-deactivate" || true
	collect_process_window_state "before-deactivate"
	collect_camera_state "before-deactivate"

	if ! wingout_window_is_live "before-deactivate"; then
		finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_WINGOUT_NOT_FOREGROUND_BEFORE_TAP" 50
	fi
	if ! ui_shows_active_status "before-deactivate"; then
		finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_NOT_ACTIVE_BEFORE_TAP" 51
	fi

	if ! deactivate_button_point_from_dump "$RUN_DIR/uidump-before-deactivate.xml" >"$RUN_DIR/deactivate-target.txt"; then
		finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_NO_ENABLED_DEACTIVATE_BUTTON" 52
	fi

	local deactivate_x deactivate_y deactivate_bounds tap_status
	read -r deactivate_x deactivate_y deactivate_bounds <"$RUN_DIR/deactivate-target.txt"
	printf 'adb -s %q shell input tap %q %q\n' "$PHONE_SERIAL" "$deactivate_x" "$deactivate_y" >"$RUN_DIR/tap-deactivate.cmd"
	set +e
	adb -s "$PHONE_SERIAL" shell input tap "$deactivate_x" "$deactivate_y" \
		>"$RUN_DIR/tap-deactivate.stdout" 2>"$RUN_DIR/tap-deactivate.stderr"
	tap_status="$?"
	set -e
	printf '%s\n' "$tap_status" >"$RUN_DIR/tap-deactivate.status"
	printf '%s\n' "$deactivate_bounds" >"$RUN_DIR/deactivate-bounds.txt"

	# Poll for "Status: Inactive" — the terminal commit-deactivate signal.
	local i
	for i in $(seq 1 "$DEACTIVATION_POLL_COUNT"); do
		sleep "$DEACTIVATION_POLL_INTERVAL_SECONDS"
		dump_ui "after-$i" || true
		collect_process_window_state "after-$i"
		collect_camera_state "after-$i"

		if deactivate_dialog_visible "after-$i"; then
			finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_DIALOG" 53
		fi

		if ! wingout_window_is_live "after-$i"; then
			finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_WINGOUT_NOT_FOREGROUND_AFTER_TAP" 54
		fi

		if ui_shows_inactive_status "after-$i"; then
			break
		fi
	done

	if ! ui_shows_inactive_status "after-$i"; then
		finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_STATUS_NEVER_INACTIVE" 55
	fi

	# AVD-side observable gates — both must hold for PASS:
	# Gate 1: /pixel/builtincamera-merged empty/unavailable (deactivate teardown propagated).
	# Gate 2: /pixel/dji-osmo-pocket-3-merged still publishes valid 10s AV (DJI continuity).
	if ! probe_builtin_camera_empty "avd-builtin-empty"; then
		finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_BUILTIN_STILL_PUBLISHING" 56
	fi
	if ! probe_dji_continuity "avd-dji-continuity"; then
		finish_with_result "BUILTINCAMERA_DEACTIVATION_FAIL_DJI_CONTINUITY_BROKEN" 57
	fi

	finish_with_result "BUILTINCAMERA_DEACTIVATION_PASS" 0
}

# Sourcability guard — allows test fixtures to source helpers without triggering main flow.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
	main "$@"
fi
