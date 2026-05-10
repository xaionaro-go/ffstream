#!/usr/bin/env bash
set -euo pipefail

# Test fixture for final-phone-health-gate.sh.
#
# Style mirrors the Goal-2/3/5 mission test fixtures: subprocess invocation
# under a mocked `adb` stub, plus sourcing-based unit tests on helpers and the
# threshold-check python. Each test has its own `# Broke-the-code:` block per
# Task #20 durable rule + Task #2/#3/#4 reinforcements.
#
# Pre-SUBMIT self-grep verification:
#   grep -cE "^# Broke-the-code"  must equal  grep -cE "^# --- T-[uh][0-9]+:"

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/final-phone-health-gate.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

tmp_dir=$(mktemp -d)
cleanup() {
	rm -rf "$tmp_dir"
}
trap cleanup EXIT
[ -n "${KEEP_TMP:-}" ] && trap - EXIT

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

# ----- Mock adb -----------------------------------------------------------
# Dispatches on argument shape. Per-query counter files live under $bin_dir to
# allow sample-aware behaviour (sample-1 vs sample-2 produce different output
# when FAKE_ADB_<query>_S{1,2}_MODE is set).
#
# Supported env knobs:
#   FAKE_ADB_GET_STATE_OUT       text emitted on `get-state` (default "device")
#   FAKE_ADB_GET_STATE_EXIT      exit code (default 0)
#   FAKE_ADB_CRASH_S{1,2}_MODE   "clean" (default) | "with_crash"
#   FAKE_ADB_PS_S{1,2}_MODE      "clean" | "with_streaming"
#   FAKE_ADB_LOAD_S{1,2}         loadavg first-line (default "0.50 0.40 0.30 1/200 1234")
#   FAKE_ADB_MEM_S{1,2}_KB       MemAvailable kB integer (default 1048576)
#   FAKE_ADB_UI_S{1,2}_EXIT      ui-responsive exit code (default 0)
#
# Each query maintains its own counter; first call → S1, second call → S2.
cat >"$bin_dir/adb" <<STUB
#!/usr/bin/env bash
ctr_dir="$bin_dir"
STUB
cat >>"$bin_dir/adb" <<'STUB'
# Find query type by scanning args.
serial=""
shell_cmd=""
get_state=0
mode=""
i=0
args=("$@")
while [ "$i" -lt "${#args[@]}" ]; do
	arg="${args[$i]}"
	case "$arg" in
		-s)
			i=$((i + 1))
			serial="${args[$i]:-}"
			;;
		get-state)
			get_state=1
			;;
		shell)
			i=$((i + 1))
			shell_cmd="${args[$i]:-}"
			;;
	esac
	i=$((i + 1))
done

bump_counter() {
	local query="$1"
	local ctr_file="$ctr_dir/adb-$query.ctr"
	local cur
	cur="$(cat "$ctr_file" 2>/dev/null || echo 0)"
	cur=$((cur + 1))
	printf '%d\n' "$cur" >"$ctr_file"
	printf '%d' "$cur"
}

resolve_mode() {
	local query="$1"
	local sample_idx="$2"
	local default="$3"
	local var="FAKE_ADB_${query}_S${sample_idx}_MODE"
	printf '%s' "${!var:-$default}"
}

resolve_value() {
	local query="$1"
	local sample_idx="$2"
	local default="$3"
	local var="FAKE_ADB_${query}_S${sample_idx}"
	printf '%s' "${!var:-$default}"
}

if [ "$get_state" -eq 1 ]; then
	bump_counter STATE >/dev/null
	printf '%s\n' "${FAKE_ADB_GET_STATE_OUT:-device}"
	exit "${FAKE_ADB_GET_STATE_EXIT:-0}"
fi

case "$shell_cmd" in
	*'awk '*'$4 == "crash_dump64"'*)
		idx="$(bump_counter CRASH)"
		mode="$(resolve_mode CRASH "$idx" clean)"
		case "$mode" in
			with_crash)
				printf 'root         9999 1 crash_dump64 /system/bin/crash_dump64 -p 8888\n'
				;;
		esac
		exit 0
		;;
	*'ps -A -o USER,PID,PPID,ARGS'*)
		idx="$(bump_counter PS)"
		mode="$(resolve_mode PS "$idx" clean)"
		# Always emit a couple of unrelated processes so blank file does NOT
		# pass the targeted-streaming regex by accident.
		printf 'root         1 0 init\n'
		printf 'shell      999 1 sh\n'
		case "$mode" in
			with_streaming)
				printf 'root      8888 1 /vendor/bin/ffstream-camera --foo\n'
				;;
			with_mediamtx)
				printf 'root      7777 1 /system/bin/mediamtx\n'
				;;
		esac
		exit 0
		;;
	*'cat /proc/loadavg'*)
		idx="$(bump_counter LOAD)"
		val="$(resolve_value LOAD "$idx" '0.50 0.40 0.30 1/200 1234')"
		printf '%s\n' "$val"
		exit 0
		;;
	*'cat /proc/meminfo'*)
		idx="$(bump_counter MEM)"
		mem_var="FAKE_ADB_MEM_S${idx}_KB"
		mem_kb="${!mem_var:-1048576}"
		printf 'MemTotal:        7896420 kB\n'
		printf 'MemAvailable:    %s kB\n' "$mem_kb"
		exit 0
		;;
	*'cmd window size'*)
		idx="$(bump_counter UI)"
		exit_var="FAKE_ADB_UI_S${idx}_EXIT"
		exit_code="${!exit_var:-0}"
		printf 'Physical size: 1080x2400\n'
		printf 'Override size: not specified\n'
		exit "$exit_code"
		;;
esac
echo "mock adb: unhandled invocation: $*" >&2
exit 99
STUB
chmod +x "$bin_dir/adb"

export PATH="$bin_dir:$PATH"

# ============================================================================
# Integration tests
# ============================================================================

run_gate_test() {
	local name="$1"
	local expected_result="$2"
	local expected_status="$3"
	shift 3
	# Remaining args: KEY=VALUE env overrides.

	local run_root="$tmp_dir/run-$name"
	rm -rf "$run_root"
	mkdir -p "$run_root"
	# Reset per-query counters between runs so sample-1 truly is the first call.
	rm -f "$bin_dir"/adb-*.ctr

	set +e
	env \
		PHONE_SERIAL=mock-serial \
		RUN_ROOT="$run_root" \
		HEALTH_GAP_SECONDS=1 \
		"$@" \
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

	# Coexistence assertion: both samples' artifacts must live under the same
	# $RUN_DIR/health-final/ directory tree. This is the "all witness artifacts
	# must coexist in same run directory" acceptance gate.
	[ -d "$run_dir/health-final/sample-1" ] \
		|| fail "$name: missing sample-1 directory under $run_dir/health-final"
	[ -d "$run_dir/health-final/sample-2" ] \
		|| fail "$name: missing sample-2 directory under $run_dir/health-final"
	[ -s "$run_dir/health-final/sample-1/result.txt" ] \
		|| fail "$name: missing sample-1/result.txt"
	[ -s "$run_dir/health-final/sample-2/result.txt" ] \
		|| fail "$name: missing sample-2/result.txt"
}

# --- T-h1: happy path → FINAL_PHONE_HEALTH_PASS ---
# Broke-the-code: removing any of the per-sample dirty checks (crash_dump64
# emptiness OR targeted-streaming match emptiness OR threshold_check_python
# returning 0) would let a dirty sample pass — letting the gate falsely claim
# PASS while the phone is still leaking processes / overloaded. Test catches
# the integrated end-to-end clean contract.
run_gate_test "T-h1-happy-path" "FINAL_PHONE_HEALTH_PASS" 0

# --- T-h2: sample-1 has crash_dump64 → FAIL_SAMPLE_1_DIRTY ---
# Broke-the-code: skipping the `[ -s crash-dump64-matches.stdout ]` check, OR
# misclassifying crash-dump64 output as benign ps content, would silently let
# the gate pass while crash_dump64 was active — masking exactly the post-
# mission crash-residue scenario this gate exists to catch.
run_gate_test "T-h2-sample1-crash" "FINAL_PHONE_HEALTH_FAIL_SAMPLE_1_DIRTY" 70 \
	FAKE_ADB_CRASH_S1_MODE=with_crash

# --- T-h3: sample-1 has streaming process match → FAIL_SAMPLE_1_DIRTY ---
# Broke-the-code: a regex that fails to match `ffstream-camera` argv tokens
# (e.g., requiring `^/` anchor or omitting the embedded-token alternation)
# would let a leaked ffstream-camera supervisor or wrapper survive into the
# clean-state assertion. The matcher must catch any token containing
# `ffstream` plus the standalone `mediamtx`.
run_gate_test "T-h3-sample1-streaming" "FINAL_PHONE_HEALTH_FAIL_SAMPLE_1_DIRTY" 70 \
	FAKE_ADB_PS_S1_MODE=with_streaming

# --- T-h4: sample-1 high load → FAIL_SAMPLE_1_DIRTY ---
# Broke-the-code: removing the load-guard (or relaxing the 8.00/6.00 threshold)
# would let the gate pass while the phone is mid-crash-loop — exactly the
# guardrail mission_test_plan.md §"Final Phone Health Gate" L805-806 mandates
# (BLOCK_HIGH_LOAD).
run_gate_test "T-h4-sample1-high-load" "FINAL_PHONE_HEALTH_FAIL_SAMPLE_1_DIRTY" 70 \
	'FAKE_ADB_LOAD_S1=10.00 9.00 5.00 1/300 5555'

# --- T-h5: sample-1 low memavailable → FAIL_SAMPLE_1_DIRTY ---
# Broke-the-code: missing the MemAvailable < 524288 kB guard would let the
# gate pass on a near-OOM phone where the next mission step is at high risk
# of LMKD reaping (BLOCK_LOW_MEMAVAILABLE per spec L811-812).
run_gate_test "T-h5-sample1-low-mem" "FINAL_PHONE_HEALTH_FAIL_SAMPLE_1_DIRTY" 70 \
	FAKE_ADB_MEM_S1_KB=100000

# --- T-h6: asymmetric (sample-1 clean, sample-2 dirty) → FAIL_SAMPLE_2_DIRTY ---
# Broke-the-code: short-circuiting on first-clean (returning PASS without
# running sample-2) would mask transient cleanup leaks where a process is
# briefly absent at sample-1 but reappears (e.g., supervisor respawning a
# zombie). The two-sample ≥10s contract specifically defends against this.
run_gate_test "T-h6-asymmetric-sample2-dirty" "FINAL_PHONE_HEALTH_FAIL_SAMPLE_2_DIRTY" 71 \
	FAKE_ADB_PS_S2_MODE=with_streaming

# --- T-h7: both samples dirty → FAIL_BOTH_SAMPLES_DIRTY ---
# Broke-the-code: collapsing the both-dirty case into the same exit code as
# either-single-dirty would lose the "this is a sustained leak, not a
# transient" signal — making operator triage harder. Distinct exit codes (70
# vs 71 vs 72) preserve sample-attribution information.
run_gate_test "T-h7-both-samples-dirty" "FINAL_PHONE_HEALTH_FAIL_BOTH_SAMPLES_DIRTY" 72 \
	FAKE_ADB_CRASH_S1_MODE=with_crash \
	FAKE_ADB_CRASH_S2_MODE=with_crash

# --- T-h8: ui-responsive fails → FAIL_SAMPLE_1_DIRTY (BLOCK_UI_RESPONSIVENESS_FAILURE) ---
# Broke-the-code: ignoring the ui-responsive command exit status would mask
# an unresponsive UI surface — phone could be in ANR / system_server stall
# while every other check looks clean. The UI guardrail catches this class.
run_gate_test "T-h8-sample1-ui-unresponsive" "FINAL_PHONE_HEALTH_FAIL_SAMPLE_1_DIRTY" 70 \
	FAKE_ADB_UI_S1_EXIT=124

# Additional assertion for T-h8: sample-1/blockers.txt must contain the
# specific BLOCK_UI_RESPONSIVENESS_FAILURE label so operators know which
# guardrail tripped.
last_run_dir="$tmp_dir/run-T-h8-sample1-ui-unresponsive"
last_health_dir="$(find "$last_run_dir" -type d -name 'health-final' | head -1)"
[ -n "$last_health_dir" ] || fail "T-h8: health-final dir not found"
grep -q '^BLOCK_UI_RESPONSIVENESS_FAILURE$' "$last_health_dir/sample-1/blockers.txt" \
	|| fail "T-h8: missing BLOCK_UI_RESPONSIVENESS_FAILURE in blockers.txt"

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
		export FINAL_HEALTH_DIR="$RUN_DIR/health-final"
		mkdir -p "$FINAL_HEALTH_DIR"
		# shellcheck disable=SC1090
		source "$script"
		eval "$body"
	)
	local rc=$?
	if [ "$rc" -ne 0 ]; then
		fail "unit test $name failed with rc=$rc"
	fi
}

# --- T-u1: run_final_health_cmd writes cmd/stdout/stderr/status quartet ---
# Broke-the-code: dropping any one of the four artifact files (cmd, stdout,
# stderr, status) would silently lose evidence — operators triaging a failure
# need all four, especially the .cmd reproduction text and the .status numeric
# code. The quartet contract is the unit of evidence.
tu1_dir="$tmp_dir/tu1"
mkdir -p "$tu1_dir/run-dir/health-final"
unit_test_run "T-u1: run_final_health_cmd quartet" "$tu1_dir" "
	hd=\"\$FINAL_HEALTH_DIR/sample-test\"
	mkdir -p \"\$hd\"
	run_final_health_cmd \"\$hd\" myname 5 /bin/echo hello-world || true
	[ -s \"\$hd/myname.cmd\" ] || { echo 'missing cmd file' >&2; exit 1; }
	[ -f \"\$hd/myname.stdout\" ] || { echo 'missing stdout file' >&2; exit 1; }
	[ -f \"\$hd/myname.stderr\" ] || { echo 'missing stderr file' >&2; exit 1; }
	[ -s \"\$hd/myname.status\" ] || { echo 'missing status file' >&2; exit 1; }
	grep -q 'hello-world' \"\$hd/myname.stdout\" || { echo 'missing stdout content' >&2; exit 1; }
	[ \"\$(cat \"\$hd/myname.status\")\" = '0' ] || { echo 'wrong status' >&2; exit 1; }
"

# --- T-u2: threshold_check_python returns 0 on healthy values ---
# Broke-the-code: a threshold-check that always returns non-zero would block
# every valid sample → false positive on every clean phone, defeating the
# gate's discrimination value.
tu2_dir="$tmp_dir/tu2"
mkdir -p "$tu2_dir/run-dir/health-final"
printf '0.10 0.05 0.02 1/100 1234\n' >"$tu2_dir/loadavg.txt"
printf 'MemTotal:        7896420 kB\nMemAvailable:    2097152 kB\n' >"$tu2_dir/meminfo.txt"
unit_test_run "T-u2: threshold healthy" "$tu2_dir" "
	threshold_check_python '$tu2_dir/loadavg.txt' '$tu2_dir/meminfo.txt' >/dev/null
"

# --- T-u3: threshold_check_python returns 1 on high load ---
# Broke-the-code: a threshold-check that ignores the load1/load5 numeric
# values and only checks memory would let a crash-looping phone (high load,
# adequate free memory) sail past the gate.
tu3_dir="$tmp_dir/tu3"
mkdir -p "$tu3_dir/run-dir/health-final"
printf '10.00 9.00 8.00 5/300 5555\n' >"$tu3_dir/loadavg.txt"
printf 'MemTotal:        7896420 kB\nMemAvailable:    2097152 kB\n' >"$tu3_dir/meminfo.txt"
unit_test_run "T-u3: threshold high load" "$tu3_dir" "
	if threshold_check_python '$tu3_dir/loadavg.txt' '$tu3_dir/meminfo.txt' >/dev/null; then
		echo 'false positive: high load passed threshold gate' >&2
		exit 1
	fi
"

# --- T-u4: threshold_check_python returns 1 on low memavailable ---
# Broke-the-code: a threshold-check that omits the MemAvailable parse (or
# defaults to 0 silently when the regex misses) would either always-block
# (false positive) or always-pass (false negative, masking near-OOM state).
tu4_dir="$tmp_dir/tu4"
mkdir -p "$tu4_dir/run-dir/health-final"
printf '0.10 0.05 0.02 1/100 1234\n' >"$tu4_dir/loadavg.txt"
printf 'MemTotal:        7896420 kB\nMemAvailable:    100000 kB\n' >"$tu4_dir/meminfo.txt"
unit_test_run "T-u4: threshold low mem" "$tu4_dir" "
	if threshold_check_python '$tu4_dir/loadavg.txt' '$tu4_dir/meminfo.txt' >/dev/null; then
		echo 'false positive: low memavailable passed threshold gate' >&2
		exit 1
	fi
"

# --- T-u5: TARGETED_STREAMING_REGEX positive AND negative coverage ---
# Broke-the-code: a regex that matches "ffstream" but also matches
# `ffstreamlike-unrelated` (false positive) would block clean phones with
# coincidentally-named processes; a regex that fails on `/vendor/bin/ffstream-
# camera` argv (false negative) would let leaked supervisors slip past. Both
# polarities are covered here in one test.
tu5_dir="$tmp_dir/tu5"
mkdir -p "$tu5_dir/run-dir/health-final"
unit_test_run "T-u5: streaming regex polarities" "$tu5_dir" "
	# Positives — must match.
	for line in \
		'root      8888 1 /vendor/bin/ffstream-camera --foo' \
		'root      7777 1 /system/bin/mediamtx' \
		'root      6666 1 ffstream-mediamtx-supervisor' \
		'root      5555 1 /sbin/loop-run-ffstream.sh'; do
		if ! printf '%s\n' \"\$line\" | grep -qE \"\$TARGETED_STREAMING_REGEX\"; then
			echo \"false negative: regex did not match expected positive: \$line\" >&2
			exit 1
		fi
	done
	# Negatives — must NOT match.
	for line in \
		'root         1 0 init' \
		'shell      999 1 sh' \
		'root      2222 1 /system/bin/logd'; do
		if printf '%s\n' \"\$line\" | grep -qE \"\$TARGETED_STREAMING_REGEX\"; then
			echo \"false positive: regex matched negative line: \$line\" >&2
			exit 1
		fi
	done
"

# --- T-u6: artifact-coexistence — sample-1 + sample-2 in same RUN_DIR ---
# Broke-the-code: a sample-2 implementation that wrote to a different RUN_DIR
# (e.g., re-resolved RUN_DIR via date timestamp on each sample) would split
# evidence across two trees, violating the "all witness artifacts must coexist
# in same run directory" acceptance gate. Here we exercise the helper's
# directory contract directly.
tu6_dir="$tmp_dir/tu6"
mkdir -p "$tu6_dir/run-dir/health-final"
unit_test_run "T-u6: artifact coexistence" "$tu6_dir" "
	hd1=\"\$FINAL_HEALTH_DIR/sample-1\"
	hd2=\"\$FINAL_HEALTH_DIR/sample-2\"
	mkdir -p \"\$hd1\" \"\$hd2\"
	# Both directories must share the same parent (FINAL_HEALTH_DIR == \$RUN_DIR/health-final).
	parent1=\"\$(dirname \"\$hd1\")\"
	parent2=\"\$(dirname \"\$hd2\")\"
	[ \"\$parent1\" = \"\$parent2\" ] \
		|| { echo \"sample dirs differ in parent: \$parent1 vs \$parent2\" >&2; exit 1; }
	[ \"\$parent1\" = \"\$FINAL_HEALTH_DIR\" ] \
		|| { echo \"sample parent != FINAL_HEALTH_DIR\" >&2; exit 1; }
"

echo "All final phone health gate tests passed."
