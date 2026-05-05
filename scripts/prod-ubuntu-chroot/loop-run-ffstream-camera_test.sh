#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/loop-run-ffstream-camera.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

wait_for_log_lines() {
	log_file=$1
	expected=$2
	deadline=$((SECONDS + 5))
	while [ "$SECONDS" -lt "$deadline" ]; do
		if [ -f "$log_file" ]; then
			line_count=$(wc -l < "$log_file" | tr -d ' ')
			if [ "$line_count" -ge "$expected" ]; then
				return 0
			fi
		fi
		sleep 0.05
	done
	return 1
}

tmp_dir=$(mktemp -d)
cleanup() {
	rm -rf "$tmp_dir"
}
trap cleanup EXIT

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

cat > "$bin_dir/run-ffstream-camera.sh" <<'STUB'
#!/bin/bash
printf '%s\n' "$$" >> "$RUN_FFSTREAM_CAMERA_LOG"
sleep "${RUN_FFSTREAM_CAMERA_SLEEP:-0.2}"
status_file=${RUN_FFSTREAM_CAMERA_STATUS_FILE:-}
if [ -n "$status_file" ] && [ -f "$status_file" ]; then
	status=$(head -n 1 "$status_file")
	sed -i 1d "$status_file"
	exit "$status"
fi
exit "${RUN_FFSTREAM_CAMERA_STATUS:-0}"
STUB
chmod +x "$bin_dir/run-ffstream-camera.sh"

run_log="$tmp_dir/run.log"
export PATH="$bin_dir:$PATH"
export RUN_FFSTREAM_CAMERA_LOG="$run_log"
export RUN_FFSTREAM_CAMERA_SLEEP=0.4
printf '%s\n' 0 78 > "$tmp_dir/statuses"
export RUN_FFSTREAM_CAMERA_STATUS_FILE="$tmp_dir/statuses"
export FFSTREAM_CAMERA_RUNNER="$bin_dir/run-ffstream-camera.sh"
export FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE="$tmp_dir/supervisor.flock"
export FFSTREAM_CAMERA_SUPERVISOR_LOCK_WAIT_SECONDS=5
export FFSTREAM_CAMERA_SUPERVISOR_STOP_STATUSES="74 78"

"$script" > "$tmp_dir/first.out" 2> "$tmp_dir/first.err" &
first_pid=$!
wait_for_log_lines "$run_log" 1 || fail "first supervisor did not start"

set +e
"$script" > "$tmp_dir/second.out" 2> "$tmp_dir/second.err"
second_status=$?
set -e
if [ "$second_status" -ne 0 ]; then
	echo "second stderr:" >&2
	cat "$tmp_dir/second.err" >&2
	fail "duplicate start must coalesce successfully, got status $second_status"
fi

wait_for_log_lines "$run_log" 2 || fail "clean End must relaunch the idle ffstream-camera daemon"
set +e
wait "$first_pid"
first_status=$?
set -e
if [ "$first_status" -ne 78 ]; then
	echo "first stderr:" >&2
	cat "$tmp_dir/first.err" >&2
	fail "unrecoverable/configuration failure must stop supervisor with original status; got $first_status"
fi

line_count=$(wc -l < "$run_log" | tr -d ' ')
if [ "$line_count" != "2" ]; then
	echo "first stderr:" >&2
	cat "$tmp_dir/first.err" >&2
	echo "second stderr:" >&2
	cat "$tmp_dir/second.err" >&2
	fail "duplicate start must not queue another supervisor, and clean End must relaunch exactly once before stop; got $line_count invocations"
fi
