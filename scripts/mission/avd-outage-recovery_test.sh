#!/usr/bin/env bash
set -euo pipefail

# Test fixture for avd-outage-recovery.sh.
#
# Style mirrors builtincamera-ui-deactivation_test.sh: subprocess invocation
# under mocked binaries, plus sourcing-based unit tests. Each test has its own
# # Broke-the-code: block (per Task #20 durable rule + Task #2/#3 reinforcements).
# Pre-SUBMIT self-grep verification: broke-the-code count must equal test-header count.

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/avd-outage-recovery.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

tmp_dir=$(mktemp -d)
# cleanup(): reap orphan mock daemons + remove tmp_dir.
#
# Substrate-bug fix per Task #122 F-task116-1 (Goal 5 mock fixture cleanup
# gap). PRE-FIX form did `rm -rf "$tmp_dir"` only — without the pkill,
# mocks spawned via `"$bin_dir/avd" &` + disown inside run_recovery_test
# survived past test exit whenever fail() short-circuited the per-test
# reap (the kill -TERM/-KILL pair at the end of run_recovery_test). Mock
# has its binary held open as (deleted) and survives rm -rf, accumulating
# as long-lived orphan with cwd pointing to the test repo (per the
# discovered PID 970822 substrate-bug class).
#
# Pattern anchors to $tmp_dir (set immediately above, BEFORE the
# `trap cleanup` install) — invariant across all trap-fire windows. The
# `:-/dev/null/never-matches` defensive default handles the unlikely
# case where this trap fires before tmp_dir is set; that fallback path
# matches no real cmdline so pkill never false-positives on the host.
# Literal-dot escape `${var//./\\.}` prevents the dot in mktemp's
# `/tmp/tmp.XXXXXX` from acting as a regex wildcard inside `pkill -f`
# (which interprets the pattern as ERE). Without the escape, parallel
# fixture runs whose tmp paths share length and most chars could
# theoretically over-match across runs (low practical risk per
# mktemp's 6-char entropy, but defense-in-depth per reviewer-2
# F-plan-6 + symmetry with the (b) curative pattern which already
# uses `/tmp/tmp\..*/bin/avd` regex).
#
# KEEP_TMP=1 (debug-preserve) is folded into cleanup() body — original
# `trap - EXIT` form was correct only when EXIT was the sole trap and
# would silently regress (delete preserved dir on Ctrl-C) once INT/TERM/
# HUP traps were added. Folded form preserves debug semantic across all
# four trap conditions while still reaping orphans on every path.
#
# Trap-disarm at function head prevents double-fire (signal handler then
# bash-exit EXIT path). pkill ‖ true + conditional rm-rf are both
# idempotent so double-fire is non-destructive even without disarm; the
# disarm is a stylistic robustness add per reviewer-2 F-plan-4.
cleanup() {
	trap - EXIT INT TERM HUP
	local tmp_dir_safe="${tmp_dir:-/dev/null/never-matches}"
	pkill -TERM -f "${tmp_dir_safe//./\\.}/bin/avd" 2>/dev/null || true
	if [ -z "${KEEP_TMP:-}" ]; then
		rm -rf "$tmp_dir"
	fi
}
trap cleanup EXIT INT TERM HUP

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

# ----- Mock binaries -------------------------------------------------------
# avd binary: trivially exits 0 — start_avd backgrounds it via setsid+nohup.
cat >"$bin_dir/avd" <<'STUB'
#!/usr/bin/env bash
# Stays alive until killed; mock daemon for outage simulation.
trap 'exit 0' TERM INT
while true; do sleep 1; done
STUB
chmod +x "$bin_dir/avd"

# ss stub: behavior controlled by FAKE_SS_MODE.
# - listening: emits both consumer + publisher port lines
# - not-listening: empty output
# - phase-aware: reads $FAKE_SS_PHASE_FILE; if "ready", emit listening; else empty.
cat >"$bin_dir/ss" <<'STUB'
#!/usr/bin/env bash
mode="${FAKE_SS_MODE:-listening}"
case "$mode" in
	listening)
		printf 'LISTEN 0 100 0.0.0.0:1945 0.0.0.0:* users:((avd,1234,3))\n'
		printf 'LISTEN 0 100 0.0.0.0:1946 0.0.0.0:* users:((avd,1234,4))\n'
		;;
	not-listening)
		printf 'LISTEN 0 100 0.0.0.0:22 0.0.0.0:* users:((sshd,99,3))\n'
		;;
	phase-aware)
		phase="$(cat "${FAKE_SS_PHASE_FILE:-/dev/null}" 2>/dev/null || echo unset)"
		if [ "$phase" = "ready" ]; then
			printf 'LISTEN 0 100 0.0.0.0:1945 0.0.0.0:* users:((avd,1234,3))\n'
			printf 'LISTEN 0 100 0.0.0.0:1946 0.0.0.0:* users:((avd,1234,4))\n'
		fi
		;;
esac
exit 0
STUB
chmod +x "$bin_dir/ss"

# ffprobe stub: behavior driven by FAKE_FFPROBE_MODE.
#  - default: 30fps h264 + 48kHz aac with frame counts in PASS range; -show_packets
#    emits ≥250 video packets including ≥3 keyframes >20KB
#  - small-frames: nb_read_frames out of range (FAIL frame-count gate)
#  - unavailable: exit 1 (route empty/down)
#  - phase-aware: per-call counter file at $FAKE_FFPROBE_CTR_FILE; resolves
#    effective mode via FAKE_FFPROBE_PHASE_<idx> env (idx = (call_n-1)/3 since
#    each probe makes 3 ffprobe calls). Phases 0..3 map to pre-builtin /
#    pre-dji / post-builtin / post-dji probes in main flow order. Used to
#    target one specific endpoint's failure without affecting earlier probes.
cat >"$bin_dir/ffprobe" <<'STUB'
#!/usr/bin/env bash
mode="${FAKE_FFPROBE_MODE:-default}"

# Detect -show_packets vs stream-info invocation.
show_packets=0
for arg in "$@"; do
	if [ "$arg" = "-show_packets" ]; then
		show_packets=1
		break
	fi
done

# Phase-aware: resolve effective mode by indexing into FAKE_FFPROBE_PHASE_<idx>.
if [ "$mode" = "phase-aware" ]; then
	ctr_file="${FAKE_FFPROBE_CTR_FILE:-/tmp/avd-outage-ffprobe.ctr}"
	cur="$(cat "$ctr_file" 2>/dev/null || echo 0)"
	cur=$((cur + 1))
	printf '%d\n' "$cur" >"$ctr_file"
	phase_idx=$(( (cur - 1) / 3 ))
	phase_var="FAKE_FFPROBE_PHASE_${phase_idx}"
	mode="${!phase_var:-default}"
fi

case "$mode" in
	unavailable)
		echo "rtmp open failed (mocked unavailable)" >&2
		exit 1
		;;
	small-frames)
		if [ "$show_packets" -eq 1 ]; then
			# Emit a small packet set so the keyframe gate would also trip.
			cat <<'JSON'
{"packets":[{"flags":"K_","size":15000,"pts_time":"0","dts_time":"0"}]}
JSON
		else
			cat <<'JSON'
{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"30/1","nb_read_frames":"30","duration":"1"},{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"nb_read_frames":"40","duration":"1"}]}
JSON
		fi
		;;
	*)
		if [ "$show_packets" -eq 1 ]; then
			# Build 260 video packets via Python: 5 keyframes >20KB at start +
			# 255 P-frames (smaller) — clears ≥250 total + ≥3 keyframes >20KB gate.
			python3 - <<'PY'
import json
packets = []
for i in range(5):
    packets.append({"flags": "K_", "size": 24000 + i * 1000, "pts_time": str(i / 30.0), "dts_time": str(i / 30.0)})
for i in range(5, 260):
    packets.append({"flags": "__", "size": 4000, "pts_time": str(i / 30.0), "dts_time": str(i / 30.0)})
print(json.dumps({"packets": packets}))
PY
		else
			cat <<'JSON'
{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"30/1","nb_read_frames":"300","duration":"10"},{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"nb_read_frames":"480","duration":"10"}]}
JSON
		fi
		;;
esac
exit 0
STUB
chmod +x "$bin_dir/ffprobe"

# ffmpeg stub — handles 3 invocation shapes:
#   1. capture: -i <url> -c copy <out.mkv> → write placeholder to output positional
#   2. volumedetect: -af volumedetect -f null - → emit `[Parsed_volumedetect_0 @ ...]
#      mean_volume: -25.5 dB` to STDERR
#   3. signalstats: -vf "signalstats,metadata=mode=print:file=<path>" → write
#      lavfi.signalstats.YAVG/YMIN/YMAX lines to the path captured from the -vf arg
cat >"$bin_dir/ffmpeg" <<'STUB'
#!/usr/bin/env bash
all_args="$*"

# volumedetect path
if [[ "$all_args" == *"volumedetect"* ]]; then
	echo "[Parsed_volumedetect_0 @ 0xfff] mean_volume: -25.5 dB" >&2
	echo "[Parsed_volumedetect_0 @ 0xfff] max_volume: -3.2 dB" >&2
	exit 0
fi

# signalstats path — extract metadata file path from -vf arg.
prev=""
sig_file=""
for arg in "$@"; do
	if [ "$prev" = "-vf" ]; then
		# Match `signalstats,metadata=mode=print:file=<path>`
		if [[ "$arg" == *"metadata=mode=print:file="* ]]; then
			sig_file="${arg##*file=}"
		fi
		prev=""
		continue
	fi
	case "$arg" in
		-*) prev="$arg" ;;
	esac
done

if [ -n "$sig_file" ]; then
	# Emit ≥5 luma samples with range > 50 (clears luminance gate).
	{
		for i in 1 2 3 4 5 6; do
			echo "frame:$i pts:$i pts_time:$(awk "BEGIN { print $i/30 }")"
			echo "lavfi.signalstats.YAVG=128.0"
			echo "lavfi.signalstats.YMIN=20.0"
			echo "lavfi.signalstats.YMAX=235.0"
		done
	} >"$sig_file"
	exit 0
fi

# capture path: write placeholder to output positional.
prev=""
out=""
for arg in "$@"; do
	if [ "$prev" = "-i" ]; then
		prev=""
		continue
	fi
	case "$arg" in
		-*) prev="$arg" ;;
		*)  out="$arg"; prev="" ;;
	esac
done
[ -n "$out" ] && [ "$out" != "-" ] && printf 'mock\n' >"$out"
exit 0
STUB
chmod +x "$bin_dir/ffmpeg"

# Deactivation-script stub: simulate the canonical deactivation script's
# success path without re-mocking adb/dump_ui — the harness controls outcome
# via FAKE_DEACT_EXIT (default 0 = success).
deact_stub="$tmp_dir/deactivation-stub.sh"
cat >"$deact_stub" <<'STUB'
#!/usr/bin/env bash
# Test stub for builtincamera-ui-deactivation.sh; controlled by FAKE_DEACT_EXIT.
exit "${FAKE_DEACT_EXIT:-0}"
STUB
chmod +x "$deact_stub"

export PATH="$bin_dir:$PATH"

# run_recovery_test — spawn a mock AVD process, record its PID to a per-run
# pid file, run the recovery script under the test mocks, assert on result.
# Args 1-5 are positional. Remaining args (6+) are KEY=VALUE pairs injected as
# env overrides via `env … "$@" …` — they win over the fixed defaults below
# because env applies them after the function's defaults.
run_recovery_test() {
	local name="$1"
	local ffprobe_mode="$2"
	local expected_result="$3"
	local expected_status="$4"
	local deact_exit="${5:-0}"
	# Collect extra KEY=VALUE env overrides (positional args 6+).
	local extra_env=()
	if [ "$#" -gt 5 ]; then
		extra_env=("${@:6}")
	fi

	local run_root="$tmp_dir/run-$name"
	rm -rf "$run_root"
	mkdir -p "$run_root"
	local pid_file="$run_root/avd.pid"

	# Spawn a mock AVD process and record its PID.
	"$bin_dir/avd" >/dev/null 2>&1 &
	local avd_pid="$!"
	disown "$avd_pid" 2>/dev/null || true
	printf '%s\n' "$avd_pid" >"$pid_file"

	# Per-run phase counter file so phase-aware ffprobe sees a fresh phase 0.
	local phase_ctr="$run_root/ffprobe-phase.ctr"
	: >"$phase_ctr"

	set +e
	env \
		RUN_ROOT="$run_root" \
		AVD_BIN="$bin_dir/avd" \
		AVD_CONFIG="$tmp_dir/avd.cfg" \
		AVD_PID_FILE="$pid_file" \
		AVD_OUTAGE_SECONDS=1 \
		AVD_READY_TIMEOUT_SECONDS=5 \
		AVD_STOP_GRACE_SECONDS=3 \
		CAPTURE_SECONDS=1 \
		FAKE_FFPROBE_MODE="$ffprobe_mode" \
		FAKE_FFPROBE_CTR_FILE="$phase_ctr" \
		FAKE_SS_MODE=listening \
		FAKE_DEACT_EXIT="$deact_exit" \
		DEACTIVATION_SCRIPT="$deact_stub" \
		"${extra_env[@]}" \
		"$script" >"$tmp_dir/$name.stdout" 2>"$tmp_dir/$name.stderr"
	local status=$?
	set -e

	local run_dir
	run_dir=$(sed -n 's/^RUN_DIR=//p' "$tmp_dir/$name.stdout" | tail -1)
	if [ -z "$run_dir" ] || [ ! -s "$run_dir/result.txt" ]; then
		cat "$tmp_dir/$name.stdout" >&2
		cat "$tmp_dir/$name.stderr" >&2
		fail "$name: harness did not write a result"
	fi

	# Reap any AVD daemon left behind (relaunched one logged in start-post-outage.pid).
	if [ -s "$run_dir/start-post-outage.pid" ]; then
		local relaunch_pid
		relaunch_pid="$(cat "$run_dir/start-post-outage.pid")"
		kill -TERM "$relaunch_pid" 2>/dev/null || true
	fi
	# Reap original mock too if still alive.
	kill -KILL "$avd_pid" 2>/dev/null || true

	local result
	result=$(cat "$run_dir/result.txt")
	if [ "$result" != "$expected_result" ]; then
		cat "$tmp_dir/$name.stdout" >&2
		cat "$tmp_dir/$name.stderr" >&2
		fail "$name: expected result='$expected_result' got '$result' (status=$status)"
	fi
	if [ "$status" -ne "$expected_status" ]; then
		fail "$name: expected status=$expected_status got $status (result=$result)"
	fi
}

# ============================================================================
# Integration tests
# ============================================================================

# --- T-r1: happy path → AVD_OUTAGE_RECOVERY_PASS ---
# Broke-the-code: any of (a) wait_for_avd_ready timing-out due to ss stub
# returning empty, (b) probe_endpoint_publishing not recognizing the mocked
# 300/480 frame counts as in-range, (c) stop_avd_with_grace failing to reap
# the mock — would all fail with mismatched fail codes. Test catches the
# integrated end-to-end recovery contract.
run_recovery_test "T-r1-happy-path" "default" \
	"AVD_OUTAGE_RECOVERY_PASS" 0

# --- T-r2: pre-outage builtin not publishing → fail 61 ---
# Broke-the-code: skipping pre-outage probes would let the script silently
# accept a degraded baseline → an ALWAYS-PASS post-outage probe (because both
# endpoints were already non-publishing) would be uncatchable.
run_recovery_test "T-r2-pre-outage-unavailable" "unavailable" \
	"AVD_OUTAGE_RECOVERY_FAIL_BUILTIN_NOT_PUBLISHING_PRE_OUTAGE" 61

# --- T-r3: post-outage builtin frame-count gate fail → fail 65 ---
# Broke-the-code: probe_endpoint_publishing accepting partial captures (e.g.,
# 30 video / 40 audio frames from small-frames mode) would mask cases where
# the recovered AVD only delivers a brief burst before re-failing. Phase-aware
# ffprobe stub targets phase 2 (post-outage builtin) with small-frames mode
# while phases 0/1 (pre-outage probes) stay default — pre-outage gate clears,
# post-outage builtin gate trips → fail 65 fires precisely as intended.
run_recovery_test "T-r3-frame-count-gate" "phase-aware" \
	"AVD_OUTAGE_RECOVERY_FAIL_BUILTIN_DID_NOT_RECOVER" 65 0 \
	FAKE_FFPROBE_PHASE_2=small-frames

# --- T-r4: post-recovery deactivate fails → fail 67 ---
# Broke-the-code: removing the post-recovery deactivate Phase 4 step would let
# the script PASS even when the post-recovery UI Deactivate cannot complete
# cleanly — masking a real Goal 5 acceptance gap (the brief explicitly requires
# deactivate-via-UI-after-recovery + builtin-empty + DJI-continuity gates).
run_recovery_test "T-r4-deact-fail-post-recovery" "default" \
	"AVD_OUTAGE_RECOVERY_FAIL_POST_RECOVERY_DEACTIVATE" 67 1

# --- T-r5: AVD not running pre-outage → fail 60 ---
# Broke-the-code: skipping the avd_pid_alive pre-outage gate would let the
# outage simulation proceed against an already-dead AVD — SIGTERM against a
# non-existent PID is a no-op, the "outage" wouldn't actually have a daemon
# to disrupt, and the recovery probe would either succeed coincidentally
# (false PASS) or fail at a misleading downstream code. Pin fail-60 to the
# correct entry-point.
echo 99999999 >"$tmp_dir/dead.pid"
run_recovery_test "T-r5-avd-not-running" "default" \
	"AVD_OUTAGE_RECOVERY_FAIL_AVD_NOT_RUNNING_PRE_OUTAGE" 60 0 \
	"AVD_PID_FILE=$tmp_dir/dead.pid"

# --- T-r6: pre-outage DJI not publishing → fail 62 ---
# Broke-the-code: dropping the second pre-outage probe (DJI) would let the
# script proceed past the baseline-verification step with only a builtin-side
# guarantee — masking the case where the DJI camera is the broken endpoint.
# Phase-aware: phase 0 (pre-builtin) default OK, phase 1 (pre-dji) unavailable.
run_recovery_test "T-r6-pre-outage-dji-fail" "phase-aware" \
	"AVD_OUTAGE_RECOVERY_FAIL_DJI_NOT_PUBLISHING_PRE_OUTAGE" 62 0 \
	FAKE_FFPROBE_PHASE_1=unavailable

# --- T-r7: post-outage AVD ports never come up → fail 64 ---
# Broke-the-code: skipping the wait_for_avd_ready timeout would let the
# script attempt post-outage probes against an AVD whose listener ports are
# down — probes would all fail at fail-65/fail-66 instead of pinpointing the
# AVD-failed-to-restart root cause. ss stub stuck in not-listening surfaces
# the timeout cleanly.
run_recovery_test "T-r7-avd-ready-timeout" "default" \
	"AVD_OUTAGE_RECOVERY_FAIL_AVD_READY_TIMEOUT" 64 0 \
	FAKE_SS_MODE=not-listening

# --- T-r8: post-outage DJI did not recover → fail 66 ---
# Broke-the-code: dropping the post-outage DJI verification would let a
# half-recovery state (only builtin restored) report success — exactly the
# Goal-5 mission's both-endpoints-restore acceptance gate would be defeated.
# Phase-aware: phases 0/1/2 default OK, phase 3 (post-dji) unavailable.
run_recovery_test "T-r8-post-outage-dji-fail" "phase-aware" \
	"AVD_OUTAGE_RECOVERY_FAIL_DJI_DID_NOT_RECOVER" 66 0 \
	FAKE_FFPROBE_PHASE_3=unavailable

# --- T-r9: deactivation script missing → fail 68 ---
# Broke-the-code: skipping the executability check on DEACTIVATION_SCRIPT
# would let the Phase-4 step silently no-op when the canonical deactivation
# helper is absent — masking a build/packaging error where the helper got
# orphaned from the recovery script. fail-68 fires precisely on this
# missing-tooling condition (distinct from fail-67 = helper-ran-but-failed).
run_recovery_test "T-r9-deactivation-script-missing" "default" \
	"AVD_OUTAGE_RECOVERY_FAIL_DEACTIVATION_SCRIPT_MISSING" 68 0 \
	"DEACTIVATION_SCRIPT=$tmp_dir/nonexistent-deact.sh"

# NOTE on fail-63 (AVD_STOP) coverage gap: stop_avd_with_grace returns
# non-zero only when SIGKILL fails to reap the AVD pid. No mock process
# survives SIGKILL (kernel-enforced), and `kill -0 1` from a non-root user
# returns EPERM → fires fail-60 before reaching the stop step. Genuine
# integration coverage of fail-63 requires a real AVD process configured to
# survive SIGKILL, which is infeasible in this unit harness. Tracked as
# v23 follow-up; covered indirectly by T-u3/T-u4 stale-PID rejection in
# stop_avd_with_grace's first liveness check.

# ============================================================================
# Unit tests via sourcing
# ============================================================================
unit_test_run() {
	local name="$1"
	local stub_dir="$2"
	local body="$3"

	(
		set -Eeuo pipefail
		export PATH="$stub_dir:$PATH"
		export RUN_ROOT="$stub_dir/run-root"
		export RUN_DIR="$stub_dir/run-dir"
		mkdir -p "$RUN_DIR"
		# shellcheck disable=SC1090
		source "$script"
		eval "$body"
	)
	local rc=$?
	if [ "$rc" -ne 0 ]; then
		fail "unit test $name failed with rc=$rc"
	fi
}

# --- T-u1: avd_pid_alive returns 0 for live PID ---
# Broke-the-code: skipping the `kill -0 "$pid"` check would false-positive on
# stale PID files referencing reaped processes — letting the outage path
# attempt to SIGTERM a non-existent PID and silently no-op the outage.
tu1_dir="$tmp_dir/tu1"
mkdir -p "$tu1_dir/run-dir"
unit_test_run "T-u1: avd_pid_alive positive on current shell PID" "$tu1_dir" "
	export AVD_PID_FILE='$tu1_dir/avd.pid'
	printf '%s\n' \"\$BASHPID\" >\"\$AVD_PID_FILE\"
	avd_pid_alive || exit 1
"

# --- T-u2: avd_pid_alive NEGATIVE on missing PID file ---
# Broke-the-code: returning 0 when the PID file is absent would let the script
# proceed past the pre-outage liveness gate with no AVD reference → SIGTERM
# would be issued against an empty target.
tu2_dir="$tmp_dir/tu2"
mkdir -p "$tu2_dir/run-dir"
unit_test_run "T-u2: avd_pid_alive negative on missing file" "$tu2_dir" "
	export AVD_PID_FILE='$tu2_dir/nonexistent.pid'
	if avd_pid_alive; then
		echo 'false positive: missing PID file matched alive predicate' >&2
		exit 1
	fi
"

# --- T-u3: avd_pid_alive NEGATIVE on stale PID (process reaped) ---
# Broke-the-code: stale-PID detection requires `kill -0` runtime probe; mere
# file-existence check would pass for a long-dead PID and mask outage failures.
tu3_dir="$tmp_dir/tu3"
mkdir -p "$tu3_dir/run-dir"
unit_test_run "T-u3: avd_pid_alive negative on stale PID" "$tu3_dir" "
	# PID 1 is init; PID 99999999 is reliably non-existent on Linux (default
	# pid_max=4194304 on most kernels per /proc/sys/kernel/pid_max).
	export AVD_PID_FILE='$tu3_dir/stale.pid'
	printf '%s\n' '99999999' >\"\$AVD_PID_FILE\"
	if avd_pid_alive; then
		echo 'false positive: stale PID matched alive predicate' >&2
		exit 1
	fi
"

# --- T-u4: avd_pid_alive NEGATIVE on non-numeric PID file content ---
# Broke-the-code: missing the regex `[[ "$pid" =~ ^[1-9][0-9]*$ ]]` validation
# would pass garbage content through to `kill -0` which errors loudly but
# silently returns non-zero — hides the file-corruption case as a generic
# stale-pid failure instead of an explicit malformed-content signal.
tu4_dir="$tmp_dir/tu4"
mkdir -p "$tu4_dir/run-dir"
unit_test_run "T-u4: avd_pid_alive negative on non-numeric content" "$tu4_dir" "
	export AVD_PID_FILE='$tu4_dir/garbage.pid'
	printf 'not-a-pid\n' >\"\$AVD_PID_FILE\"
	if avd_pid_alive; then
		echo 'false positive: non-numeric content matched alive predicate' >&2
		exit 1
	fi
"

# --- T-u5: avd_pid_alive NEGATIVE on PID "0" content ---
# Broke-the-code: a regex `^[0-9]+$` (which accepts "0") would let a stale
# PID file containing "0" pass through to `kill -0 0`. Per POSIX, `kill -0 0`
# signals every process in the caller's process group — which always returns
# 0 → false-positive. The tightened regex `^[1-9][0-9]*$` rejects "0" at the
# regex layer before kill -0 ever fires.
tu5_dir="$tmp_dir/tu5"
mkdir -p "$tu5_dir/run-dir"
unit_test_run "T-u5: avd_pid_alive negative on PID 0" "$tu5_dir" "
	export AVD_PID_FILE='$tu5_dir/zero.pid'
	printf '0\n' >\"\$AVD_PID_FILE\"
	if avd_pid_alive; then
		echo 'false positive: PID 0 matched alive predicate' >&2
		exit 1
	fi
"

# --- T-u6: cleanup() reaps orphan mock daemon under $tmp_dir/bin/avd ---
# Broke-the-code: PRE-FIX cleanup() did `rm -rf "$tmp_dir"` only — without
# pkill, mocks spawned via run_recovery_test's `"$bin_dir/avd" &` + disown
# survive past test exit when fail() short-circuits the per-test reap.
# Mock has its binary held open as (deleted) and survives rm -rf,
# accumulating as orphan with cwd pointing to the test repo (Task #116
# PID 970822 substrate-bug class). Test calls cleanup() in a subshell
# with a temporary $tmp_dir override + a freshly-spawned mock; asserts no
# orphan survives. Multi-signal trap coupling (EXIT/INT/TERM/HUP) is
# additionally verified by 4 separate empirical A/B-differential captures
# preserved at submit-time under task122-ab-evidence/.
tu6_dir="$tmp_dir/tu6"
mkdir -p "$tu6_dir/bin"
cp "$bin_dir/avd" "$tu6_dir/bin/avd"
(
	# Subshell scope contains the tmp_dir override so the parent's
	# cleanup-on-EXIT still uses the real $tmp_dir.
	tmp_dir="$tu6_dir"
	"$tu6_dir/bin/avd" >/dev/null 2>&1 &
	disown $! 2>/dev/null || true
	sleep 0.3
	if ! pgrep -f "$tu6_dir/bin/avd" >/dev/null 2>&1; then
		echo "T-u6 setup: mock did not stay alive after spawn" >&2
		exit 1
	fi
	# Call the test fixture's cleanup() function (defined at top of file).
	# PRE-FIX cleanup body (rm -rf only) leaves mock alive → exit 1.
	# POST-FIX cleanup body (pkill + rm -rf) reaps mock → exit 0.
	cleanup
	# Allow mock's TERM-trap to fire (sleep loop has ~1s granularity).
	sleep 1.5
	if pgrep -f "$tu6_dir/bin/avd" >/dev/null 2>&1; then
		pgrep -af "$tu6_dir/bin/avd" >&2 || true
		pkill -KILL -f "$tu6_dir/bin/avd" 2>/dev/null || true
		echo "T-u6: cleanup() did not reap mock daemon under $tu6_dir/bin/avd" >&2
		exit 1
	fi
	exit 0
) || fail "T-u6: cleanup() failed to reap orphan mock daemon (Task #122 F-task116-1 substrate-bug regression)"

echo "All AVD outage recovery tests passed."
