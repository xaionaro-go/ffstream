#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

# Exponential backoff for ffstream-camera supervisor (#350).
# - Tracks consecutive rapid-failure starts.
# - Backs off 0.1 -> 1 -> 5 -> 30 seconds.
# - Resets backoff after a successful long-running invocation (>= 60s).
delay=0.1
while true; do
	start=$(date +%s)
	run-ffstream-camera.sh
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
