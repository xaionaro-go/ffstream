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

- Do not edit or read `**/imports/**`, `**/import/**` -- these directories are not the source of truth for the source code.
- Android SDK is in `ffstream/.Android`.
- `ffmpeg/myscripts` you may find how to update ffstream on a real phone.
- The base `Dockerfile` is available at `streamctl/docker/termux/Dockerfile`.

## 4. Test environment

- The ADB server with the real test phones is available at `172.17.0.1` (see `adb -L tcp:172.17.0.1:5037 devices`). The IP address of the phone itself is `192.168.0.159`. There is direct access from dev environment to the phone, but not the other way.
- Destination `192.168.0.131:9713` (from the phone) is forwarded to Agent's environment/container. Use this port to listen (with `avd`) for RTMP streams from `ffstream` running on the phone. Do not do manual port forwarding, forwarding is handled by nftables on the host system (you don't have access to).
- When running `ffstream` on the phone, don't forget `LD_LIBRARY_PATH=/data/data/com.termux/files/home/lib`.
- To connect to the phone via ffstreamctl use `ffstreamctl --remote-addr tcp+ssl:192.168.0.159:3593 pipelines get`, but `ffstream` on the phone should also have `-listen_control 0.0.0.0:3593`.
- Use `DEBUG` logging in `ffstream`. Enable `TRACE` only when needed, as it can severely degrade performance and disrupt packet/frame processing.

## 5. Production environment

- There is a gRPC interface supported by `ffstream` (`172.29.170.2:3593`). If you need some specific debugging information that is not provided by the interface then add the required debugging capabilities into the gRPC interface (so that the next time a similar bug happens, it is easier to diagnose). One of the useful features that already exists is: `ffstreamctl --remote-addr tcp+ssl:172.29.170.2:3593 pipelines get` (to get the current avpipeline).
- Destination `192.168.0.131:9713` (from the phone) is forwarded to Agent's environment/container. Use this port to listen (with `avd`) for RTMP streams from `ffstream` running on the phone. Do not do manual port forwarding, forwarding is handled by nftables on the host system (you don't have access to).
- You may also get the logs in `/tmp/mediamtx.log` (via SSH to `root@172.29.170.2`). If some logs are missing, add more logging to `ffstream` so that next time it will be easier to diagnose. If you need to access normal Android file tree, it is in `/android/`.
- When running `ffstream` on the phone, don't forget `LD_LIBRARY_PATH=/data/data/com.termux/files/home/lib`.
- Do not change anything on the production phone, do not restart anything. You may "only look, not touch".

There are two ways how `ffstream` is launched:
- Either via `mediamtx`.
- Or directly using the script `/usr/local/bin/run-ffstream.sh` (on the phone).

If you see evidences of `ffstream` running via `/tmp/mediamtx.log` (on the phone), then the first way is used (not the script)

## 6. Rules

- Do not edit/add/delete/rename/any-way-modify any files on a real phone, except files inside the termux home and files inside `ubuntu/tmp`
- Every time you finish a change, make a git commit with proper description. All commits should be in a separate branch `drafts`. If you made a change in avpipeline then push the change to the public repository (as `drafts`) and pull the commit in `ffstream`.
- a SEGFAULT is never fault of libav, it is always fault of our code and YOU MUST FIX IT.
- No log should happen for each frame, unless it has logging level TRACE.
