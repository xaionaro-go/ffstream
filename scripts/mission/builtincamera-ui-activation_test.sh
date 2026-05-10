#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/builtincamera-ui-activation.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

tmp_dir=$(mktemp -d)
cleanup() {
	rm -rf "$tmp_dir"
}
trap cleanup EXIT

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

ui_xml="$tmp_dir/window.xml"
cat >"$ui_xml" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<hierarchy>
  <node text="Activate" bounds="[0,2192][487,2335]" enabled="true" clickable="true" package="center.dx.wingout" />
</hierarchy>
XML

cat >"$bin_dir/adb" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "-s" ]; then
	shift 2
fi

case "${1:-}" in
	forward)
		exit 0
		;;
	push)
		cp "${2:-}" "$FAKE_PUSHED_SETTINGS"
		exit 0
		;;
	logcat)
		case "${2:-}" in
			-c)
				exit 0
				;;
			-d)
				cat "$FAKE_LOGCAT"
				exit 0
				;;
		esac
		;;
	pull)
		cp "$FAKE_UI_XML" "${3:-}"
		exit 0
		;;
	shell)
		shift
		cmd="$*"
		case "$cmd" in
			"uiautomator dump /sdcard/window.xml")
				exit 0
				;;
			"pidof center.dx.wingout")
				printf '1234\n'
				exit 0
				;;
			"dumpsys window")
				printf 'mCurrentFocus=Window{center.dx.wingout/center.dx.wingout.MainActivity}\n'
				exit 0
				;;
			"dumpsys activity top")
				printf 'ACTIVITY center.dx.wingout/.MainActivity\n'
				exit 0
				;;
			"run-as center.dx.wingout cat cache/streaming_settings.json")
				if [ -s "$FAKE_SETTINGS_FILE" ]; then
					cat "$FAKE_SETTINGS_FILE"
					exit 0
				fi
				exit 1
				;;
			"run-as center.dx.wingout mkdir -p cache")
				exit 0
				;;
			run-as\ center.dx.wingout\ cp\ *\ cache/streaming_settings.json)
				cp "$FAKE_PUSHED_SETTINGS" "$FAKE_SETTINGS_FILE"
				exit 0
				;;
			chmod\ 0644\ /data/local/tmp/*|rm\ -f\ /data/local/tmp/*|"am force-stop center.dx.wingout")
				exit 0
				;;
			*)
				exit 0
				;;
		esac
		;;
esac

exit 0
STUB

cat >"$bin_dir/ffstreamctl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

kind="${*: -2:1} ${*: -1}"
case "$kind" in
	"inputs info")
		count_file="$FAKE_INPUT_COUNT"
		;;
	"pipelines get")
		count_file="$FAKE_PIPELINE_COUNT"
		;;
	*)
		echo "unexpected ffstreamctl args: $*" >&2
		exit 64
		;;
esac

count=0
if [ -s "$count_file" ]; then
	count=$(cat "$count_file")
fi
count=$((count + 1))
printf '%s\n' "$count" >"$count_file"

if [ "$count" -eq 2 ]; then
	case "$kind" in
	"inputs info")
			case "${FAKE_INPUT_MODE:-complete}" in
				complete)
					cat <<'JSON'
{"inputs":[{"is_active":true,"input_config":{"custom_options":[{"key":"f","value":"android_camera"},{"key":"video_size","value":"1920x1920"},{"key":"framerate","value":"30"}]}},{"is_active":true,"input_config":{"custom_options":[{"key":"f","value":"android_microphone"},{"key":"sample_rate","value":"48000"}]}}]}
JSON
					;;
				camera-only)
					cat <<'JSON'
{"inputs":[{"is_active":true,"input_config":{"custom_options":[{"key":"f","value":"android_camera"},{"key":"video_size","value":"1920x1920"},{"key":"framerate","value":"30"}]}}]}
JSON
					;;
				*)
					echo "unexpected FAKE_INPUT_MODE=${FAKE_INPUT_MODE:-}" >&2
					exit 64
					;;
			esac
			;;
		"pipelines get")
			cat <<'JSON'
{"description":"Transcoder(NaiveDecoderFactory(/)->NaiveEncoderFactory(av1_mediacodec/)):StreamMux.Outputs[801] SetDropOnCloserWrapper(Output(rtmp://192.168.141.16:1946/pixel/builtincamera-av1-1920/):StreamMux.Outputs[801]) Transcoder(NaiveDecoderFactory(/)->NaiveEncoderFactory(/aac)):StreamMux.Outputs[802] SetDropOnCloserWrapper(Output(rtmp://192.168.141.16:1946/pixel/builtincamera-aac-48000/):StreamMux.Outputs[802]) StreamMux:Output:audio-only"}
JSON
			;;
	esac
	exit 0
fi

printf '{}\n'
STUB

chmod +x "$bin_dir/adb" "$bin_dir/ffstreamctl"

fake_logcat="$tmp_dir/logcat.txt"
cat >"$fake_logcat" <<'LOGCAT'
05-06 19:26:57.491  1078  1078 E kernel  : Out of memory: Killed process 5148 (ffstream) total-vm:3505152056kB, anon-rss:930332kB
05-06 19:26:58.105  6298  6298 I crash_dump64: performing dump of process 947
LOGCAT

export PATH="$bin_dir:$PATH"
export FAKE_UI_XML="$ui_xml"
export FAKE_LOGCAT="$fake_logcat"
export FAKE_INPUT_COUNT="$tmp_dir/input-count"
export FAKE_PIPELINE_COUNT="$tmp_dir/pipeline-count"
export FAKE_SETTINGS_FILE="$tmp_dir/streaming_settings.json"
export FAKE_PUSHED_SETTINGS="$tmp_dir/pushed_streaming_settings.json"

run_root="$tmp_dir/run"
set +e
RUN_ROOT="$run_root" \
	PHONE_SERIAL=41041JEKB08092 \
	FFSTREAMCTL_BIN="$bin_dir/ffstreamctl" \
	ACTIVATION_POLL_COUNT=1 \
	ACTIVATION_POLL_INTERVAL_SECONDS=0 \
	ACTIVATION_STABILITY_SECONDS=1 \
	ACTIVATION_STABILITY_POLL_SECONDS=1 \
	"$script" >"$tmp_dir/run.stdout" 2>"$tmp_dir/run.stderr"
status=$?
set -e

run_dir=$(sed -n 's/^RUN_DIR=//p' "$tmp_dir/run.stdout" | tail -1)
if [ -z "$run_dir" ] || [ ! -s "$run_dir/result.txt" ]; then
	cat "$tmp_dir/run.stdout" >&2
	cat "$tmp_dir/run.stderr" >&2
	fail "activation harness did not write a result"
fi

result=$(cat "$run_dir/result.txt")
if [ "$result" != "BUILTINCAMERA_ACTIVATION_FAIL_LMKD" ]; then
	cat "$tmp_dir/run.stdout" >&2
	cat "$tmp_dir/run.stderr" >&2
	find "$run_dir" -maxdepth 1 -type f -name 'camera-inputs-*' -o -name 'logcat-relevant-*' >&2
	fail "expected stability guard to classify LMKD, got $result with status $status"
fi
if [ "$status" -eq 0 ]; then
	fail "LMKD activation classification must be a failing harness status"
fi

printf '' >"$fake_logcat"
printf '0\n' >"$FAKE_INPUT_COUNT"
printf '0\n' >"$FAKE_PIPELINE_COUNT"

set +e
RUN_ROOT="$run_root-camera-only" \
	PHONE_SERIAL=41041JEKB08092 \
	FFSTREAMCTL_BIN="$bin_dir/ffstreamctl" \
	ACTIVATION_POLL_COUNT=1 \
	ACTIVATION_POLL_INTERVAL_SECONDS=0 \
	ACTIVATION_STABILITY_SECONDS=0 \
	FAKE_INPUT_MODE=camera-only \
	"$script" >"$tmp_dir/run-camera-only.stdout" 2>"$tmp_dir/run-camera-only.stderr"
status=$?
set -e

run_dir=$(sed -n 's/^RUN_DIR=//p' "$tmp_dir/run-camera-only.stdout" | tail -1)
if [ -z "$run_dir" ] || [ ! -s "$run_dir/result.txt" ]; then
	cat "$tmp_dir/run-camera-only.stdout" >&2
	cat "$tmp_dir/run-camera-only.stderr" >&2
	fail "camera-only activation harness did not write a result"
fi

result=$(cat "$run_dir/result.txt")
if [ "$result" = "BUILTINCAMERA_ACTIVATION_PASS" ]; then
	cat "$tmp_dir/run-camera-only.stdout" >&2
	cat "$tmp_dir/run-camera-only.stderr" >&2
	fail "activation harness must not pass when microphone input is missing"
fi
if [ "$status" -eq 0 ]; then
	fail "missing microphone must be a failing harness status"
fi

# ============================================================================
# Unit tests for v20-spec helpers (pgrep_3sample_any_death_wins, finalize_cycle_logcat,
# pid_continuity_check_intersection, classify_via_fallback, handle_unknown_disambiguation).
# Each test runs in a subshell with a per-test stub directory layered onto $PATH so
# helpers see controlled mocks. Source the production script to load function definitions
# without triggering main flow (per BASH_SOURCE guard).
# ============================================================================

# Broke-the-code-validation: each unit test below documents the inverse — what
# mutation in the helper would cause the test to fail. Per testing-discipline
# dual-sided rule.

unit_test_run() {
	local name="$1"
	local stub_dir="$2"
	local body="$3"

	# shellcheck disable=SC2086
	(
		set -Eeuo pipefail
		export PATH="$stub_dir:$PATH"
		# Avoid leaking RUN_DIR/RUN_ROOT into the sourcing shell — we don't run main flow.
		export RUN_ROOT="$stub_dir/run-root"
		export RUN_DIR="$stub_dir/run-dir"
		# shellcheck disable=SC1090
		source "$script"
		eval "$body"
	)
	local rc=$?
	if [ "$rc" -ne 0 ]; then
		fail "unit test $name failed with rc=$rc"
	fi
}

# --- T-h1: pgrep_3sample_any_death_wins — all-alive returns 0; wall-clock ~1s ---
# Broke-the-code: removing skip-final-sleep guard `[ "$i" -lt 3 ] && sleep 0.5`
# would push wall-clock to ~1.5s and FAIL the upper-bound assertion.
th1_dir="$tmp_dir/th1"
mkdir -p "$th1_dir"
cat >"$th1_dir/pgrep" <<'PGREP_STUB'
#!/usr/bin/env bash
exit 0
PGREP_STUB
chmod +x "$th1_dir/pgrep"

unit_test_run "T-h1: pgrep_3sample alive path 1s wall-clock" "$th1_dir" '
	t0_ns=$(date +%s%N)
	pgrep_3sample_any_death_wins "fake-pattern" || exit 1
	t1_ns=$(date +%s%N)
	# Use ms granularity to dodge floating-point in pure bash.
	dt_ms=$(( (t1_ns - t0_ns) / 1000000 ))
	# Lower bound: 2 sleeps of 0.5s = 1.0s = 1000ms (allow 50ms scheduler slack).
	[ "$dt_ms" -ge 950 ] || { echo "wall-clock $dt_ms ms under 950ms" >&2; exit 2; }
	# Upper bound: 1.0s + scheduler slack. 1100ms is generous; if helper has
	# erroneous final-iteration sleep, dt_ms ≥ 1500 → FAIL upper bound.
	[ "$dt_ms" -le 1200 ] || { echo "wall-clock $dt_ms ms exceeds 1200ms (skip-final-sleep regression?)" >&2; exit 3; }
'

# --- T-h2: pgrep_3sample_any_death_wins — first sample dead → returns 1 fast ---
# Broke-the-code: changing `return 1` on death detection to `continue` would
# allow late-recovery samples to override → test would PASS when helper returns 0.
th2_dir="$tmp_dir/th2"
mkdir -p "$th2_dir"
cat >"$th2_dir/pgrep" <<'PGREP_STUB'
#!/usr/bin/env bash
exit 1
PGREP_STUB
chmod +x "$th2_dir/pgrep"

unit_test_run "T-h2: pgrep_3sample dead path returns 1" "$th2_dir" '
	t0_ns=$(date +%s%N)
	if pgrep_3sample_any_death_wins "fake-pattern"; then
		echo "expected return 1 on dead pgrep, got 0" >&2
		exit 1
	fi
	t1_ns=$(date +%s%N)
	dt_ms=$(( (t1_ns - t0_ns) / 1000000 ))
	# Early exit on first dead sample → wall-clock should be << 1s.
	[ "$dt_ms" -le 200 ] || { echo "early-exit dead path took $dt_ms ms (>200ms)" >&2; exit 2; }
'

# --- T-h3: pid_continuity_check_intersection — non-empty intersection returns 0 ---
# Broke-the-code: comparing PID strings instead of intersection-non-empty would
# false-positive on reordering or whitespace drift.
th3_dir="$tmp_dir/th3"
mkdir -p "$th3_dir"
cat >"$th3_dir/pgrep" <<'PGREP_STUB'
#!/usr/bin/env bash
# Returns the same PID set as the saved snapshot (intersection: 1234, 5678)
printf '5678\n1234\n9999\n'
PGREP_STUB
chmod +x "$th3_dir/pgrep"

unit_test_run "T-h3: pid_continuity_check_intersection non-empty" "$th3_dir" '
	saved="1234 5678 "
	pid_continuity_check_intersection "$saved" "fake-pattern" || exit 1
'

# --- T-h4: pid_continuity_check_intersection — empty intersection returns 1 ---
# Broke-the-code: returning 0 when saved is empty (no anchor) would mask
# all-cycle-deaths as "alive".
th4_dir="$tmp_dir/th4"
mkdir -p "$th4_dir"
cat >"$th4_dir/pgrep" <<'PGREP_STUB'
#!/usr/bin/env bash
printf '7777\n8888\n'
PGREP_STUB
chmod +x "$th4_dir/pgrep"

unit_test_run "T-h4: pid_continuity_check_intersection empty" "$th4_dir" '
	saved="1234 5678 "
	if pid_continuity_check_intersection "$saved" "fake-pattern"; then
		echo "expected return 1 on empty intersection, got 0" >&2
		exit 1
	fi
'

# --- T-h5/T-h6/T-h7: finalize_cycle_logcat — bash function overrides for builtins ---
# `kill` and `sync` are bash builtins; PATH stubs are bypassed. Use bash function
# definitions inside the test body — bash resolves functions BEFORE builtins.
# Also: $$ in a subshell still expands to PARENT's PID (per bash docs); using
# a mock PID like 99999 + function-override of kill avoids any actual signal delivery.

th5_dir="$tmp_dir/th5"
mkdir -p "$th5_dir"
th5_log="$th5_dir/calls.log"
: >"$th5_log"

# T-h5: PID set + alive (mocked kill -0 returns 0): kill -TERM called, sync called.
# Broke-the-code: removing kill -TERM call would FAIL the kill-TERM grep below.
unit_test_run "T-h5: finalize_cycle_logcat PID-set-alive kills+syncs" "$th5_dir" "
	# Override builtins in this subshell to avoid signal delivery.
	kill() {
		case \"\${1:-}\" in
			-0)  printf 'kill -0 %s\n' \"\${2:-}\" >>'$th5_log'; return 0 ;;
			*)   printf 'kill %s\n' \"\$*\" >>'$th5_log'; return 0 ;;
		esac
	}
	sync() { printf 'sync\n' >>'$th5_log'; }
	timeout() { printf 'timeout %s\n' \"\$*\" >>'$th5_log'; return 0; }
	wait() { return 0; }
	CYCLE_LOGCAT_TEE_PID=99999
	finalize_cycle_logcat
	grep -q '^kill -TERM' '$th5_log' || { echo 'missing kill -TERM call' >&2; cat '$th5_log' >&2; exit 1; }
	grep -q '^sync' '$th5_log' || { echo 'missing sync call' >&2; cat '$th5_log' >&2; exit 2; }
"

# T-h6: PID set + DEAD (mocked kill -0 returns 1): kill -TERM NOT called, sync STILL called.
# Broke-the-code (v19 regression): wrapping sync inside `&& kill -0` predicate
# would skip sync when PID is set+dead. v20 two-tier nested-if preserves sync.
th6_dir="$tmp_dir/th6"
mkdir -p "$th6_dir"
th6_log="$th6_dir/calls.log"
: >"$th6_log"

unit_test_run "T-h6: finalize_cycle_logcat PID-set-DEAD syncs (v20 fix)" "$th6_dir" "
	kill() {
		case \"\${1:-}\" in
			-0)  printf 'kill -0 %s (DEAD)\n' \"\${2:-}\" >>'$th6_log'; return 1 ;;
			*)   printf 'kill %s\n' \"\$*\" >>'$th6_log'; return 0 ;;
		esac
	}
	sync() { printf 'sync\n' >>'$th6_log'; }
	timeout() { printf 'timeout %s\n' \"\$*\" >>'$th6_log'; return 0; }
	wait() { return 0; }
	CYCLE_LOGCAT_TEE_PID=99999
	finalize_cycle_logcat
	# Outer if (-n PID set) passes → enters block.
	# Inner if (kill -0 PID) FAILS → skips kill -TERM + timeout wait.
	# sync MUST still execute (v20 two-tier nested-if defensive flush).
	grep -q '^sync' '$th6_log' || { echo 'v20 regression: sync skipped on PID-set-DEAD' >&2; cat '$th6_log' >&2; exit 1; }
	# kill -TERM should NOT be called (inner if skipped).
	if grep -q '^kill -TERM' '$th6_log'; then
		echo 'kill -TERM should not run on dead PID' >&2
		cat '$th6_log' >&2
		exit 2
	fi
"

# T-h7: PID unset → Path B no-op (no kill, no sync).
# Broke-the-code: removing outer `[ -n \"\$CYCLE_LOGCAT_TEE_PID\" ]` guard would
# call sync on Path B → wasted I/O on every cycle for process-substitution path.
th7_dir="$tmp_dir/th7"
mkdir -p "$th7_dir"
th7_log="$th7_dir/calls.log"
: >"$th7_log"

unit_test_run "T-h7: finalize_cycle_logcat PID-unset no-op (Path B)" "$th7_dir" "
	kill() { printf 'kill %s\n' \"\$*\" >>'$th7_log'; return 0; }
	sync() { printf 'sync\n' >>'$th7_log'; }
	CYCLE_LOGCAT_TEE_PID=''
	finalize_cycle_logcat
	if [ -s '$th7_log' ]; then
		echo 'Path B should be no-op; got:' >&2
		cat '$th7_log' >&2
		exit 1
	fi
"

# --- T-h8: classify_via_fallback — sets CLASSIFICATION_PATH=fallback in main shell ---
# Broke-the-code: invoking via \$(classify_via_fallback) would lose assignment to subshell
# (per v17 §3.1.7.1 main-shell scope warning); test invokes directly to verify the
# helper itself is correct (independent of caller-side discipline).
th8_dir="$tmp_dir/th8"
mkdir -p "$th8_dir"

unit_test_run "T-h8: classify_via_fallback sets CLASSIFICATION_PATH=fallback" "$th8_dir" '
	CLASSIFICATION_PATH="direct"
	classify_via_fallback
	[ "$CLASSIFICATION_PATH" = "fallback" ] || { echo "expected fallback, got: $CLASSIFICATION_PATH" >&2; exit 1; }
'

# --- T-h9: handle_unknown_disambiguation — Both-ALIVE: log_warn + CLASSIFICATION_PATH=fallback ---
# Broke-the-code (v18 fix verification): omitting the echo "WARN" call would leave
# the comment-vs-code drift uncorrected per F-v16-2.
# Override route_to_t27_spontaneous_context_dump (which calls finish_with_result→exit)
# with a bash function so the test body keeps running after the call.
th9_dir="$tmp_dir/th9"
mkdir -p "$th9_dir"
th9_log="$th9_dir/disambig.log"
th9_route_log="$th9_dir/route.log"

unit_test_run "T-h9: handle_unknown_disambiguation Both-ALIVE log_warn" "$th9_dir" "
	# Override the routing helper so the helper returns instead of exiting.
	route_to_t27_spontaneous_context_dump() { printf 'route_to_t27\n' >>'$th9_route_log'; }
	W_VERDICT=ALIVE
	F_VERDICT=ALIVE
	CLASSIFICATION_PATH=''
	handle_unknown_disambiguation 2>'$th9_log'
	[ \"\$CLASSIFICATION_PATH\" = 'fallback' ] || { echo \"CLASSIFICATION_PATH not set to fallback: \$CLASSIFICATION_PATH\" >&2; exit 1; }
	grep -q 'WARN.*W=ALIVE.*F=ALIVE' '$th9_log' || { echo 'missing log_warn for Both-ALIVE' >&2; cat '$th9_log' >&2; exit 2; }
	grep -q 'route_to_t27' '$th9_route_log' || { echo 'route_to_t27 not invoked' >&2; exit 3; }
"

# --- T-h10: triple_source_either_dead_wins — pgrep DEAD wins → DEAD verdict ---
# Broke-the-code: short-circuit on pgrep ALIVE without checking logcat/pidcont
# would mask logcat-only or pidcont-only deaths.
th10_dir="$tmp_dir/th10"
mkdir -p "$th10_dir"

unit_test_run "T-h10: triple_source_either_dead_wins pgrep-DEAD-wins" "$th10_dir" '
	W_PGREP=DEAD; W_LOGCAT=ALIVE; W_PIDCONT=ALIVE
	verdict=$(triple_source_either_dead_wins "$W_PGREP" "$W_LOGCAT" "$W_PIDCONT")
	[ "$verdict" = "DEAD" ] || { echo "expected DEAD got $verdict" >&2; exit 1; }
'

# --- T-h11: triple_source_either_dead_wins — all ALIVE → ALIVE verdict ---
# Broke-the-code: inverting the if/else branches (DEAD on all-ALIVE) would
# misclassify healthy cycles as DEAD → all subsequent dispatch would fall
# into handle_unknown_disambiguation Both-DEAD branch unconditionally,
# routing every successful cycle to T2.7 fallback. This test would FAIL
# with `expected ALIVE got DEAD` because the all-ALIVE input no longer
# yields the ALIVE verdict the helper's name and §3.1 contract promise.
unit_test_run "T-h11: triple_source_either_dead_wins all-ALIVE" "$th10_dir" '
	W_PGREP=ALIVE; W_LOGCAT=ALIVE; W_PIDCONT=ALIVE
	verdict=$(triple_source_either_dead_wins "$W_PGREP" "$W_LOGCAT" "$W_PIDCONT")
	[ "$verdict" = "ALIVE" ] || { echo "expected ALIVE got $verdict" >&2; exit 1; }
'

# --- T-h12: triple_source_either_dead_wins — pidcont DEAD wins → DEAD verdict ---
# Broke-the-code: omitting the third disjunct `|| [ "$pidcont_verdict" = "DEAD" ]`
# from the explicit if/then/else combiner would silently drop the PID-continuity
# evidence source — the §3.1.5 intersection check whose role is catching
# all-cycle PID respawns that pgrep + logcat both miss. This test would FAIL
# with `expected DEAD got ALIVE` because pidcont=DEAD with the other two ALIVE
# would no longer trigger the DEAD verdict, regressing the v8 NEW-MAJOR-
# LIVENESS-ASYMMETRY resolution embedded in the triple-source contract.
unit_test_run "T-h12: triple_source_either_dead_wins pidcont-DEAD-wins" "$th10_dir" '
	W_PGREP=ALIVE; W_LOGCAT=ALIVE; W_PIDCONT=DEAD
	verdict=$(triple_source_either_dead_wins "$W_PGREP" "$W_LOGCAT" "$W_PIDCONT")
	[ "$verdict" = "DEAD" ] || { echo "expected DEAD got $verdict" >&2; exit 1; }
'

# --- T-h13: handle_unknown_disambiguation — W=DEAD,F=ALIVE → routes via classify_runtime_teardown ---
# Broke-the-code: routing W=DEAD,F=ALIVE through classify_via_fallback (instead of
# classify chain) would lose the LMKD/reboot/AFTER_TAP discrimination.
th13_dir="$tmp_dir/th13"
mkdir -p "$th13_dir"
th13_route_log="$th13_dir/route.log"

unit_test_run "T-h13: handle_unknown_disambiguation W-DEAD-only → classify chain" "$th13_dir" "
	# Override classify_runtime_teardown to capture the call without exiting.
	classify_runtime_teardown() { printf 'classify %s %s\n' \"\${1:-}\" \"\${2:-}\" >>'$th13_route_log'; }
	route_to_t27_spontaneous_context_dump() { printf 'route_to_t27\n' >>'$th13_route_log'; }
	W_VERDICT=DEAD
	F_VERDICT=ALIVE
	CLASSIFICATION_PATH=''
	handle_unknown_disambiguation 'lbl-state' 'lbl-logcat' || true
	grep -q '^classify lbl-state lbl-logcat' '$th13_route_log' || { echo 'classify_runtime_teardown not called for W-DEAD-only' >&2; cat '$th13_route_log' >&2; exit 1; }
	# Should NOT route to T2.7 fallback in W-DEAD-only branch.
	if grep -q '^route_to_t27' '$th13_route_log'; then
		echo 'unexpected T2.7 route on W-DEAD-only branch' >&2
		exit 2
	fi
"

# --- T-h14: handle_unknown_disambiguation — W=ALIVE,F=DEAD → fallback + route_to_t27 ---
# Broke-the-code: forgetting to set CLASSIFICATION_PATH=fallback would leak
# direct-routing semantics into a context where catch-all is expected.
th14_dir="$tmp_dir/th14"
mkdir -p "$th14_dir"
th14_route_log="$th14_dir/route.log"

unit_test_run "T-h14: handle_unknown_disambiguation F-DEAD-only → fallback" "$th14_dir" "
	classify_runtime_teardown() { printf 'classify\n' >>'$th14_route_log'; }
	route_to_t27_spontaneous_context_dump() { printf 'route_to_t27\n' >>'$th14_route_log'; }
	W_VERDICT=ALIVE
	F_VERDICT=DEAD
	CLASSIFICATION_PATH=''
	handle_unknown_disambiguation || true
	[ \"\$CLASSIFICATION_PATH\" = 'fallback' ] || { echo \"CLASSIFICATION_PATH not fallback: \$CLASSIFICATION_PATH\" >&2; exit 1; }
	grep -q '^route_to_t27' '$th14_route_log' || { echo 'route_to_t27 not invoked on F-DEAD-only' >&2; cat '$th14_route_log' >&2; exit 2; }
	if grep -q '^classify' '$th14_route_log'; then
		echo 'classify_runtime_teardown should NOT be called on F-DEAD-only' >&2
		exit 3
	fi
"

# --- T-h15: handle_unknown_disambiguation — W=DEAD,F=DEAD → fallback + route_to_t27 ---
# Broke-the-code: missing the W=DEAD,F=DEAD elif branch would fall through to
# Both-ALIVE else branch, emitting a spurious WARN log.
th15_dir="$tmp_dir/th15"
mkdir -p "$th15_dir"
th15_route_log="$th15_dir/route.log"
th15_warn_log="$th15_dir/warn.log"

unit_test_run "T-h15: handle_unknown_disambiguation Both-DEAD → fallback no-warn" "$th15_dir" "
	classify_runtime_teardown() { printf 'classify\n' >>'$th15_route_log'; }
	route_to_t27_spontaneous_context_dump() { printf 'route_to_t27\n' >>'$th15_route_log'; }
	W_VERDICT=DEAD
	F_VERDICT=DEAD
	CLASSIFICATION_PATH=''
	handle_unknown_disambiguation 2>'$th15_warn_log' || true
	[ \"\$CLASSIFICATION_PATH\" = 'fallback' ] || { echo \"CLASSIFICATION_PATH not fallback: \$CLASSIFICATION_PATH\" >&2; exit 1; }
	grep -q '^route_to_t27' '$th15_route_log' || { echo 'route_to_t27 not invoked on Both-DEAD' >&2; exit 2; }
	# Both-DEAD is a known/expected case — should NOT emit the Both-ALIVE WARN.
	if grep -q 'WARN.*W=ALIVE.*F=ALIVE' '$th15_warn_log'; then
		echo 'unexpected Both-ALIVE WARN emitted on Both-DEAD branch' >&2
		cat '$th15_warn_log' >&2
		exit 3
	fi
"

echo "All unit tests passed."
