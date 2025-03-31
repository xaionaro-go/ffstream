TERMUX_PKG_HOMEPAGE=https://github.com/xaionaro-go/ffstream
TERMUX_PKG_DESCRIPTION="A kick-in replacement for ffmpeg, but for live streaming"
TERMUX_PKG_LICENSE="CC0-1.0"
TERMUX_PKG_MAINTAINER="@xaionaro"
TERMUX_PKG_VERSION=-termux
TERMUX_PKG_REVISION=1
TERMUX_PKG_SRCURL=git+file:///project/
TERMUX_PKG_SHA256=SKIP_CHECKSUM

termux_step_make() {
	termux_setup_golang
	export GOPATH=$TERMUX_PKG_BUILDDIR

	mkdir -p "$GOPATH"/src/github.com/xaionaro-go
	cp -a "$TERMUX_PKG_SRCDIR" "$GOPATH"/src/github.com/xaionaro-go/ffstream
	cd "$GOPATH"/src/github.com/xaionaro-go/ffstream

	make ffstream-android-arm64-in-termux ENABLE_LIBAV=true
}

termux_step_make_install() {
	install -Dm700 \
		"$GOPATH"/src/github.com/xaionaro-go/ffstream/bin/ffstream-android-arm64 \
		"$TERMUX_PREFIX"/bin/ffstream
}
