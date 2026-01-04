
ENABLE_VLC?=false
ENABLE_LIBSRT?=false
ENABLE_DEBUG_TRACE?=false
ANDROID_NDK_VERSION?=r28-beta2

GOTAGS:=$(GOTAGS),with_libav,ffmpeg7
ifeq ($(ENABLE_LIBSRT), true)
	GOTAGS:=$(GOTAGS),with_libsrt
endif
ifeq ($(ENABLE_VLC), true)
	GOTAGS:=$(GOTAGS),with_libvlc
endif
ifeq ($(ENABLE_DEBUG_TRACE), true)
	GOTAGS:=$(GOTAGS),debug_trace
endif

GOTAGS:=$(GOTAGS:,%=%)
GOPATH?=$(shell go env GOPATH)

GOBUILD_FLAGS?=-buildvcs=true
ifneq ($(GOTAGS),)
	GOBUILD_FLAGS+=-tags=$(GOTAGS)
endif

all: bin/ffstream-linux-amd64 bin/ffstream-linux-arm64 bin/ffstreamctl-linux-amd64 bin/ffstreamctl-linux-arm64

build:
	mkdir -p build

bin/ffstream-linux-amd64: build
	CGO_ENABLED=1 ASAN_OPTIONS=protect_shadow_gap=0 GOOS=linux GOARCH=amd64 go build $(GOBUILD_FLAGS) -o bin/ffstream-linux-amd64 ./cmd/ffstream

bin/ffstream-linux-arm64: build
	CGO_ENABLED=1 ASAN_OPTIONS=protect_shadow_gap=0 GOOS=linux GOARCH=arm64 go build $(GOBUILD_FLAGS) -o bin/ffstream-linux-arm64 ./cmd/ffstream

bin/ffstreamctl-linux-amd64: build
	CGO_ENABLED=false GOOS=linux GOARCH=amd64 go build -o bin/ffstreamctl-linux-amd64 ./cmd/ffstreamctl

bin/ffstreamctl-linux-arm64: build
	CGO_ENABLED=false GOOS=linux GOARCH=arm64 go build -o bin/ffstreamctl-linux-arm64 ./cmd/ffstreamctl

bin/ffstreamctl-android-arm64: build
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o bin/ffstreamctl-android-arm64 ./cmd/ffstreamctl

# pkg-config-wrapper for local builds
$(GOPATH)/bin/pkg-config-wrapper:
	go install github.com/xaionaro-go/pkg-config-wrapper@5dd443e6c18336416c49047e2ba0002e26a85278

# Download Android NDK for local cross-compilation
3rdparty/arm64/android-ndk-$(ANDROID_NDK_VERSION):
	mkdir -p 3rdparty/arm64
	cd 3rdparty/arm64 && wget https://dl.google.com/android/repository/android-ndk-$(ANDROID_NDK_VERSION)-linux.zip && unzip android-ndk-$(ANDROID_NDK_VERSION)-linux.zip && rm -f android-ndk-$(ANDROID_NDK_VERSION)-linux.zip

# Check for ffmpeg7 termux libraries (built via build/build-ffmpeg-for-android.sh)
3rdparty/arm64/termux:
	@if [ ! -f 3rdparty/arm64/termux/data/data/com.termux/files/usr/lib/libavcodec.a ]; then \
		echo "ERROR: ffmpeg7 libraries not found. Please run: ./build/build-ffmpeg-for-android.sh"; \
		exit 1; \
	fi

# Build ffstream for Android ARM64 without Docker (uses ffmpeg7 libraries built via build/build-ffmpeg-for-android.sh)
# Key: Use -linkmode=external and -Wl,-Bdynamic to ensure dynamic linking of libc.so
# This prevents static linking of bionic's getauxval which crashes on Android
ffstream-android-arm64-static-cgo: build $(GOPATH)/bin/pkg-config-wrapper 3rdparty/arm64/android-ndk-$(ANDROID_NDK_VERSION) 3rdparty/arm64/termux
	$(eval ANDROID_NDK_HOME=$(PWD)/3rdparty/arm64/android-ndk-$(ANDROID_NDK_VERSION))
	PKG_CONFIG_WRAPPER_LOG='/tmp/pkg_config_wrapper.log' \
	PKG_CONFIG_WRAPPER_LOG_LEVEL='trace' \
	PKG_CONFIG_LIBS_FORCE_STATIC='libav*,libsrt' \
	PKG_CONFIG_ERASE="-fopenmp=*,-landroid,-lcamera2ndk,-lmediandk" \
	PKG_CONFIG='$(GOPATH)/bin/pkg-config-wrapper' \
	PKG_CONFIG_PATH='$(PWD)/3rdparty/arm64/termux/data/data/com.termux/files/usr/lib/pkgconfig' \
	CGO_CFLAGS='-std=gnu99 -I$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/include/ -I$(PWD)/3rdparty/arm64/termux/data/data/com.termux/files/usr/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow' \
	CGO_LDFLAGS='-v -Wl,-Bstatic -lcrypto -lv4lconvert -ljpeg -Wl,-Bdynamic -ldl -lc -landroid -landroid-glob -landroid-posix-semaphore -lcamera2ndk -lmediandk -lpulse -lc++_shared -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/aarch64-linux-android/35/ -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/ -L$(PWD)/3rdparty/arm64/termux/data/data/com.termux/files/usr/lib' \
	ANDROID_NDK_HOME="$(ANDROID_NDK_HOME)" \
	CC="$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android35-clang" \
	CXX="$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android35-clang++" \
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
	go build $(GOBUILD_FLAGS),mediacodec,patched_libav -ldflags='-linkmode=external' -o bin/ffstream-android-arm64 ./cmd/ffstream
	ls -ldh bin/ffstream-android-arm64

DOCKER_IMAGE?=xaionaro2/streampanel-android-builder
DOCKER_CONTAINER_NAME?=ffstream-android-builder

PASSTHROUGH_AVPIPELINE?=false

dockerbuilder-android-arm64:
	docker pull  $(DOCKER_IMAGE)
	if ! docker start $(DOCKER_CONTAINER_NAME) >/dev/null 2>&1; then \
		docker run \
			--detach \
			--init \
			--name $(DOCKER_CONTAINER_NAME) \
			--volume "$(PWD)/.cache:/home/builder/.cache" \
			--tty \
			--volume ".:/project" \
			$(DOCKER_IMAGE) >/dev/null 2>&1 || /bin/true; \
	fi
	docker exec -ti $(DOCKER_CONTAINER_NAME) sudo rm -rf /home/builder/avpipeline
	if $(PASSTHROUGH_AVPIPELINE); then \
		docker cp ../avpipeline/ $(DOCKER_CONTAINER_NAME):/home/builder/avpipeline/ && \
		docker exec -ti $(DOCKER_CONTAINER_NAME) sudo chown -R builder:builder /home/builder/avpipeline; \
	fi

bin/ffstream-android-arm64: dockerbuilder-android-arm64
	@if git diff-files --quiet; then \
		git branch -D v0-termux; \
		git branch v0-termux; \
		rm -f /home/builder/termux-packages/output/ffstream_0-termux-1_aarch64.deb; \
		docker exec $(DOCKER_CONTAINER_NAME) make ENABLE_VLC="$(ENABLE_VLC)" ENABLE_DEBUG_TRACE="$(ENABLE_DEBUG_TRACE)" -C /project ffstream-android-arm64-in-docker; \
		docker cp $(DOCKER_CONTAINER_NAME):/home/builder/termux-packages/output/ffstream_0-termux-1_aarch64.deb bin/ffstream-android-termux-arm64.deb; \
	else \
		echo "ERROR: there are uncommitted changes, please either stash them or commit" >&2; \
	fi

bin/ffstream-android-termux.deb: bin/ffstream-android-arm64

/home/builder/go/bin/pkg-config-wrapper:
	go install github.com/xaionaro-go/pkg-config-wrapper@5dd443e6c18336416c49047e2ba0002e26a85278

ffstream-android-arm64-in-docker: /home/builder/go/bin/pkg-config-wrapper
	git config --global --add safe.directory /project/.git
	chmod -R +w /home/builder/.termux-build/ffstream 2>/dev/null || /bin/true
	rm -rf /home/builder/.termux-build/ffstream /data/data/.built-packages/ffstream
	cd /home/builder/termux-packages && \
		if ! [ -f ./packages/ffmpeg/rebuilt-successful ]; then \
			sed -e 's/--disable-libsrt/--enable-libsrt/g' -i ./packages/ffmpeg/build.sh && \
			bash -x ./build-package.sh -I -f ffmpeg && \
			touch ./packages/ffmpeg/rebuilt-successful; \
		fi && \
		mkdir -p packages/ffstream && \
		cp /project/build/termux/build.sh ./packages/ffstream/build.sh && \
		bash -x ./build-package.sh ffstream

ffstream-android-arm64-in-termux: build
	git log -1
	go mod tidy
	chmod -R +w /home/builder/.termux-build/ffstream /home/builder/.termux-build/_cache/go* ~/go/pkg/mod/golang.org
	for F in ~/.termux-build/ffstream/build/pkg/mod/golang.org/toolchain@v0.0.1-go*.linux-amd64/src/runtime/cgo/cgo.go ~/.termux-build/_cache/go*/src/runtime/cgo/cgo.go ~/go/pkg/mod/golang.org/toolchain@v0.0.1-*.linux-amd64/src/runtime/cgo/cgo.go; do \
		sed -e 's/Werror/Wno-error/g' -i $$F || /bin/true; \
	done
	git config --global --add safe.directory /project
	$(eval ANDROID_NDK_HOME=$(shell ls -d /home/builder/lib/android-ndk-* | tail -1))
	# TODO: make static: libv4lconvert, libcrypto, libc++_shared, libandroid-glob, libandroid-posix-semaphore
	PKG_CONFIG_WRAPPER_LOG='/tmp/pkg_config_wrapper.log' \
	PKG_CONFIG_WRAPPER_LOG_LEVEL='trace' \
	PKG_CONFIG_LIBS_FORCE_STATIC='libav*,libvlc,libsrt,libv4l*' \
	PKG_CONFIG_ERASE="-fopenmp=*,-landroid,-lcamera2ndk,-lmediandk" \
	PKG_CONFIG='/home/builder/go/bin/pkg-config-wrapper' \
	PKG_CONFIG_PATH='/data/data/com.termux/files/usr/lib/pkgconfig' \
	CGO_CFLAGS='-std=gnu99 -I$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/include/ -I/data/data/com.termux/files/usr/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow' \
	CGO_LDFLAGS='-v -Wl,-Bstatic -lcrypto -lv4lconvert -ljpeg -Wl,-Bdynamic -ldl -lc -landroid -landroid-glob -landroid-posix-semaphore -lcamera2ndk -lmediandk -lpulse -lc++_shared -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/ -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/aarch64-linux-android/35/ -L/data/data/com.termux/files/usr/lib' \
	ANDROID_NDK_HOME="$(ANDROID_NDK_HOME)" \
	PATH="${PATH}:${HOME}/go/bin" \
	GOFLAGS="$(GOBUILD_FLAGS),mediacodec,patched_libav -ldflags=$(shell echo ${LINKER_FLAGS_ANDROID} | tr " " ",")" \
	go build -x -o bin/ffstream-android-arm64 ./cmd/ffstream
	ls -ldh bin/ffstream-android-arm64
