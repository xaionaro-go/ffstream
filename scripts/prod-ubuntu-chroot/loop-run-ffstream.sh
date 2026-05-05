#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

run_ffstream=${FFSTREAM_RUNNER:-run-ffstream.sh}
stop_statuses=${FFSTREAM_SUPERVISOR_STOP_STATUSES:-"74 78 126 127"}

# Exponential backoff for ffstream supervisor.
# - Tracks consecutive rapid-failure starts.
# - Backs off 0.1 -> 1 -> 5 -> 30 seconds.
# - Resets backoff after a successful long-running invocation (>= 60s).
# - Stops for unrecoverable setup/configuration failures.
delay=0.1
while true; do
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
