#!/usr/bin/env bash
set -Eeuo pipefail

# Task #5 — Final phone health gate. Runs after the mission witness sequence
# (Goal 1 + Goal 2 + Goal 3 + Goal 5) has completed and before claiming PASS.
# Implements mission_test_plan.md §"Final Phone Health Gate" verbatim.
#
# Two fresh serial-targeted samples ≥HEALTH_GAP_SECONDS apart. Each sample
# captures phone-state, crash-dump64 matches, full ps, loadavg, meminfo,
# ui-responsiveness, targeted streaming-process matches, and a numeric load+mem
# threshold check. A clean sample requires ALL of:
#   - crash-dump64 match output empty
#   - targeted-streaming-process match output empty
#   - threshold-check exit 0 (load1 < 8.00, load5 < 6.00, MemAvailable ≥ 524288 kB)
#   - every adb command succeeds (status 0)
#   - ui-responsive command succeeds (status 0)
#   - process-match grep status ≤ 1 (0 = match, 1 = no match; >1 is grep error)
#
# All artifacts coexist under $RUN_DIR/health-final/sample-{1,2}/.
#
# Forbidden actions (constraints honoured by this script body):
#   - no factory reset / userdata erase / adb root / fastboot -w
#   - no su 0 / chmod / chcon / kill / pkill / killall
#   - no dev-box mediamtx evidence
#   - no substituted topology / priority-0 / AddInput / ffstreamctl inputs add
#   - no validating built-in camera through /pixel/dji-osmo-pocket-3-merged
# This gate observes phone process state read-only; it never mutates the phone.

PHONE_SERIAL="${PHONE_SERIAL:-41041JEKB08092}"
RUN_ROOT="${RUN_ROOT:-${HOME}/tmp/test-final-phone-health-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="${RUN_DIR:-$RUN_ROOT/final-phone-health-$(date -u +%Y%m%dT%H%M%SZ)}"
FINAL_HEALTH_DIR="$RUN_DIR/health-final"
HEALTH_GAP_SECONDS="${HEALTH_GAP_SECONDS:-10}"

# Targeted streaming-process matcher per mission_test_plan.md §"Phone Health
# Inline Preflight" L223-225 + §"Final Phone Health Gate" L780-782. Matches
# every process token containing `ffstream` plus the standalone `mediamtx`.
TARGETED_STREAMING_REGEX='(^|[[:space:]/])([^[:space:]/]*ffstream[^[:space:]]*|mediamtx)([[:space:]]|$)'

# run_final_health_cmd — write cmd / stdout / stderr / status quartet for one
# timeout-bounded read-only adb invocation under a per-sample directory.
run_final_health_cmd() {
	local health_dir="$1"
	local name="$2"
	local timeout_s="$3"
	shift 3
	{
		printf 'timeout %s' "$timeout_s"
		printf ' %q' "$@"
		printf '\n'
	} >"$health_dir/$name.cmd"
	set +e
	timeout "$timeout_s" "$@" \
		>"$health_dir/$name.stdout" 2>"$health_dir/$name.stderr"
	local status="$?"
	set -e
	printf '%s\n' "$status" >"$health_dir/$name.status"
	return "$status"
}

# threshold_check_python — emit BLOCK_HIGH_LOAD / BLOCK_LOW_MEMAVAILABLE and
# return exit 1 when guardrails breach. Thresholds match the inline preflight:
# load1 < 8.00, load5 < 6.00, MemAvailable ≥ 524288 kB.
threshold_check_python() {
	local loadavg_path="$1"
	local meminfo_path="$2"
	python3 - "$loadavg_path" "$meminfo_path" <<'PY'
import re
import sys

load_path, meminfo_path = sys.argv[1:3]
load_parts = open(load_path, encoding="utf-8").read().split()
load1 = float(load_parts[0]) if len(load_parts) >= 1 else 999.0
load5 = float(load_parts[1]) if len(load_parts) >= 2 else 999.0
meminfo = open(meminfo_path, encoding="utf-8").read()
match = re.search(r"^MemAvailable:\s+(\d+)\s+kB$", meminfo, re.MULTILINE)
mem_available_kb = int(match.group(1)) if match else 0

failed = False
if load1 >= 8.00 or load5 >= 6.00:
    print(f"BLOCK_HIGH_LOAD load1={load1:.2f} load5={load5:.2f}")
    failed = True
else:
    print(f"load_ok load1={load1:.2f} load5={load5:.2f}")

if mem_available_kb < 524288:
    print(f"BLOCK_LOW_MEMAVAILABLE mem_available_kb={mem_available_kb}")
    failed = True
else:
    print(f"memavailable_ok mem_available_kb={mem_available_kb}")

sys.exit(1 if failed else 0)
PY
}

# run_final_health_sample — one full sample under $FINAL_HEALTH_DIR/<sample_name>.
# Returns 0 iff the sample is clean. Always writes a result.txt + blockers.txt.
run_final_health_sample() {
	local sample_name="$1"
	local health_dir="$FINAL_HEALTH_DIR/$sample_name"
	mkdir -p "$health_dir"

	run_final_health_cmd "$health_dir" phone-state 10 \
		adb -s "$PHONE_SERIAL" get-state || true
	run_final_health_cmd "$health_dir" crash-dump64-matches 10 \
		adb -s "$PHONE_SERIAL" shell \
		'ps -A -o USER,PID,PPID,NAME,ARGS | awk '\''$4 == "crash_dump64" { print }'\''' || true
	run_final_health_cmd "$health_dir" ps-full 10 \
		adb -s "$PHONE_SERIAL" shell 'ps -A -o USER,PID,PPID,ARGS' || true
	run_final_health_cmd "$health_dir" loadavg 10 \
		adb -s "$PHONE_SERIAL" shell 'cat /proc/loadavg' || true
	run_final_health_cmd "$health_dir" meminfo 10 \
		adb -s "$PHONE_SERIAL" shell 'cat /proc/meminfo' || true
	run_final_health_cmd "$health_dir" ui-responsive 15 \
		adb -s "$PHONE_SERIAL" shell \
		'cmd window size && dumpsys window displays | head -80' || true

	# Targeted streaming-process matches grep — emits non-zero only when no
	# match (which is the desired clean state). status > 1 is a grep error and
	# blocks separately.
	printf '%s\n' \
		"grep -E '$TARGETED_STREAMING_REGEX' \"$health_dir/ps-full.stdout\"" \
		>"$health_dir/targeted-streaming-process-matches.cmd"
	set +e
	grep -E "$TARGETED_STREAMING_REGEX" "$health_dir/ps-full.stdout" \
		>"$health_dir/targeted-streaming-process-matches.txt" \
		2>"$health_dir/targeted-streaming-process-matches.stderr"
	local grep_status="$?"
	set -e
	printf '%s\n' "$grep_status" \
		>"$health_dir/targeted-streaming-process-matches.status"

	printf 'python3 - %q %q\n' \
		"$health_dir/loadavg.stdout" "$health_dir/meminfo.stdout" \
		>"$health_dir/threshold-check.cmd"
	set +e
	threshold_check_python "$health_dir/loadavg.stdout" "$health_dir/meminfo.stdout" \
		>"$health_dir/threshold-check.stdout" \
		2>"$health_dir/threshold-check.stderr"
	local threshold_status="$?"
	set -e
	printf '%s\n' "$threshold_status" >"$health_dir/threshold-check.status"

	# Aggregate blockers. Touch blockers.txt so callers can `[ -s ... ]` even
	# when the sample is clean.
	: >"$health_dir/blockers.txt"
	local health_failed=0
	local status_file
	for status_file in \
		"$health_dir/phone-state.status" \
		"$health_dir/crash-dump64-matches.status" \
		"$health_dir/ps-full.status" \
		"$health_dir/loadavg.status" \
		"$health_dir/meminfo.status" \
		"$health_dir/ui-responsive.status" \
		"$health_dir/threshold-check.status"; do
		if [ "$(cat "$status_file")" -ne 0 ]; then
			echo "BLOCK_HEALTH_COMMAND $status_file" >>"$health_dir/blockers.txt"
			health_failed=1
		fi
	done

	if [ "$(cat "$health_dir/ui-responsive.status")" -ne 0 ]; then
		echo "BLOCK_UI_RESPONSIVENESS_FAILURE" >>"$health_dir/blockers.txt"
		health_failed=1
	fi
	if [ "$grep_status" -gt 1 ]; then
		echo "BLOCK_PROCESS_MATCH_GREP_FAILURE status=$grep_status" \
			>>"$health_dir/blockers.txt"
		health_failed=1
	fi
	if [ -s "$health_dir/crash-dump64-matches.stdout" ]; then
		echo "BLOCK_CRASH_DUMP64_ACTIVE" >>"$health_dir/blockers.txt"
		health_failed=1
	fi
	if [ -s "$health_dir/targeted-streaming-process-matches.txt" ]; then
		echo "BLOCK_TARGET_STREAMING_PROCESS_ACTIVE" >>"$health_dir/blockers.txt"
		health_failed=1
	fi

	if [ "$health_failed" -ne 0 ]; then
		echo "PHONE_FINAL_HEALTH_NEEDS_RECOVERY" >"$health_dir/result.txt"
		return 1
	fi
	echo "PHONE_FINAL_HEALTH_SAMPLE_CLEAN" >"$health_dir/result.txt"
	return 0
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
	mkdir -p "$FINAL_HEALTH_DIR"
	printf '%s\n' "$RUN_DIR" >"$RUN_ROOT/latest-final-phone-health-run.txt"

	date -u +%Y-%m-%dT%H:%M:%SZ >"$RUN_DIR/started-at.txt"
	{
		printf 'PHONE_SERIAL=%s\n' "$PHONE_SERIAL"
		printf 'RUN_DIR=%s\n' "$RUN_DIR"
		printf 'FINAL_HEALTH_DIR=%s\n' "$FINAL_HEALTH_DIR"
		printf 'HEALTH_GAP_SECONDS=%s\n' "$HEALTH_GAP_SECONDS"
	} >"$RUN_DIR/config.txt"

	local sample1_status=0
	local sample2_status=0

	if ! run_final_health_sample sample-1; then
		sample1_status=1
	fi

	# Spec mandates ≥10s between samples regardless of sample-1 outcome — both
	# samples are evidence in the gate decision.
	sleep "$HEALTH_GAP_SECONDS"

	if ! run_final_health_sample sample-2; then
		sample2_status=1
	fi

	if [ "$sample1_status" -eq 0 ] && [ "$sample2_status" -eq 0 ]; then
		finish_with_result "FINAL_PHONE_HEALTH_PASS" 0
	fi
	if [ "$sample1_status" -ne 0 ] && [ "$sample2_status" -ne 0 ]; then
		finish_with_result "FINAL_PHONE_HEALTH_FAIL_BOTH_SAMPLES_DIRTY" 72
	fi
	if [ "$sample1_status" -ne 0 ]; then
		finish_with_result "FINAL_PHONE_HEALTH_FAIL_SAMPLE_1_DIRTY" 70
	fi
	finish_with_result "FINAL_PHONE_HEALTH_FAIL_SAMPLE_2_DIRTY" 71
}

# Sourcability guard — allows test fixtures to source helpers without triggering main flow.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
	main "$@"
fi
