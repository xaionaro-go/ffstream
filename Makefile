
ENABLE_VLC?=false
ENABLE_LIBSRT?=false
ENABLE_DEBUG_TRACE?=false
ANDROID_NDK_VERSION?=r28-beta2

# Use ccache for CGO compilations when available
CCACHE:=$(shell which ccache 2>/dev/null)
ifneq ($(CCACHE),)
  CC_PREFIX:=ccache
endif

# Android NDK paths
ANDROID_NDK_HOME?=$(CURDIR)/3rdparty/arm64/android-ndk-$(ANDROID_NDK_VERSION)
NDK_SYSROOT:=$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot

# On ARM64 hosts, use native system clang to avoid x86_64 emulation overhead (~12x faster)
ifeq ($(shell uname -m),aarch64)
  ANDROID_ARM64_CC?=/usr/bin/clang --target=aarch64-linux-android35 --sysroot=$(NDK_SYSROOT)
  ANDROID_ARM64_CXX?=/usr/bin/clang++ --target=aarch64-linux-android35 --sysroot=$(NDK_SYSROOT)
  ANDROID_X86_64_CC?=/usr/bin/clang --target=x86_64-linux-android35 --sysroot=$(NDK_SYSROOT)
  ANDROID_X86_64_CXX?=/usr/bin/clang++ --target=x86_64-linux-android35 --sysroot=$(NDK_SYSROOT)
else
  ANDROID_ARM64_CC?=$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android35-clang
  ANDROID_ARM64_CXX?=$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android35-clang++
  ANDROID_X86_64_CC?=$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android35-clang
  ANDROID_X86_64_CXX?=$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android35-clang++
endif

GOTAGS:=$(GOTAGS),with_libav
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

# Build ffmpeg and dependencies via Docker (if not already built)
3rdparty/arm64/sysroot:
	@if [ ! -f 3rdparty/arm64/sysroot/lib/libavcodec.a ]; then \
		echo "Building ffmpeg and dependencies for arm64 via Docker..."; \
		./scripts/docker-build.sh --arch=arm64; \
	fi

3rdparty/x86_64/sysroot:
	@if [ ! -f 3rdparty/x86_64/sysroot/lib/libavcodec.a ]; then \
		echo "Building ffmpeg and dependencies for x86_64 via Docker..."; \
		./scripts/docker-build.sh --arch=x86_64; \
	fi

# Build ffstream for Android ARM64 without Docker (uses ffmpeg8 libraries built via scripts/build-ffmpeg-android.sh --arch=arm64)
# Key: Use -linkmode=external and -Wl,-Bdynamic to ensure dynamic linking of libc.so
# This prevents static linking of bionic's getauxval which crashes on Android
ffstream-android-arm64-static-cgo: build $(GOPATH)/bin/pkg-config-wrapper 3rdparty/arm64/android-ndk-$(ANDROID_NDK_VERSION) 3rdparty/arm64/sysroot
	PKG_CONFIG_WRAPPER_LOG='/tmp/pkg_config_wrapper.log' \
	PKG_CONFIG_WRAPPER_LOG_LEVEL='trace' \
	PKG_CONFIG_LIBS_FORCE_STATIC='libav*,libsrt' \
	PKG_CONFIG_ERASE="-fopenmp=*,-landroid,-lcamera2ndk,-lmediandk,-lpulse,-lz,-D_REENTRANT" \
	PKG_CONFIG='$(GOPATH)/bin/pkg-config-wrapper' \
	PKG_CONFIG_PATH='$(CURDIR)/3rdparty/arm64/sysroot/lib/pkgconfig' \
	CGO_CFLAGS='-std=gnu99 -I$(NDK_SYSROOT)/usr/include/ -I$(CURDIR)/3rdparty/arm64/sysroot/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow' \
	CGO_LDFLAGS='-v -Wl,-Bstatic -lcrypto -lv4lconvert -ljpeg -lc++_static -lc++abi -Wl,-Bdynamic -ldl -lc -landroid -lcamera2ndk -lmediandk -lz -L$(NDK_SYSROOT)/usr/lib/aarch64-linux-android/35/ -L$(NDK_SYSROOT)/usr/lib/aarch64-linux-android/ -L$(NDK_SYSROOT)/usr/lib/ -L$(CURDIR)/3rdparty/arm64/sysroot/lib' \
	ANDROID_NDK_HOME="$(ANDROID_NDK_HOME)" \
	CC="$(CC_PREFIX) $(ANDROID_ARM64_CC)" \
	CXX="$(CC_PREFIX) $(ANDROID_ARM64_CXX)" \
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
	GOPROXY=off \
	go build $(GOBUILD_FLAGS),mediacodec,patched_libav -ldflags='-linkmode=external' -o bin/ffstream-android-arm64 ./cmd/ffstream
	ls -ldh bin/ffstream-android-arm64

# Build ffstream for Android x86_64 (for emulator testing)
ffstream-android-x86_64-static-cgo: build $(GOPATH)/bin/pkg-config-wrapper 3rdparty/arm64/android-ndk-$(ANDROID_NDK_VERSION) 3rdparty/x86_64/sysroot
	PKG_CONFIG_WRAPPER_LOG='/tmp/pkg_config_wrapper.log' \
	PKG_CONFIG_WRAPPER_LOG_LEVEL='trace' \
	PKG_CONFIG_LIBS_FORCE_STATIC='libav*,libsrt' \
	PKG_CONFIG_ERASE="-fopenmp=*,-landroid,-lcamera2ndk,-lmediandk,-lpulse,-D_REENTRANT" \
	PKG_CONFIG='$(GOPATH)/bin/pkg-config-wrapper' \
	PKG_CONFIG_PATH='$(CURDIR)/3rdparty/x86_64/sysroot/lib/pkgconfig' \
	CGO_CFLAGS='-std=gnu99 -I$(NDK_SYSROOT)/usr/include/ -I$(CURDIR)/3rdparty/x86_64/sysroot/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow' \
	CGO_LDFLAGS='-v -Wl,-Bstatic -lcrypto -lv4lconvert -ljpeg -lc++_static -lc++abi -Wl,-Bdynamic -ldl -lc -landroid -lcamera2ndk -lmediandk -L$(NDK_SYSROOT)/usr/lib/x86_64-linux-android/35/ -L$(NDK_SYSROOT)/usr/lib/x86_64-linux-android/ -L$(NDK_SYSROOT)/usr/lib/ -L$(CURDIR)/3rdparty/x86_64/sysroot/lib' \
	ANDROID_NDK_HOME="$(ANDROID_NDK_HOME)" \
	CC="$(CC_PREFIX) $(ANDROID_X86_64_CC)" \
	CXX="$(CC_PREFIX) $(ANDROID_X86_64_CXX)" \
	CGO_ENABLED=1 GOOS=android GOARCH=amd64 \
	go build $(GOBUILD_FLAGS),mediacodec,patched_libav -ldflags='-linkmode=external' -o bin/ffstream-android-x86_64 ./cmd/ffstream
	ls -ldh bin/ffstream-android-x86_64

# Convenience targets: build everything for Android (deps + binary)
bin/ffstream-android-arm64: ffstream-android-arm64-static-cgo
bin/ffstream-android-x86_64: ffstream-android-x86_64-static-cgo
