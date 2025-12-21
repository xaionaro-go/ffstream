# `ffstream`

A drop-in replacement for `ffmpeg` that is tailored to live-streaming: it allows for dynamically changing encoding settings (e.g. bitrate).

The main use case this was implemented for is [IRL streaming](https://kick.com/category/irl). For example:
* When the internet connection is of variable bandwidth: one may dynamically adjust the bitrate according to available channel.
* When the transcoding equipment is overheating, it is possible to dynamically enable a pass-through (and disable transcoding until the equipment would cool down).

But actually, if you got into using `ffstream` instead of `ffmpeg` you may want to consider writing yourown tool that is tailored for your needs by just using [`avpipeline`](https://github.com/xaionaro-go/avpipeline) directly.

# How to use

This is supposed to be a kick-in replacement. So for example if you have:
```sh
ffmpeg -i rtmp://127.0.0.1:1937/test/stream0 -c:v libx264 -f flv rtmp://127.0.0.1:1937/test/stream1
```
you should be able to just replace `ffmpeg` with `ffstream`:
```sh
ffstream -i rtmp://127.0.0.1:1937/test/stream0 -c:v libx264 -f flv rtmp://127.0.0.1:1937/test/stream1
```

However, it won't work with various fancy functionality like filters, yet (to be implemented).

But since you switched to `ffstream`, now you can add flag `-listen_control` which would open a socket and listen for incoming requests live, e.g.:
```sh
ffstream -listen_control unix:/tmp/ffstream.sock -i rtmp://127.0.0.1:1937/test/stream0 -c:v libx264 -f flv rtmp://127.0.0.1:1937/test/stream1
```

After that you may use `ffstreamctl` to manage the actively running `ffstream`, for example:
```sh
<TBD>
```

# Android

On Android it works based on [Termux](https://en.wikipedia.org/wiki/Termux). If you already have Termux on your phone, then you can just build on your computer the tool:
```sh
make bin/ffstream-android-arm64.deb
```
(it will use Docker for that)

then install the `deb` file `bin/ffstream-android-termux-arm64.deb` in your Termux environment.

If you also need `ffstreamctl` then just build the static binary for normal Linux:
```sh
make bin/ffstreamctl-linux-arm64
```
and copy to `/data/data/com.termux/files/usr/bin/ffstreamctl` (it will be a static binary, so it works in any Linux environment)

But on the bright side when you succeed you get access to [MediaCodec](https://developer.android.com/reference/android/media/MediaCodec) (hardware encoder) as well:
```sh
~ $ ffstream -encoders | grep mediacodec
00000000000000E2 av1_mediacodec
000000000000001B h264_mediacodec
00000000000000AD hevc_mediacodec
000000000000000C mpeg4_mediacodec
000000000000008B vp8_mediacodec
00000000000000A7 vp9_mediacodec
```

# Debugging
For debugging, you may:

1. Add verbosity (e.g. with `-v trace`), e.g.:
```sh
ffstream -v trace -i rtmp://127.0.0.1:1937/test/stream0 -c:v libx264 -f flv rtmp://127.0.0.1:1937/test/stream1
```

2. Add the flag `-listen_net_pprof`, e.g.:
```sh
ffstream -v trace -listen_net_pprof 0.0.0.0:12345 -i rtmp://127.0.0.1:1937/test/stream0 -c:v libx264 -f flv rtmp://127.0.0.1:1937/test/stream1
```

This allows to debug the app as any other Go app, e.g.:
```sh
curl http://127.0.0.1:12345/debug/pprof/goroutine?debug=1 | less
```

3. Collect all logs to a centralized logging server (for investigating rarely occurring problems), e.g.:
```sh
ffstream -v trace -logstash_addr udp://my.logstash.server:9600 -sentry_dsn https://my.sentry.server/URI -i rtmp://127.0.0.1:1937/test/stream0 -c:v libx264 -f flv rtmp://127.0.0.1:1937/test/stream1
```

