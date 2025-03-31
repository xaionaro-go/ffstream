
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

bin/ffstream-linux-amd64: build
	GOOS=linux GOARCH=amd64 go build $(GOBUILD_FLAGS) -o bin/ffstream-linux-amd64 ./cmd/ffstream

bin/ffstream-linux-arm64: build
	GOOS=linux GOARCH=arm64 go build $(GOBUILD_FLAGS) -o bin/ffstream-linux-arm64 ./cmd/ffstream

bin/ffstreamctl-linux-amd64: build
	CGO_ENABLED=false GOOS=linux GOARCH=amd64 go build $(GOBUILD_FLAGS) -o bin/ffstreamctl-linux-amd64 ./cmd/ffstreamctl

bin/ffstreamctl-linux-arm64: build
	CGO_ENABLED=false GOOS=linux GOARCH=arm64 go build $(GOBUILD_FLAGS) -o bin/ffstreamctl-linux-arm64 ./cmd/ffstreamctl

DOCKER_IMAGE?=xaionaro2/streampanel-android-builder
DOCKER_CONTAINER_NAME?=ffstream-android-builder

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

bin/ffstream-android-arm64: dockerbuilder-android-arm64
	@if git diff-files --quiet; then \
		git branch -D v-termux; \
		git branch v-termux; \
		docker exec $(DOCKER_CONTAINER_NAME) make ENABLE_VLC="$(ENABLE_VLC)" ENABLE_LIBAV="$(ENABLE_LIBAV)" -C /project ffstream-android-arm64-in-docker; \
		docker cp ffstream-android-builder:/home/builder/termux-packages/output/ffstream_-termux-1_aarch64.deb bin/ffstream-android-termux-arm64.deb; \
	else \
		echo "ERROR: there are uncommitted changes, please either stash them or commit" >&2; \
	fi

bin/ffstream-android-termux.deb: bin/ffstream-android-arm64

/home/builder/go/bin/pkg-config-wrapper:
	go install github.com/xaionaro-go/pkg-config-wrapper@5dd443e6c18336416c49047e2ba0002e26a85278

ffstream-android-arm64-in-docker: /home/builder/go/bin/pkg-config-wrapper
	git config --global --add safe.directory /project/.git
	chmod -R +w /home/builder/.termux-build/ffstream 2>/dev/null || /bin/true
	rm -rf /home/builder/.termux-build/ffstream
	cd /home/builder/termux-packages && \
		mkdir -p packages/ffstream && \
		cp /project/build/termux/build.sh ./packages/ffstream/build.sh && \
		bash -x ./build-package.sh ffstream

ffstream-android-arm64-in-termux: build
	go mod tidy
	chmod -R +w /home/builder/.termux-build/ffstream
	for F in ~/.termux-build/ffstream/build/pkg/mod/golang.org/toolchain@v0.0.1-go*.linux-amd64/src/runtime/cgo/cgo.go; do \
		sed -e 's/Werror/Wno-error/g' -i $$F; \
	done
	git config --global --add safe.directory /project
	$(eval ANDROID_NDK_HOME=$(shell ls -d /home/builder/lib/android-ndk-* | tail -1))
	PKG_CONFIG_WRAPPER_LOG='/tmp/pkg_config_wrapper.log' \
	PKG_CONFIG_WRAPPER_LOG_LEVEL='trace' \
	PKG_CONFIG_LIBS_FORCE_STATIC='libav*,libvlc,libsrt' \
	PKG_CONFIG_ERASE="-fopenmp=*,-landroid,-lcamera2ndk,-lmediandk" \
	PKG_CONFIG='/home/builder/go/bin/pkg-config-wrapper' \
	PKG_CONFIG_PATH='/data/data/com.termux/files/usr/lib/pkgconfig' \
	CGO_CFLAGS='-std=gnu99 -I$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/include/ -I/data/data/com.termux/files/usr/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow' \
	CGO_LDFLAGS='-v -Wl,-Bdynamic -ldl -lc -landroid -lcamera2ndk -lmediandk -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/ -L$(ANDROID_NDK_HOME)/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/aarch64-linux-android/24/ -L/data/data/com.termux/files/usr/lib' \
	ANDROID_NDK_HOME="$(ANDROID_NDK_HOME)" \
	PATH="${PATH}:${HOME}/go/bin" \
	GOFLAGS="$(GOBUILD_FLAGS) -ldflags=$(shell echo ${LINKER_FLAGS_ANDROID} | tr " " ",")" \
	go build -x -o bin/ffstream-android-arm64 ./cmd/ffstream
