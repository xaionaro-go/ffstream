#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

lock_file=${FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE:-/tmp/ffstream-camera-supervisor.flock}
run_ffstream_camera=${FFSTREAM_CAMERA_RUNNER:-run-ffstream-camera.sh}
# 78 is emitted by run-ffstream-camera.sh for invalid configuration; it must
# stop here instead of looping on an operator-fixable setup error.
stop_statuses=${FFSTREAM_CAMERA_SUPERVISOR_STOP_STATUSES:-"74 78 126 127"}

if ! command -v flock >/dev/null 2>&1; then
	echo "ffstream-camera supervisor requires flock" >&2
	exit 69
fi

exec 9>>"$lock_file"
if ! flock -n 9; then
	echo "ffstream-camera supervisor already running; coalescing start request" >&2
	exit 0
fi
: > "$lock_file"
printf '%s\n' "$$" >&9

# Captured-stdout log file managed by rc.local (`>>` redirect) and rotated
# in-place by this supervisor. The file path is operator-owned at boot
# time; the supervisor only enforces the size cap. See
# loop-run-ffstream.sh for the rotation rationale (kernel O_APPEND + cp +
# truncate-in-place semantics + log-loss-window note); the same logic
# applies here.
loop_camera_log_file=${LOOP_CAMERA_LOG_FILE:-/data/ubuntu/tmp/loop-run-ffstream-camera.log}
loop_camera_log_max_bytes=${LOOP_CAMERA_LOG_MAX_BYTES:-209715200}

rotate_loop_camera_log_if_needed() {
	local path=$1
	local max=$2
	[ -f "$path" ] || return 0
	local size
	size=$(stat -c %s "$path" 2>/dev/null || echo 0)
	[ "$size" -gt "$max" ] || return 0
	# See loop-run-ffstream.sh for the cp-failure operator-feedback
	# rationale; same logic applies here.
	if ! cp -f "$path" "$path.1" 2>/dev/null; then
		echo "rotation: cp failed: $path -> $path.1 (continuing without truncate)" >&2
		return 1
	fi
	: > "$path"
}

# Exponential backoff for the ffstream-camera supervisor.
# - Tracks consecutive rapid-failure starts.
# - Backs off 0.1 -> 1 -> 5 -> 30 seconds.
# - Resets backoff after a successful long-running invocation (>= 60s).
# - A clean End exits the active ffstream instance, then relaunches an idle
#   daemon so the next Activate works without operator intervention.
# - Stops only for statuses that indicate unrecoverable configuration/setup
#   failure. Runtime exits are restartable.
delay=0.1
while true; do
	rotate_loop_camera_log_if_needed "$loop_camera_log_file" "$loop_camera_log_max_bytes"
	start=$(date +%s)
	"$run_ffstream_camera"
	status=$?
	case " $stop_statuses " in
		*" $status "*)
			echo "ffstream-camera supervisor stopping after unrecoverable status $status" >&2
			exit "$status"
			;;
	esac
	end=$(date +%s)
	dur=$((end - start))
	if [ "$status" -eq 0 ]; then
		# Intentional End: relaunch an idle daemon promptly.
		delay=0.1
	elif [ "$dur" -ge 60 ]; then
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
