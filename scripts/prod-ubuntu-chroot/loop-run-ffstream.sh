#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

run_ffstream=${FFSTREAM_RUNNER:-run-ffstream.sh}
stop_statuses=${FFSTREAM_SUPERVISOR_STOP_STATUSES:-"74 78 126 127"}

# Captured-stdout log file managed by rc.local (`>>` redirect) and rotated
# in-place by this supervisor. The file path is operator-owned at boot
# time; the supervisor only enforces the size cap.
loop_log_file=${LOOP_LOG_FILE:-/data/ubuntu/tmp/loop-run-ffstream.log}
# Default cap: 200 MiB. ffmpeg verbose log is roughly 10 MB/hour at info
# level → ~20 hours of forensic context per rotation cycle. Phone /data
# partition is multi-hundred GB; 200 MiB is < 0.1% of capacity. Operators
# can override via LOOP_LOG_MAX_BYTES.
loop_log_max_bytes=${LOOP_LOG_MAX_BYTES:-209715200}

# rotate_loop_log_if_needed: copy-truncate the captured-stdout log when it
# crosses the size cap. Uses cp+: > rather than mv because the parent
# rc.local invocation holds an O_APPEND file descriptor on the live log;
# `mv` would change the inode and route subsequent writes into the .1
# backup. Truncate-in-place preserves the inode + parent's FD; on the
# next O_APPEND write the kernel re-seeks to EOF (offset 0).
#
# Rotation log-loss window: between the cp and the truncate, supervisor
# stdout writes that arrive after cp completes but before the truncate
# may be lost (truncated away). Window is bounded by cp duration
# (~1-2s for a 200 MiB file on phone-class storage). Acceptable for
# restart-loop scenarios where stdout traffic is bursty + the next
# iteration restarts logging immediately.
rotate_loop_log_if_needed() {
	local path=$1
	local max=$2
	[ -f "$path" ] || return 0
	local size
	size=$(stat -c %s "$path" 2>/dev/null || echo 0)
	[ "$size" -gt "$max" ] || return 0
	# If cp fails (disk-full on .1 destination, permission denied, .1
	# is a non-empty directory, etc.), emit a diagnostic to stderr —
	# rc.local captures supervisor stderr into the live log itself,
	# so operators grep find rotation failures inline with surrounding
	# forensic context — and skip the truncate so live log content is
	# preserved for the next rotation attempt.
	if ! cp -f "$path" "$path.1" 2>/dev/null; then
		echo "rotation: cp failed: $path -> $path.1 (continuing without truncate)" >&2
		return 1
	fi
	: > "$path"
}

# Exponential backoff for ffstream supervisor.
# - Tracks consecutive rapid-failure starts.
# - Backs off 0.1 -> 1 -> 5 -> 30 seconds.
# - Resets backoff after a successful long-running invocation (>= 60s).
# - Stops for unrecoverable setup/configuration failures.
delay=0.1
while true; do
	rotate_loop_log_if_needed "$loop_log_file" "$loop_log_max_bytes"
	start=$(date +%s)
	"$run_ffstream"
	status=$?
	case " $stop_statuses " in
		*" $status "*)
			echo "ffstream supervisor stopping after unrecoverable status $status" >&2
			exit "$status"
			;;
	esac
	end=$(date +%s)
	dur=$((end - start))
	if [ "$dur" -ge 60 ]; then
		# Successful long run; reset backoff.
		delay=0.1
	else
		# Rapid fail; escalate backoff.
		case "$delay" in
			0.1) delay=1 ;;
			1)   delay=5 ;;
			5)   delay=30 ;;
			*)   delay=30 ;;  # cap
		esac
	fi
	sleep "$delay"
done
