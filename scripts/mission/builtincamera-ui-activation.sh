#!/usr/bin/env bash
set -Eeuo pipefail

PHONE_SERIAL="${PHONE_SERIAL:-41041JEKB08092}"
RUN_ROOT="${RUN_ROOT:-${HOME}/tmp/test-phone-activation-fix-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="${RUN_DIR:-$RUN_ROOT/builtincamera-ui-activation-$(date -u +%Y%m%dT%H%M%SZ)}"
FFSTREAMCTL_BIN="${FFSTREAMCTL_BIN:-/home/streaming/go/bin/ffstreamctl}"
CAMERA_CONTROL_FORWARD_PORT="${CAMERA_CONTROL_FORWARD_PORT:-23594}"
PHONE_CAMERA_CONTROL_PORT="${PHONE_CAMERA_CONTROL_PORT:-3594}"
WINGOUT_PACKAGE="${WINGOUT_PACKAGE:-center.dx.wingout}"
WINGOUT_PGREP_PATTERN="${WINGOUT_PGREP_PATTERN:-$WINGOUT_PACKAGE}"
FFSTREAM_PGREP_PATTERN="${FFSTREAM_PGREP_PATTERN:-^ffstream$}"
ACTIVATION_POLL_COUNT="${ACTIVATION_POLL_COUNT:-12}"
ACTIVATION_POLL_INTERVAL_SECONDS="${ACTIVATION_POLL_INTERVAL_SECONDS:-5}"
ACTIVATION_STABILITY_SECONDS="${ACTIVATION_STABILITY_SECONDS:-60}"
ACTIVATION_STABILITY_POLL_SECONDS="${ACTIVATION_STABILITY_POLL_SECONDS:-5}"
AVD_HOST="${AVD_HOST:-192.168.141.16}"
AVD_CONSUMER_PORT="${AVD_CONSUMER_PORT:-1945}"
BUILTIN_CAMERA_CONSUMER_URL="${BUILTIN_CAMERA_CONSUMER_URL:-rtmp://$AVD_HOST:$AVD_CONSUMER_PORT/pixel/builtincamera-merged}"

# v20 §3.1.7.1 CLASSIFICATION_PATH state machine — explicit empty default = "direct" semantics.
# Valid values: "" (direct) | "fallback" (catch-all override engaged per §3.1.7).
# All mutators MUST be invoked in main-shell scope (NOT via $(...)) per v18 EXTENSION:
#   - classify_via_fallback (§3.1.7.1)
#   - handle_unknown_disambiguation (§6.2.4 — Both-DEAD + Both-ALIVE branches)
CLASSIFICATION_PATH=""

# v20 §3.1.3.5 tee-process tracking PID (Path A only). Unset/empty = Path B (process-substitution).
# Production deployments default to Path B for simplicity; Path A reserved for future
# tee-from-cycle-start integration (Stop #28 v20 4-row matrix preferring Path A under uncertainty).
CYCLE_LOGCAT_TEE_PID=""

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

button_point_from_dump() {
	local dump_path="$1"
	python3 - "$dump_path" <<'PY'
import re
import sys
import xml.etree.ElementTree as ET

root = ET.parse(sys.argv[1]).getroot()
for node in root.iter("node"):
    label = node.attrib.get("content-desc") or node.attrib.get("text") or ""
    if label not in ("Activate", "Re-Activate"):
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

collect_logcat_window() {
	local label="$1"
	run_cmd "logcat-$label" timeout 60 adb -s "$PHONE_SERIAL" logcat -d || true
	run_shell "logcat-relevant-$label" \
		"rg -n 'lowmemorykiller|lmkd|Kill .*center\\.dx\\.wingout|Kill .*ffstream|Out of memory: Killed process .*ffstream|center\\.dx\\.wingout|WINDOW DIED|InputDispatcher|Activation failed|SwitchOutput|crash_dump64|F DEBUG|android\\.hardware\\.camera\\.provider|Camera provider .* has died|GraphRunner watchdog|CameraService: Stop camera streaming|bootstat|Canonical boot reason|Normalized last reboot reason|Booting Linux|kernel_panic|Restarting system|watchdog|panic|BUG:' '$RUN_DIR/logcat-$label.stdout' || true"
}

logcat_has_lmkd_activation_failure() {
	local label="$1"
	rg -q "lowmemorykiller: Kill 'center\.dx\.wingout'|lowmemorykiller: Kill 'ffstream'|Out of memory: Killed process .*ffstream|cpuset=camera-daemon|Process center\.dx\.wingout .* has died|WINDOW DIED.*center\.dx\.wingout" \
		"$RUN_DIR/logcat-relevant-$label.stdout"
}

logcat_has_camera_provider_failure() {
	local label="$1"
	rg -q "android\.hardware\.camera\.provider|Camera provider .* has died|GraphRunner watchdog|CameraService: Stop camera streaming" \
		"$RUN_DIR/logcat-relevant-$label.stdout"
}

adb_loss_detected() {
	local label="$1"
	local name
	for name in \
		"dump-$label" \
		"pull-$label" \
		"wingout-pid-$label" \
		"window-focus-$label" \
		"activity-top-$label"; do
		[ -s "$RUN_DIR/$name.stderr" ] || continue
		rg -q "device '$PHONE_SERIAL' not found|no devices/emulators found|failed to get feature set|device offline|device unauthorized" \
			"$RUN_DIR/$name.stderr" && return 0
	done
	return 1
}

device_reboot_or_offline_detected() {
	local state_label="$1"
	local logcat_label="$2"

	adb_loss_detected "$state_label" || return 1
	[ -s "$RUN_DIR/logcat-$logcat_label.stderr" ] &&
		rg -q "waiting for device|device '$PHONE_SERIAL' not found|no devices/emulators found|failed to get feature set" \
			"$RUN_DIR/logcat-$logcat_label.stderr" &&
		return 0
	[ -s "$RUN_DIR/logcat-relevant-$logcat_label.stdout" ] &&
		rg -q "Canonical boot reason|Normalized last reboot reason|Booting Linux|kernel_panic|Restarting system|watchdog|panic|BUG:" \
			"$RUN_DIR/logcat-relevant-$logcat_label.stdout" &&
		return 0
	return 1
}

wingout_window_is_live() {
	local label="$1"
	[ -s "$RUN_DIR/wingout-pid-$label.stdout" ] || return 1
	rg -q "mCurrentFocus=.*$WINGOUT_PACKAGE|mFocusedApp=.*$WINGOUT_PACKAGE" \
		"$RUN_DIR/window-focus-$label.stdout"
}

activation_dialog_visible() {
	local label="$1"
	[ -s "$RUN_DIR/uidump-$label.xml" ] || return 1
	rg -q "Activation failed|SwitchOutputByProps|could not be activated" "$RUN_DIR/uidump-$label.xml"
}

inputs_have_required_capture_devices() {
	local inputs_path="$1"
	python3 - "$inputs_path" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as input_file:
    data = json.load(input_file)

has_camera = False
has_microphone = False
for input_info in data.get("inputs", []):
    custom_options = {
        option.get("key"): option.get("value")
        for option in input_info.get("input_config", {}).get("custom_options", [])
    }
    if not input_info.get("is_active"):
        continue
    if custom_options.get("f") == "android_camera":
        if custom_options.get("video_size") == "1920x1920" and custom_options.get("framerate") == "30":
            has_camera = True
        continue
    if custom_options.get("f") == "android_microphone":
        if custom_options.get("sample_rate") == "48000":
            has_microphone = True
        continue

sys.exit(0 if has_camera and has_microphone else 1)
PY
}

seed_streaming_settings() {
	local existing_settings="$RUN_DIR/streaming-settings-before.json"
	local generated_settings="$RUN_DIR/streaming-settings-generated.json"
	local readback_settings="$RUN_DIR/streaming-settings-readback.json"
	local remote_settings="/data/local/tmp/${WINGOUT_PACKAGE}-streaming-settings-$(basename "$RUN_DIR").json"

	set +e
	adb -s "$PHONE_SERIAL" shell run-as "$WINGOUT_PACKAGE" cat cache/streaming_settings.json \
		>"$existing_settings" 2>"$RUN_DIR/read-streaming-settings-before.stderr"
	local read_status="$?"
	set -e
	printf '%s\n' "$read_status" >"$RUN_DIR/read-streaming-settings-before.status"
	if [ "$read_status" -ne 0 ]; then
		printf '{}\n' >"$existing_settings"
	fi

	BUILTIN_CAMERA_CONSUMER_URL="$BUILTIN_CAMERA_CONSUMER_URL" \
		SETTINGS_INPUT="$existing_settings" \
		SETTINGS_OUTPUT="$generated_settings" \
		python3 <<'PY'
import datetime
import json
import os

input_path = os.environ["SETTINGS_INPUT"]
output_path = os.environ["SETTINGS_OUTPUT"]
output_url = os.environ["BUILTIN_CAMERA_CONSUMER_URL"]

try:
    with open(input_path, encoding="utf-8") as input_file:
        data = json.load(input_file)
except Exception:
    data = {}

if not isinstance(data, dict):
    data = {}

data.update(
    {
        "settingsSchemaVersion": 2,
        "width": 1920,
        "height": 1920,
        "fps": 30,
        "bitrateKbps": 8000,
        "maxBitrateKbps": 12000,
        "videoCodec": "av1_mediacodec",
        "audioCodec": "aac",
        "audioSampleRate": 48000,
        "audioBitrateKbps": 64,
        "audioChannels": 1,
        "outputUrl": output_url,
        "preferredCamera": data.get("preferredCamera") or "Front",
        "preferredMicrophoneId": int(data.get("preferredMicrophoneId") or 0),
        "timestampUtc": datetime.datetime.now(datetime.UTC)
        .replace(microsecond=0)
        .isoformat()
        .replace("+00:00", "Z"),
    }
)

with open(output_path, "w", encoding="utf-8") as output_file:
    json.dump(data, output_file, indent=4, sort_keys=True)
    output_file.write("\n")
PY

	run_cmd push-streaming-settings adb -s "$PHONE_SERIAL" push "$generated_settings" "$remote_settings"
	run_cmd chmod-streaming-settings adb -s "$PHONE_SERIAL" shell chmod 0644 "$remote_settings" || true
	run_cmd ensure-settings-dir adb -s "$PHONE_SERIAL" shell run-as "$WINGOUT_PACKAGE" mkdir -p cache
	run_cmd install-streaming-settings adb -s "$PHONE_SERIAL" shell run-as "$WINGOUT_PACKAGE" \
		cp "$remote_settings" cache/streaming_settings.json
	run_cmd remove-temp-streaming-settings adb -s "$PHONE_SERIAL" shell rm -f "$remote_settings" || true
	run_cmd read-streaming-settings-after adb -s "$PHONE_SERIAL" shell run-as "$WINGOUT_PACKAGE" \
		cat cache/streaming_settings.json
	cp "$RUN_DIR/read-streaming-settings-after.stdout" "$readback_settings"

	BUILTIN_CAMERA_CONSUMER_URL="$BUILTIN_CAMERA_CONSUMER_URL" \
		python3 - "$readback_settings" <<'PY'
import json
import os
import sys

with open(sys.argv[1], encoding="utf-8") as input_file:
    data = json.load(input_file)

expected_url = os.environ["BUILTIN_CAMERA_CONSUMER_URL"]
assert data.get("outputUrl") == expected_url, data.get("outputUrl")
assert data.get("width") == 1920, data.get("width")
assert data.get("height") == 1920, data.get("height")
assert data.get("fps") == 30, data.get("fps")
assert data.get("videoCodec") == "av1_mediacodec", data.get("videoCodec")
assert data.get("audioSampleRate") == 48000, data.get("audioSampleRate")
PY
}

camera_pipeline_has_required_outputs() {
	local label="$1"
	local inputs_path="$RUN_DIR/camera-inputs-$label.stdout"
	local pipelines_path="$RUN_DIR/camera-pipelines-$label.stdout"

	inputs_have_required_capture_devices "$inputs_path" || return 1
	rg -q "NaiveEncoderFactory\\(av1_mediacodec/\\)" "$pipelines_path" || return 1
	rg -q "NaiveEncoderFactory\\(/aac\\)" "$pipelines_path" || return 1
	rg -q "Output\\([^)]*builtincamera-av1-1920/\\)" "$pipelines_path" || return 1
	rg -q "Output\\([^)]*builtincamera-aac-48000/\\)" "$pipelines_path" || return 1
}

# =============================================================================
# v20 IF1 RULING SPEC — heuristic-tighten classifier helpers
#
# Per §3.3 v20 fail-code → bucket-mapping:
#   40 WINGOUT_NOT_FOREGROUND_BEFORE_TAP → T1.3 (in-cycle) | T0.4 (pre-cycle)
#   41 NO_ENABLED_ACTIVATE_BUTTON         → T1.3
#   42 DIALOG                              → T1.3
#   43 LMKD                                → T1 catch-all (legacy bucket; not in v20 §3.3 explicit table)
#   44 WINGOUT_NOT_FOREGROUND_AFTER_TAP    → T1.1
#   45 NO_CAMERA_OUTPUTS                   → T1.2
#   46 DEVICE_REBOOT_OR_OFFLINE            → T0.2a
#   47 CAMERA_PROVIDER                     → T2.7 (default; MEDIUM-confidence)
#   48 STREAM_TEARDOWN                     → T2.7 (default; MEDIUM-confidence)
#
# Per Stop #28 v20 4-row matrix: this implementation defaults to Path B
# (process-substitution semantics — adb logcat -d one-shot per cycle, no tee
# process). CYCLE_LOGCAT_TEE_PID stays empty for the lifetime of the run.
# Path A reserved for future tee-from-cycle-start integration when log volume
# crosses 10 events/s sustained AND cycle ≥60s.
# =============================================================================

# §3.1.2 v18 — 3-Sample Liveness Polling, skip-final-sleep idiom.
# Polls pgrep at T=0, T=0.5s, T=1.0s (3 samples, 1-second window total).
# Returns 0 (ALIVE) if all 3 samples find process; returns 1 (DEAD) on first miss.
# Wall-clock = 1.0s on ALIVE path (NOT 1.5s — final-iteration sleep is skipped
# per `[ "$i" -lt 3 ] && sleep 0.5`, matching the stated cadence rationale).
pgrep_3sample_any_death_wins() {
	local pattern="$1"
	local i
	for i in 1 2 3; do
		if ! pgrep -f "$pattern" > /dev/null 2>&1; then
			return 1
		fi
		# Skip sleep after final sample so wall-clock matches T=0/0.5/1.0 cadence.
		[ "$i" -lt 3 ] && sleep 0.5
	done
	return 0
}

# §3.1.5 PID-continuity intersection check.
# Given saved cycle-start PID set + current pgrep pattern, returns 0 (ALIVE) iff
# at least one PID from the saved set is still present in the current pgrep result
# (intersection non-empty); returns 1 (DEAD) otherwise.
#
# Empty saved set → return 1 (no cycle-start anchor; all-cycle-deaths cannot
# be masked as ALIVE).
pid_continuity_check_intersection() {
	local saved="$1"
	local pattern="$2"
	local current_pids
	[ -n "$saved" ] && [ -n "${saved// /}" ] || return 1
	current_pids="$(pgrep -f "$pattern" 2>/dev/null | sort -n | tr '\n' ' ' || true)"
	[ -n "$current_pids" ] || return 1
	# Intersection: any PID in saved that also appears in current.
	local pid
	for pid in $saved; do
		# shellcheck disable=SC2076
		if [[ " $current_pids " =~ " $pid " ]]; then
			return 0
		fi
	done
	return 1
}

# §3.1.3.5 v20 — Logcat finalization protocol (two-tier nested-if).
#
# Three behavioral cases:
#   Path A normal (CYCLE_LOGCAT_TEE_PID set, tee alive):
#       kill -TERM + timeout-bounded wait + sync + sleep
#   Path A edge   (CYCLE_LOGCAT_TEE_PID set, tee crashed mid-cycle):
#       sync + sleep only (defensive flush; preserves v17/v18 behavior)
#   Path B        (CYCLE_LOGCAT_TEE_PID unset, process-substitution active):
#       full no-op (outer if fails)
#
# v15: timeout-bounded wait prevents indefinite hang on stuck adb daemon.
# v19 introduced compound-`&&` scope-creep regression on Path A edge; v20
# fixes via two-tier nested-if so sync+sleep run regardless of inner tee state.
finalize_cycle_logcat() {
	if [ -n "$CYCLE_LOGCAT_TEE_PID" ]; then
		if kill -0 "$CYCLE_LOGCAT_TEE_PID" 2>/dev/null; then
			kill -TERM "$CYCLE_LOGCAT_TEE_PID" 2>/dev/null || true
			# 2s timeout envelope: bounded wait avoids indefinite hang.
			timeout 2 wait "$CYCLE_LOGCAT_TEE_PID" 2>/dev/null || true
		fi
		# Defensive flush — runs regardless of tee alive/crashed state.
		# sync(8) flushes kernel buffers; meaningful when tee emitted data.
		sync
		# Cross-filesystem stabilization buffer (per F-v13-4); 0.2s suffices
		# for cross-process coherence on slow filesystems.
		sleep 0.2
	fi
	# Path B: outer if fails → full no-op preserved.
}

# §3.1 v17 — Triple-source either-DEAD-wins combiner (explicit if/then/else
# per NEW-NIT-V15-VERDICT-IDIOM-FRAGILE; eliminates `(... && echo X || echo Y)`
# parse-trap surface). Echoes "DEAD" or "ALIVE" on stdout.
triple_source_either_dead_wins() {
	local pgrep_verdict="$1"
	local logcat_verdict="$2"
	local pidcont_verdict="$3"
	if [ "$pgrep_verdict" = "DEAD" ] || \
	   [ "$logcat_verdict" = "DEAD" ] || \
	   [ "$pidcont_verdict" = "DEAD" ]; then
		printf '%s\n' "DEAD"
	else
		printf '%s\n' "ALIVE"
	fi
}

# §3.1.7.1 v17 — Catch-all override → CLASSIFICATION_PATH=fallback.
# MUST be invoked in main-shell scope (NOT via $(...) per main-shell scope
# warning); subshell-scoped invocation loses the assignment.
classify_via_fallback() {
	CLASSIFICATION_PATH="fallback"
}

# §6.2.4 helper — route the unattributable-cycle-end case to T2.7 fallback.
# Called from handle_unknown_disambiguation Both-DEAD/Both-ALIVE branches.
route_to_t27_spontaneous_context_dump() {
	finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_STREAM_TEARDOWN" 48
}

# §6.2.4 v18 — Both-ALIVE branch MUST emit log warning before fallback routing.
# Pattern: classify by 4-state W,F verdict combination.
#   W=DEAD,F=ALIVE → wingout-only failure (route via §6.2.1 patterns; classify_runtime_teardown)
#   W=ALIVE,F=DEAD → ffstream-only failure (T2.5 OOM_KILL or T2.x catch-all)
#   W=DEAD,F=DEAD  → both-DEAD without disambig timestamps → T2.7 fallback
#   W=ALIVE,F=ALIVE → should-not-occur (caller invariant violation); log_warn + T2.7 fallback
#
# All CLASSIFICATION_PATH mutations happen in main-shell scope (per §3.1.7.1
# v18 EXTENSION); helper MUST NOT be invoked via $(...) command substitution.
handle_unknown_disambiguation() {
	if [ "${W_VERDICT:-}" = "DEAD" ] && [ "${F_VERDICT:-}" = "ALIVE" ]; then
		# Wingout-only death; route via existing classifier chain.
		classify_runtime_teardown "${1:-after-handle-unknown}" "${2:-after-handle-unknown}"
	elif [ "${F_VERDICT:-}" = "DEAD" ] && [ "${W_VERDICT:-}" = "ALIVE" ]; then
		# Ffstream-only death; T2.5 OOM_KILL most common — route via fallback for now.
		# Future: dedicated OOM-classification path if logcat shows "Out of memory: Killed process".
		classify_via_fallback
		route_to_t27_spontaneous_context_dump
	elif [ "${W_VERDICT:-}" = "DEAD" ] && [ "${F_VERDICT:-}" = "DEAD" ]; then
		# Both-DEAD: timestamps unavailable for ordering → §3.1.7 catch-all → T2.7.
		classify_via_fallback
		route_to_t27_spontaneous_context_dump
	else
		# Both-ALIVE: caller invariant violation. v18 fix (closes F-v16-2):
		# explicit log statement satisfies "log warning" comment contract.
		echo "WARN: handle_unknown_disambiguation reached with W=ALIVE,F=ALIVE — should not occur" >&2
		classify_via_fallback
		route_to_t27_spontaneous_context_dump
	fi
}

# Compute per-target W/F verdicts via triple-source either-DEAD-wins.
# Sets globals W_VERDICT, F_VERDICT (values: ALIVE | DEAD).
#
# Sources (per §3.1):
#   pgrep 3-sample (§3.1.2)         — T=0/0.5/1.0 polling, 1s window
#   logcat death-event (§3.1.3)     — pattern grep over finalized logcat file
#   PID-continuity intersection     — saved-PID-set ∩ current-pgrep non-empty
#
# CAVEAT (host-vs-phone asymmetry not yet handled by spec — flagged for v21):
# This script runs on the dev host but wingout lives on the phone (adb-shell-side).
# Host pgrep cannot find wingout; the helper would always return W=DEAD on this
# topology. Wiring into main() would BREAK existing test/runtime expectations
# (which use wingout_window_is_live via mocked adb pidof+dumpsys).
#
# Helper is preserved as testable v20-spec-compliant unit, NOT yet wired into
# main flow. Callers must set WINGOUT_PIDS_AT_T0 + FFSTREAM_PIDS_AT_T0 globals
# (and arrange for $WINGOUT_PGREP_PATTERN / $FFSTREAM_PGREP_PATTERN to resolve
# via the appropriate process-table source) before invocation.
compute_target_verdicts() {
	local logcat_label="$1"
	local W_PGREP F_PGREP W_LOGCAT F_LOGCAT W_PIDCONT F_PIDCONT

	# Step 1: PID-continuity intersection (per §3.1.5).
	if pid_continuity_check_intersection "${WINGOUT_PIDS_AT_T0:-}" "$WINGOUT_PGREP_PATTERN"; then
		W_PIDCONT=ALIVE
	else
		W_PIDCONT=DEAD
	fi
	if pid_continuity_check_intersection "${FFSTREAM_PIDS_AT_T0:-}" "$FFSTREAM_PGREP_PATTERN"; then
		F_PIDCONT=ALIVE
	else
		F_PIDCONT=DEAD
	fi

	# Step 2: pgrep 3-sample either-DEAD-wins (per §3.1.2).
	if pgrep_3sample_any_death_wins "$WINGOUT_PGREP_PATTERN"; then
		W_PGREP=ALIVE
	else
		W_PGREP=DEAD
	fi
	if pgrep_3sample_any_death_wins "$FFSTREAM_PGREP_PATTERN"; then
		F_PGREP=ALIVE
	else
		F_PGREP=DEAD
	fi

	# Step 3 (v18): finalize_cycle_logcat MUST run before §3.1.3 helpers per Stop #28.
	# Path B (default this script): no-op via finalize_cycle_logcat outer-if early exit.
	finalize_cycle_logcat

	# Step 4: Logcat-event detection (per §3.1.3).
	if logcat_has_wingout_death_event "$logcat_label"; then
		W_LOGCAT=DEAD
	else
		W_LOGCAT=ALIVE
	fi
	if logcat_has_ffstream_death_event "$logcat_label"; then
		F_LOGCAT=DEAD
	else
		F_LOGCAT=ALIVE
	fi

	# Step 5: Triple-source either-DEAD-wins combine.
	W_VERDICT="$(triple_source_either_dead_wins "$W_PGREP" "$W_LOGCAT" "$W_PIDCONT")"
	F_VERDICT="$(triple_source_either_dead_wins "$F_PGREP" "$F_LOGCAT" "$F_PIDCONT")"
}

# §3.1.3 logcat death-event detectors per target.
# Path B (process-substitution semantics): reads from $RUN_DIR/logcat-relevant-$label.stdout
# which was populated by collect_logcat_window. Returns 0 if death-event pattern present.
logcat_has_wingout_death_event() {
	local label="$1"
	local path="$RUN_DIR/logcat-relevant-$label.stdout"
	[ -s "$path" ] || return 1
	rg -q "Process center\.dx\.wingout .* has died|WINDOW DIED.*center\.dx\.wingout|lowmemorykiller: Kill 'center\.dx\.wingout'|crash_dump64.*center\.dx\.wingout|F DEBUG.*center\.dx\.wingout" \
		"$path"
}

logcat_has_ffstream_death_event() {
	local label="$1"
	local path="$RUN_DIR/logcat-relevant-$label.stdout"
	[ -s "$path" ] || return 1
	rg -q "Out of memory: Killed process .*ffstream|lowmemorykiller: Kill 'ffstream'|crash_dump64.*ffstream|F DEBUG.*ffstream" \
		"$path"
}

# §6.2.1 evidence-based runtime-teardown classifier (preserved from pre-v20 baseline).
# Used both directly from main-loop's verify_camera_stream_stability and from
# handle_unknown_disambiguation's W=DEAD,F=ALIVE branch.
classify_runtime_teardown() {
	local state_label="$1"
	local logcat_label="$2"

	if device_reboot_or_offline_detected "$state_label" "$logcat_label"; then
		finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_DEVICE_REBOOT_OR_OFFLINE" 46
	fi
	if logcat_has_lmkd_activation_failure "$logcat_label"; then
		finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_LMKD" 43
	fi
	if logcat_has_camera_provider_failure "$logcat_label"; then
		finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_CAMERA_PROVIDER" 47
	fi
	finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_STREAM_TEARDOWN" 48
}

verify_camera_stream_stability() {
	local success_label="$1"

	if [ "$ACTIVATION_STABILITY_SECONDS" -le 0 ]; then
		return 0
	fi

	local polls=$(((ACTIVATION_STABILITY_SECONDS + ACTIVATION_STABILITY_POLL_SECONDS - 1) / ACTIVATION_STABILITY_POLL_SECONDS))
	local i
	for i in $(seq 1 "$polls"); do
		sleep "$ACTIVATION_STABILITY_POLL_SECONDS"
		local label="stable-$i"
		dump_ui "$label" || true
		collect_process_window_state "$label"
		collect_camera_state "$label"

		if activation_dialog_visible "$label"; then
			collect_logcat_window "after-stability-dialog-$i"
			finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_DIALOG" 42
		fi

		if ! wingout_window_is_live "$label"; then
			collect_logcat_window "after-stability-wingout-not-live-$i"
			classify_runtime_teardown "$label" "after-stability-wingout-not-live-$i"
		fi

		if ! camera_pipeline_has_required_outputs "$label"; then
			collect_logcat_window "after-stability-stream-missing-$i"
			classify_runtime_teardown "$label" "after-stability-stream-missing-$i"
		fi
	done

	cp "$RUN_DIR/camera-inputs-$success_label.stdout" "$RUN_DIR/camera-inputs-first-success.stdout" || true
	cp "$RUN_DIR/camera-pipelines-$success_label.stdout" "$RUN_DIR/camera-pipelines-first-success.stdout" || true
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
	printf '%s\n' "$RUN_DIR" >"$RUN_ROOT/latest-builtincamera-ui-activation-run.txt"

	date -u +%Y-%m-%dT%H:%M:%SZ >"$RUN_DIR/started-at.txt"
	printf 'PHONE_SERIAL=%s\n' "$PHONE_SERIAL" >"$RUN_DIR/config.txt"
	printf 'RUN_DIR=%s\n' "$RUN_DIR" >>"$RUN_DIR/config.txt"
	printf 'BUILTIN_CAMERA_CONSUMER_URL=%s\n' "$BUILTIN_CAMERA_CONSUMER_URL" >>"$RUN_DIR/config.txt"

	run_cmd adb-forward-camera-control adb -s "$PHONE_SERIAL" forward \
		"tcp:$CAMERA_CONTROL_FORWARD_PORT" "tcp:$PHONE_CAMERA_CONTROL_PORT"
	run_cmd logcat-clear adb -s "$PHONE_SERIAL" logcat -c || true
	run_cmd stop-wingout-before-settings adb -s "$PHONE_SERIAL" shell am force-stop "$WINGOUT_PACKAGE" || true
	seed_streaming_settings
	run_cmd wake adb -s "$PHONE_SERIAL" shell input keyevent KEYCODE_WAKEUP || true
	run_cmd dismiss-keyguard adb -s "$PHONE_SERIAL" shell wm dismiss-keyguard || true
	run_cmd collapse-statusbar adb -s "$PHONE_SERIAL" shell cmd statusbar collapse || true
	run_cmd home adb -s "$PHONE_SERIAL" shell input keyevent KEYCODE_HOME || true
	run_cmd launch-wingout adb -s "$PHONE_SERIAL" shell monkey -p "$WINGOUT_PACKAGE" \
		-c android.intent.category.LAUNCHER 1
	sleep 3
	dump_ui "launch" || true

	if ! rg -q "'Activate'" "$RUN_DIR/ui-targets-launch.txt"; then
		run_cmd tap-menu adb -s "$PHONE_SERIAL" shell input tap 540 210 || true
		sleep 1
		dump_ui "after-menu" || true
		run_cmd tap-cameras adb -s "$PHONE_SERIAL" shell input tap 540 493 || true
		sleep 2
		dump_ui "after-cameras" || true
	fi

	dump_ui "before-activate" || true
	collect_process_window_state "before-activate"
	collect_camera_state "before-activate"

	if ! wingout_window_is_live "before-activate"; then
		collect_logcat_window "before-activate"
		finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_WINGOUT_NOT_FOREGROUND_BEFORE_TAP" 40
	fi

	if ! button_point_from_dump "$RUN_DIR/uidump-before-activate.xml" >"$RUN_DIR/activate-target.txt"; then
		finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_NO_ENABLED_ACTIVATE_BUTTON" 41
	fi

	local activate_x activate_y activate_bounds tap_status
	read -r activate_x activate_y activate_bounds <"$RUN_DIR/activate-target.txt"
	printf 'adb -s %q shell input tap %q %q\n' "$PHONE_SERIAL" "$activate_x" "$activate_y" >"$RUN_DIR/tap-activate.cmd"
	set +e
	adb -s "$PHONE_SERIAL" shell input tap "$activate_x" "$activate_y" \
		>"$RUN_DIR/tap-activate.stdout" 2>"$RUN_DIR/tap-activate.stderr"
	tap_status="$?"
	set -e
	printf '%s\n' "$tap_status" >"$RUN_DIR/tap-activate.status"
	printf '%s\n' "$activate_bounds" >"$RUN_DIR/activate-bounds.txt"

	local i
	for i in $(seq 1 "$ACTIVATION_POLL_COUNT"); do
		sleep "$ACTIVATION_POLL_INTERVAL_SECONDS"
		dump_ui "after-$i" || true
		collect_process_window_state "after-$i"
		collect_camera_state "after-$i"

		if activation_dialog_visible "after-$i"; then
			collect_logcat_window "after-dialog"
			finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_DIALOG" 42
		fi

		if ! wingout_window_is_live "after-$i"; then
			collect_logcat_window "after-wingout-not-live-$i"
			# Post-tap death classification chain — reduced subset of v20 §3.3 routing
			# because post-tap context defaults to T1.1 WINGOUT_NOT_FOREGROUND_AFTER_TAP
			# unless reboot/LMKD evidence promotes it to T0.2a/T1 catch-all.
			# (Post-stability teardown uses the broader classify_runtime_teardown chain
			# that adds T2.x camera-provider / stream-teardown buckets.)
			if device_reboot_or_offline_detected "after-$i" "after-wingout-not-live-$i"; then
				finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_DEVICE_REBOOT_OR_OFFLINE" 46
			fi
			if logcat_has_lmkd_activation_failure "after-wingout-not-live-$i"; then
				finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_LMKD" 43
			fi
			finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_WINGOUT_NOT_FOREGROUND_AFTER_TAP" 44
		fi

		if camera_pipeline_has_required_outputs "after-$i"; then
			verify_camera_stream_stability "after-$i"
			collect_logcat_window "after-success"
			finish_with_result "BUILTINCAMERA_ACTIVATION_PASS" 0
		fi
	done

	collect_logcat_window "after-timeout"
	finish_with_result "BUILTINCAMERA_ACTIVATION_FAIL_NO_CAMERA_OUTPUTS" 45
}

# v20 sourcability guard — allows test fixtures to source helpers without triggering main flow.
# Per "${BASH_SOURCE[0]}" = "${0}" idiom: true when executed directly; false when sourced.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
	main "$@"
fi
