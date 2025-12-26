# ffstream-specific instructions

## 1. Project goal

The project exists to provide portable and resilient live streaming tool. For example, one can run ffstream on an Android phone and it will:
* Consume whatever sensor is available (there should be multiple failover inputs).
* Compress it so that it will with the available network bandwidth.
* Will stream it to servers.

Currently the scheme we have is:
* inputs sources -> ffstream -> wireguard -> avd -> mpv

`avd` here stands for "Audio/Video daemon".

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

## 4. Environment

- The ADB server with the real phones is available at `172.17.0.1`.

## 5. Rules

- Do not edit/add/delete/rename/any-way-modify any files on a real phone, except files inside the termux home and files inside `ubuntu/tmp`
- Every time you finish a change, make a git commit with proper description. If you made a change in avpipeline, then use script `ffstream/myscripts/push-avpipeline-and-test.sh`.
