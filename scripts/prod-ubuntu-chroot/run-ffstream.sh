#!/bin/bash
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

. /etc/streaming.env

CONFIG="$(cat '/android/data/user/0/center.dx.wingout/cache/streaming_settings.json')"
BUILTIN_CAM_WIDTH="$(echo "$CONFIG" | jq '.width')"
BUILTIN_CAM_WIDTH="${BUILTIN_CAM_WIDTH:-$WIDTH}"
BUILTIN_CAM_HEIGHT="$(echo "$CONFIG" | jq '.height')"
BUILTIN_CAM_HEIGHT="${BUILTIN_CAM_HEIGHT:-$HEIGHT}"
BUILTIN_CAM_FPS="$(echo "$CONFIG" | jq '.fps')"
BUILTIN_CAM_FPS="${BUILTIN_CAM_FPS:-30}"
BUILTIN_CAM_WHICH="$(echo "$CONFIG" | jq '.preferredCamera')"

case "$BUILTIN_CAM_WHICH" in
	[Ff]ront)
		BUILTIN_CAM_INDEX=1
		;;
	[Bb]ack)
		BUILTIN_CAM_INDEX=0
		;;
	*)
		BUILTIN_CAM_INDEX=1
		;;
esac

fix-mic &

export PULSE_SERVER=127.0.0.1

DST=rtmp://"$ADDR_HOME":1946

export GOMEMLIMIT="${FFSTREAM_GOMEMLIMIT:-15GiB}"
echo 1000 > /proc/self/oom_score_adj
# rtmp input is registered as a fallback (priority 1, lower priority than 0).
# Wingout's gRPC AddInput call registers camera+mic at priority 0 (primary).
# InputWithFallback selects the active source by ascending priority number
# (0 = best/highest, see avpipeline/preset/inputwithfallback/input_with_fallback.go
# onInputChainKernelOpen + ffstream/pkg/ffstream/resource.go ByFallbackPriority).
# When camera is active, it preempts; when camera is gone, rtmp takes over.
exec prlimit --as="${FFSTREAM_RAM_CAP_AS:-21474836480}" -- nice -n -15 taskset -c 6-8 ffstream -v "$FFSTREAM_LOG_LEVEL" -retry_input_timeout_on_failure 1s -retry_output_timeout_on_failure 0 -auto_bitrate "$FFSTREAM_AUTO_BITRATE" -auto_bitrate_max_height "$FFSTREAM_AUTOBITRATE_MAX_HEIGHT" -auto_bitrate_min_height "$FFSTREAM_AUTOBITRATE_MIN_HEIGHT" -auto_bitrate_auto_bypass "$FFSTREAM_AUTO_BYPASS" -hwaccel mediacodec -mux_mode different_outputs_same_tracks_split_av -listen_control tcp:127.0.0.1:3593 -listen_net_pprof 0.0.0.0:8238 -itsoffset 00:00:00.000 -fflags nobuffer -flags low_delay -rtbufsize 5M -probesize 32768 -analyzeduration 200000 -video_size "$WIDTH"x"$HEIGHT" -fallback_priority 1 -i rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3 -s "$WIDTH"x"$HEIGHT" -c:v "$VCODEC" -ar 48000 -ac 1 -sample_fmt fltp -c:a "$ACODEC" -b:v 4M -bufsize 4M -g "$[ $FRAMERATE * $KEYFRAME_INTERVAL ]" -r "$FRAMERATE" -f flv "$DST"'/pixel/dji-osmo-pocket-3-${v:0:codec}${a:0:codec}-${v:0:height}${a:0:rate}/' 2>&1 | tee /data/ubuntu/tmp/ffstream.log

