#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

lock_file=${FFSTREAM_CAMERA_SUPERVISOR_LOCK_FILE:-/tmp/ffstream-camera-supervisor.flock}
run_ffstream_camera=${FFSTREAM_CAMERA_RUNNER:-run-ffstream-camera.sh}

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

# Exponential backoff for the ffstream-camera supervisor.
# - Tracks consecutive rapid-failure starts.
# - Backs off 0.1 -> 1 -> 5 -> 30 seconds.
# - Resets backoff after a successful long-running invocation (>= 60s).
# - Stops after a clean ffstream-camera exit; UI Deactivate owns clean daemon
#   shutdown through the End RPC.
delay=0.1
while true; do
	start=$(date +%s)
	"$run_ffstream_camera"
	status=$?
	if [ "$status" -eq 0 ]; then
		exit 0
	fi
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
