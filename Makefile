
ENABLE_VLC?=false
ENABLE_LIBAV?=false

GOTAGS:=
ifeq ($(ENABLE_LIBAV), true)
	GOTAGS:=$(GOTAGS),with_libav
endif
ifeq ($(ENABLE_VLC), true)
	GOTAGS:=$(GOTAGS),with_libvlc
endif

GOTAGS:=$(GOTAGS:,%=%)
GOPATH?=$(shell go env GOPATH)

GOBUILD_FLAGS?=-buildvcs=true
ifneq ($(GOTAGS),)
	GOBUILD_FLAGS+=-tags=$(GOTAGS)
endif

all: ffstream-linux-amd64 ffstream-linux-arm64 ffstreamctl-linux-amd64 ffstreamctl-linux-arm64

build:
	mkdir -p build

ffstream-linux-amd64: build
	GOOS=linux GOARCH=amd64 go build $(GOBUILD_FLAGS) -o build/ffstream-linux-amd64 ./cmd/ffstream

ffstream-linux-arm64: build
	GOOS=linux GOARCH=arm64 go build $(GOBUILD_FLAGS) -o build/ffstream-linux-arm64 ./cmd/ffstream

ffstreamctl-linux-amd64: build
	GOOS=linux GOARCH=amd64 go build $(GOBUILD_FLAGS) -o build/ffstreamctl-linux-amd64 ./cmd/ffstreamctl

ffstreamctl-linux-arm64: build
	GOOS=linux GOARCH=arm64 go build $(GOBUILD_FLAGS) -o build/ffstreamctl-linux-arm64 ./cmd/ffstreamctl

DOCKER_IMAGE?=xaionaro2/streampanel-android-builder
DOCKER_CONTAINER_NAME?=streampanel-android-builder

dockerbuilder-android-arm64:
	docker pull  $(DOCKER_IMAGE)
	docker start $(DOCKER_IMAGE) >/dev/null 2>&1 || \
		docker run \
			--detach \
			--init \
			--name $(DOCKER_CONTAINER_NAME) \
			--volume ".:/project" \
			--tty \
			$(DOCKER_IMAGE) >/dev/null 2>&1 || /bin/true

dockerbuild-ffstream-android-arm64: dockerbuilder-android-arm64
	docker exec $(DOCKER_CONTAINER_NAME) make ENABLE_VLC="$(ENABLE_VLC)" ENABLE_LIBAV="$(ENABLE_LIBAV)" -C /project ffstream-android-arm64-in-docker

$(GOPATH)/bin/pkg-config-wrapper:
	go install github.com/xaionaro-go/pkg-config-wrapper@5dd443e6c18336416c49047e2ba0002e26a85278

ffstream-android-arm64-in-docker: build-ffstream-android-arm64-in-docker

build-ffstream-android-arm64-in-docker: build $(GOPATH)/bin/pkg-config-wrapper
	go mod tidy
	git config --global --add safe.directory /project
	$(eval ANDROID_NDK_HOME=$(shell ls -d /home/builder/lib/android-ndk-* | tail -1))
	cd cmd/ffstream && \
		PKG_CONFIG_WRAPPER_LOG='/tmp/pkg_config_wrapper.log' \
		PKG_CONFIG_WRAPPER_LOG_LEVEL='trace' \
		PKG_CONFIG_LIBS_FORCE_STATIC='libav*,libvlc,libsrt' \
		PKG_CONFIG_ERASE="-fopenmp=*,-landroid,-lcamera2ndk,-lmediandk" \
		PKG_CONFIG='$(GOPATH)/bin/pkg-config-wrapper' \
		PKG_CONFIG_PATH='/data/data/com.termux/files/usr/lib/pkgconfig' \
		CGO_CFLAGS='-I$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/include/ -I/data/data/com.termux/files/usr/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow' \
		CGO_LDFLAGS='-v -Wl,-Bdynamic -ldl -lc -lcamera2ndk -lmediandk -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/ -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/aarch64-linux-android/24/ -L/data/data/com.termux/files/usr/lib' \
		ANDROID_NDK_HOME="$(ANDROID_NDK_HOME)" \
		PATH="${PATH}:${HOME}/go/bin" \
		GOFLAGS="$(GOBUILD_FLAGS) -ldflags=$(shell echo ${LINKER_FLAGS_ANDROID} | tr " " ",")" \
		fyne package $(FYNEBUILD_FLAGS) --appID center.dx.ffstream --use-raw-icon -release -os android/arm64 && mv ffstream.apk ../../build/ffstream-arm64.apk
