#!/bin/bash
#
# run-ffstream-camera.sh (#350): supervises an IDLE-mode ffstream daemon
# alongside the legacy run-ffstream.sh path. Keeps every global flag from
# the legacy launcher (low-latency input defaults, hwaccel, auto-bitrate,
# retry behaviour, scheduling/affinity), and drops only inputs / outputs
# (those are added at runtime via wingout's gRPC):
#   AddInput(camera) + AddInput(mic) + SetOutputURL + SwitchOutputByProps.
#
# SwitchOutputByProps also carries the per-Activate width/height/bitrate,
# so the daemon does NOT bake `-auto_bitrate_resolution` into argv —
# the resolution lives in user-tap-time settings, not in /etc/streaming.env
# (which keeps the legacy DJI 1920x1080 anchor).
#
# Split with the legacy script — both daemons co-exist on one phone:
#   - Listens on tcp:127.0.0.1:3594 (legacy: 3593)
#   - pprof on 0.0.0.0:8239         (legacy: 8238)
#   - Logs to /data/ubuntu/tmp/ffstream-camera.log
#
# -mux_mode different_outputs_same_tracks_split_av (#350 task #12) makes
# the daemon emit two separate publishes per Activate — one video-only
# RTMP connection, one audio-only — so each lands on a distinct avd
# regex/static endpoint (pixel/builtincamera-${v:0:codec}-${v:0:height}
# for video, pixel/builtincamera-${a:0:codec}-${a:0:rate} for audio).
#
# Mission: see /home/streaming/go/src/github.com/xaionaro-go/mission.md

export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin

. /etc/streaming.env

# Mic-fix daemon (PulseAudio mic plumbing). Backgrounded — same as legacy.
fix-mic &

export PULSE_SERVER=127.0.0.1
export GOMEMLIMIT="${FFSTREAM_GOMEMLIMIT:-15GiB}"

# Tell the OOM killer to prefer this process if memory pressure hits;
# wingout / Android system processes must outlive us.
echo 1000 > /proc/self/oom_score_adj

# All flags below are GLOBAL defaults (apply to every later AddInput RPC's
# AVFormatContext) — preserved verbatim from run-ffstream.sh except for
# the per-input/per-output flags that wingout drives via gRPC at
# user-tap-Activate time. Dropped relative to the legacy launcher:
#   -i <url>                    (replaced by AddInput RPC)
#   -fallback_priority <n>      (per-input; AddInput carries Priority)
#   -itsoffset <ts>             (per-input; AddInput as needed)
#   -video_size <WxH>           (per-input; AddInput's CustomOptions)
#   -auto_bitrate_resolution    (per-Activate; SwitchOutputByProps width/height)
#   -s/-c:v/-c:a/-b:v/-bufsize/-g/-r/-ar/-ac/-sample_fmt (per-output)
#   -f <fmt> <DST_URL>          (per-output; SetOutputURL + SwitchOutputByProps)
#
# hardened_malloc (preloaded inside termux via /usr/local/bin/ffstream
# wrapper) reserves ~1TB of virtual address space at startup. Inherited
# RLIMIT_AS is too small without explicit lifting — without prlimit, the
# binary aborts with "fatal allocator error: failed to reserve allocator
# state" at process entry. Mirrors the legacy run-ffstream.sh.
exec prlimit --as="${FFSTREAM_RAM_CAP_AS:-unlimited}" -- \
	nice -n -15 \
	taskset -c 6-8 \
	ffstream \
		-v "$FFSTREAM_LOG_LEVEL" \
		-retry_input_timeout_on_failure 1s \
		-retry_output_timeout_on_failure 0 \
		-quiet_on_open_failure true \
		-hwaccel mediacodec \
		-mux_mode different_outputs_same_tracks_split_av \
		-listen_control tcp:127.0.0.1:3594 \
		-listen_net_pprof 0.0.0.0:8239 \
		-fflags nobuffer \
		-flags low_delay \
		-rtbufsize 5M \
		-probesize 32768 \
		-analyzeduration 200000 \
		2>&1 | tee /data/ubuntu/tmp/ffstream-camera.log
