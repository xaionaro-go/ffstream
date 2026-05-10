#!/bin/bash
# Tests that loop-run-ffstream-camera.sh enforces a size cap on its
# captured-stdout log file via in-script copytruncate (no logrotate dep),
# per Task #48.
#
# Mirrors loop-run-ffstream_rotation_test.sh (TC1+TC2+TC4+TC5) for the
# camera-side supervisor. See that test file's header for the rationale
# behind each test case.

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/loop-run-ffstream-camera.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

tmp_dir=$(mktemp -d)
cleanup() {
	# TC6 chmods a sub-directory to 000 to simulate cp failure; restore
	# write permission tree-wide before rm so the cleanup succeeds.
	chmod -R +w "$tmp_dir" 2>/dev/null || true
	rm -rf "$tmp_dir"
}
trap cleanup EXIT

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

cat > "$bin_dir/run-ffstream-camera.sh" <<'STUB'
#!/bin/bash
exit 78
STUB
chmod +x "$bin_dir"/*

export PATH="$bin_dir:$PATH"
export FFSTREAM_CAMERA_RUNNER="$bin_dir/run-ffstream-camera.sh"

# Per-test isolated lock files prevent cross-iteration contention
# (the camera supervisor uses flock for single-instance enforcement).

# ============================================================================
# TC1: rotate when log is over cap
# ============================================================================
tc1_log="$tmp_dir/tc1-loop.log"
printf 'tc1-marker-AAAA\n%.0s' {1..64} > "$tc1_log"
tc1_pre_size=$(stat -c %s "$tc1_log")

LOOP_CAMERA_LOG_FILE="$tc1_log" \
LOOP_CAMERA_LOG_MAX_BYTES=100 \
FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE="$tmp_dir/tc1.flock" \
	"$script" > "$tmp_dir/tc1.out" 2> "$tmp_dir/tc1.err" \
	|| status=$?
status=${status:-0}
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/tc1.err" >&2
	fail "TC1: supervisor stub status 78 expected; got $status"
fi
if [ ! -e "$tc1_log" ]; then
	fail "TC1: live log was deleted instead of truncated-in-place"
fi
tc1_post_size=$(stat -c %s "$tc1_log")
if [ "$tc1_post_size" -ne 0 ]; then
	fail "TC1: live log not truncated; size=$tc1_post_size (was $tc1_pre_size, cap 100)"
fi
if [ ! -f "$tc1_log.1" ]; then
	fail "TC1: .1 backup file not created during rotation"
fi
if ! grep -q "tc1-marker-AAAA" "$tc1_log.1"; then
	fail "TC1: .1 missing pre-rotation content"
fi
unset status

# ============================================================================
# TC2: no rotation when log is under cap
# ============================================================================
tc2_log="$tmp_dir/tc2-loop.log"
echo "tc2-tiny-content" > "$tc2_log"
tc2_pre_size=$(stat -c %s "$tc2_log")
tc2_pre_md5=$(md5sum < "$tc2_log")

LOOP_CAMERA_LOG_FILE="$tc2_log" \
LOOP_CAMERA_LOG_MAX_BYTES=$((1024*1024)) \
FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE="$tmp_dir/tc2.flock" \
	"$script" > "$tmp_dir/tc2.out" 2> "$tmp_dir/tc2.err" \
	|| status=$?
status=${status:-0}
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/tc2.err" >&2
	fail "TC2: supervisor stub status 78 expected; got $status"
fi
tc2_post_size=$(stat -c %s "$tc2_log")
tc2_post_md5=$(md5sum < "$tc2_log")
if [ "$tc2_pre_size" -ne "$tc2_post_size" ]; then
	fail "TC2: under-cap log was modified; pre=$tc2_pre_size post=$tc2_post_size"
fi
if [ "$tc2_pre_md5" != "$tc2_post_md5" ]; then
	fail "TC2: under-cap log content changed; pre=$tc2_pre_md5 post=$tc2_post_md5"
fi
if [ -e "$tc2_log.1" ]; then
	fail "TC2: .1 backup created spuriously when log was under cap"
fi
unset status

# ============================================================================
# TC4: cascade race — stale .1 overwritten with new pre-rotation content
# ============================================================================
tc4_log="$tmp_dir/tc4-loop.log"
printf 'tc4-NEW-content-BBBB\n%.0s' {1..64} > "$tc4_log"
echo "tc4-OLD-stale-content-CCCC" > "$tc4_log.1"

LOOP_CAMERA_LOG_FILE="$tc4_log" \
LOOP_CAMERA_LOG_MAX_BYTES=100 \
FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE="$tmp_dir/tc4.flock" \
	"$script" > "$tmp_dir/tc4.out" 2> "$tmp_dir/tc4.err" \
	|| status=$?
status=${status:-0}
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/tc4.err" >&2
	fail "TC4: supervisor stub status 78 expected; got $status"
fi
if grep -q "tc4-OLD-stale-content-CCCC" "$tc4_log.1"; then
	fail "TC4: .1 still contains stale content; rotation did not overwrite"
fi
if ! grep -q "tc4-NEW-content-BBBB" "$tc4_log.1"; then
	fail "TC4: .1 missing new pre-rotation content after cascade overwrite"
fi
unset status

# ============================================================================
# TC5: O_APPEND-after-truncate semantics
# ============================================================================
tc5_log="$tmp_dir/tc5-loop.log"
printf 'tc5-pre-rotation-DDDD\n%.0s' {1..64} > "$tc5_log"

exec 9>>"$tc5_log"

LOOP_CAMERA_LOG_FILE="$tc5_log" \
LOOP_CAMERA_LOG_MAX_BYTES=100 \
FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE="$tmp_dir/tc5.flock" \
	"$script" > "$tmp_dir/tc5.out" 2> "$tmp_dir/tc5.err" \
	|| status=$?
status=${status:-0}
if [ "$status" -ne 78 ]; then
	exec 9>&-
	cat "$tmp_dir/tc5.err" >&2
	fail "TC5: supervisor stub status 78 expected; got $status"
fi

printf 'POST-ROTATION-WRITE\n' >&9
exec 9>&-

expected_payload=$'POST-ROTATION-WRITE\n'
expected_size=${#expected_payload}
tc5_post_size=$(stat -c %s "$tc5_log")
if [ "$tc5_post_size" -ne "$expected_size" ]; then
	fail "TC5: post-rotation O_APPEND write didn't land at offset 0; size=$tc5_post_size expected=$expected_size"
fi
if ! grep -q "POST-ROTATION-WRITE" "$tc5_log"; then
	fail "TC5: post-rotation O_APPEND write content missing from live log"
fi
if grep -q "tc5-pre-rotation-DDDD" "$tc5_log"; then
	fail "TC5: pre-rotation content survived into live log post-truncate"
fi
unset status

# ============================================================================
# TC6: cp failure surfaced via stderr; live log NOT truncated; .1 untouched
# ============================================================================
# Per Task #56 (Minor F-task48-1 follow-up). See
# loop-run-ffstream_rotation_test.sh TC6 header for the rationale.
tc6_log="$tmp_dir/tc6-loop.log"
printf 'tc6-marker-EEEE\n%.0s' {1..64} > "$tc6_log"
# See loop-run-ffstream_rotation_test.sh TC6 for the chmod-000-dir
# rationale.
mkdir "$tc6_log.1"
chmod 000 "$tc6_log.1"
tc6_pre_size=$(stat -c %s "$tc6_log")
tc6_pre_md5=$(md5sum < "$tc6_log")

LOOP_CAMERA_LOG_FILE="$tc6_log" \
LOOP_CAMERA_LOG_MAX_BYTES=100 \
FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE="$tmp_dir/tc6.flock" \
	"$script" > "$tmp_dir/tc6.out" 2> "$tmp_dir/tc6.err" \
	|| status=$?
status=${status:-0}
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/tc6.err" >&2
	fail "TC6: supervisor stub status 78 expected; got $status"
fi
if ! grep -q "rotation: cp failed" "$tmp_dir/tc6.err"; then
	cat "$tmp_dir/tc6.err" >&2
	fail "TC6: supervisor must emit 'rotation: cp failed: ...' stderr on cp failure"
fi
tc6_post_size=$(stat -c %s "$tc6_log")
tc6_post_md5=$(md5sum < "$tc6_log")
if [ "$tc6_pre_size" -ne "$tc6_post_size" ]; then
	fail "TC6: live log size changed despite cp failure; pre=$tc6_pre_size post=$tc6_post_size"
fi
if [ "$tc6_pre_md5" != "$tc6_post_md5" ]; then
	fail "TC6: live log content changed despite cp failure; pre=$tc6_pre_md5 post=$tc6_post_md5"
fi
chmod 700 "$tc6_log.1"
if [ ! -d "$tc6_log.1" ]; then
	fail "TC6: pre-existing .1 directory was destroyed despite cp failure"
fi
unset status

echo "PASS: loop-run-ffstream-camera rotation tests (TC1+TC2+TC4+TC5+TC6)"
