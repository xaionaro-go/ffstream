# ffstream-specific instructions

## 1. Project goal

The project exists to provide portable and resilient live streaming tool. For example, one can run ffstream on an Android phone and it will:
* Consume whatever sensor is available (there should be multiple failover inputs).
* Compress it so that it will with the available network bandwidth.
* Will stream it to servers.

Currently the scheme we have is:
* inputs sources -> ffstream -> wireguard -> avd -> mpv

`avd` here stands for "Audio/Video daemon". The config we use is provided in `avd/examples/`.

`ffstream` and `avd` are tailored for each other to support dynamic change of resolutions, codecs and so on. There we use the audio track as the main one (if it is finished then the whole stream is considered finished).

## 2. How it works

A phone:
* Is rooted with Magisk or with a custom userdebug build.
* Has termux installed
* Has custom Ubuntu environment rolled out to `/data/ubuntu` (that is auto-executed by Magisk or by custom init)
* Any orchestration is generally handled by the Ubuntu environment, but `ffstream` is running from termux to get access to MediaCodec.
* Uses application WingOut that runs as a normal Android application, but it communicates with `ffstream` via gRPC.

## 3. Special paths

- Do not edit `**/imports/**`, `**/import/**` -- these directories are not the source of truth for the source code.
- Android SDK is in `ffstream/.Android`.
- `ffmpeg/myscripts` you may find how to update ffstream on a real phone.
- The base `Dockerfile` is available at `streamctl/docker/termux/Dockerfile`.

## 4. Test environment

- The ADB server with the real test phones is available at `172.17.0.1`. The IP address of the phone itself is 192.168.0.159.
- When running `ffstream` on the phone, don't forget `LD_LIBRARY_PATH=/data/data/com.termux/files/home/lib`.

## 5. Production environment

- There is a gRPC interface supported by `ffstream` (`172.29.170.2:3593`). If you need some specific debugging information that is not provided by the interface then add the required debugging capabilities into the gRPC interface (so that the next time a similar bug happens, it is easier to diagnose). One of the useful features that already exists is: `ffstreamctl --remote-addr tcp+ssl:<HOST>:3593 pipelines get` (to get the current avpipeline).
- You may also get the logs in `/tmp/mediamtx.log` (via SSH to `root@172.29.170.2`). If some logs are missing, add more logging to `ffstream` so that next time it will be easier to diagnose.
- When running `ffstream` on the phone, don't forget `LD_LIBRARY_PATH=/data/data/com.termux/files/home/lib`.
- Do not change anything on the production phone, do not restart anything. You may "only look, not touch".

There are two ways how `ffstream` is launched:
- Either via `mediamtx`.
- Or directly using the script below:
```sh
exec taskset -c 6-7 ffstream -v "$FFSTREAM_LOG_LEVEL" -retry_input_timeout_on_failure 1s -retry_output_timeout_on_failure 0 -auto_bitrate "$FFSTREAM_AUTO_BITRATE" -auto_bitrate_max_height "$FFSTREAM_AUTOBITRATE_MAX_HEIGHT" -auto_bitrate_min_height "$FFSTREAM_AUTOBITRATE_MIN_HEIGHT" -auto_bitrate_auto_bypass "$FFSTREAM_AUTO_BYPASS" -hwaccel mediacodec -mux_mode different_outputs_same_tracks_split_av -listen_control 127.0.0.1:3593 -listen_net_pprof 0.0.0.0:8238 -itsoffset 00:00:00.000 -fflags nobuffer -flags low_delay -rtbufsize 5M -probesize 32768 -analyzeduration 200000 -video_size "$WIDTH"x"$HEIGHT" -i rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3 -fallback_priority 1 -video_size "$BUILTIN_CAM_WIDTH"x"$BUILTIN_CAM_HEIGHT" -camera_index "$BUILTIN_CAM_INDEX" -framerate "$BUILTIN_CAM_FPS" -f android_camera -i '' -fallback_priority 1 -f pulse -i default -s "$WIDTH"x"$HEIGHT" -c:v "$VCODEC" -ar 48000 -ac 1 -sample_fmt fltp -c:a "$ACODEC" -b:v 4M -bufsize 4M -g "$[ $FRAMERATE * $KEYFRAME_INTERVAL ]" -r "$FRAMERATE" -f flv "$DST"'/pixel/dji-osmo-pocket-3-${v:0:codec}${a:0:codec}-${v:0:height}${a:0:rate}/'
```

## 6. Rules

- Do not edit/add/delete/rename/any-way-modify any files on a real phone, except files inside the termux home and files inside `ubuntu/tmp`
- Every time you finish a change, make a git commit with proper description. All commits should be in a separate branch `drafts`. If you made a change in avpipeline then push the change to the public repository (as `drafts`) and pull the commit in `ffstream`.
- a SEGFAULT is never fault of libav, it is always fault of our code and YOU MUST FIX IT.
- No log should happen for each frame, unless it has logging level TRACE.
