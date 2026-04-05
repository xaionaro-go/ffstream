#!/bin/bash
# Build ALL dependencies + FFmpeg for Android (arm64 or x86_64)
# Runs inside the Docker container (or can be invoked directly with the NDK present).
#
# Usage:
#   ./scripts/build-all-android.sh --arch=arm64|x86_64 [--clean]
#
# All outputs go to /output/ (mapped from the host).

set -euo pipefail

###############################################################################
# Configuration
###############################################################################

NDK_DIR="${ANDROID_NDK_ROOT:-/opt/android-ndk}"
API_LEVEL=35
JOBS="$(nproc)"

OPENSSL_VERSION="3.6.1"
LIBJPEG_TURBO_VERSION="3.1.3"
V4L_UTILS_VERSION="1.28.1"
FFMPEG_VERSION="8.0.1"

PATCH_DIR="/build/patches"
# When running outside Docker, use the script-relative patch dir as fallback
if [ ! -d "$PATCH_DIR" ]; then
    PATCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/patches"
fi

###############################################################################
# Parse arguments
###############################################################################

ARCH=""
CLEAN=false

for arg in "$@"; do
    case $arg in
        --arch=*)
            ARCH="${arg#--arch=}"
            ;;
        --clean)
            CLEAN=true
            ;;
        --help|-h)
            echo "Usage: $0 --arch=arm64|x86_64 [--clean]"
            exit 0
            ;;
        *)
            echo "ERROR: Unknown argument: $arg"
            echo "Usage: $0 --arch=arm64|x86_64 [--clean]"
            exit 1
            ;;
    esac
done

if [ -z "$ARCH" ]; then
    echo "ERROR: --arch is required"
    echo "Usage: $0 --arch=arm64|x86_64 [--clean]"
    exit 1
fi

###############################################################################
# Architecture-specific variables
###############################################################################

case "$ARCH" in
    arm64)
        ANDROID_ABI="arm64-v8a"
        CC_PREFIX="aarch64-linux-android${API_LEVEL}"
        OPENSSL_TARGET="android-arm64"
        FFMPEG_ARCH="aarch64"
        FFMPEG_EXTRA_FLAGS=""
        MESON_CPU_FAMILY="aarch64"
        MESON_CPU="aarch64"
        ;;
    x86_64)
        ANDROID_ABI="x86_64"
        CC_PREFIX="x86_64-linux-android${API_LEVEL}"
        OPENSSL_TARGET="android-x86_64"
        FFMPEG_ARCH="x86_64"
        FFMPEG_EXTRA_FLAGS="--disable-x86asm"
        MESON_CPU_FAMILY="x86_64"
        MESON_CPU="x86_64"
        ;;
    *)
        echo "ERROR: Unsupported architecture: $ARCH (must be arm64 or x86_64)"
        exit 1
        ;;
esac

TOOLCHAIN="${NDK_DIR}/toolchains/llvm/prebuilt/linux-x86_64"
CC="${TOOLCHAIN}/bin/${CC_PREFIX}-clang"
CXX="${TOOLCHAIN}/bin/${CC_PREFIX}-clang++"
AR="${TOOLCHAIN}/bin/llvm-ar"
NM="${TOOLCHAIN}/bin/llvm-nm"
STRIP="${TOOLCHAIN}/bin/llvm-strip"
RANLIB="${TOOLCHAIN}/bin/llvm-ranlib"
NDK_SYSROOT="${TOOLCHAIN}/sysroot"

if [ ! -f "$CC" ]; then
    echo "ERROR: NDK compiler not found at $CC"
    echo "Make sure the NDK is installed at $NDK_DIR"
    exit 1
fi

BUILD_DIR="/tmp/android-build-${ARCH}"
OUTPUT_DIR="/output"

if [ "$CLEAN" = true ]; then
    echo "=== Cleaning previous build ==="
    rm -rf "$BUILD_DIR"
fi

mkdir -p "$BUILD_DIR"
mkdir -p "$OUTPUT_DIR/lib/pkgconfig"
mkdir -p "$OUTPUT_DIR/include"

echo "============================================================"
echo "  Building all dependencies + FFmpeg for Android ${ARCH}"
echo "============================================================"
echo "NDK:        ${NDK_DIR}"
echo "API level:  ${API_LEVEL}"
echo "ABI:        ${ANDROID_ABI}"
echo "Output:     ${OUTPUT_DIR}"
echo "Jobs:       ${JOBS}"
echo ""

###############################################################################
# 1. OpenSSL (libcrypto only)
###############################################################################

build_openssl() {
    echo ""
    echo "=== [1/4] Building OpenSSL ${OPENSSL_VERSION} (libcrypto) ==="

    local src_dir="${BUILD_DIR}/openssl-${OPENSSL_VERSION}"

    if [ -f "$OUTPUT_DIR/lib/libcrypto.a" ] && [ "$CLEAN" != true ]; then
        echo "  libcrypto.a already exists, skipping"
        return
    fi

    cd "$BUILD_DIR"
    if [ ! -d "$src_dir" ]; then
        if [ ! -f "openssl-${OPENSSL_VERSION}.tar.gz" ]; then
            echo "  Downloading OpenSSL ${OPENSSL_VERSION}..."
            wget -q "https://github.com/openssl/openssl/releases/download/openssl-${OPENSSL_VERSION}/openssl-${OPENSSL_VERSION}.tar.gz"
        fi
        tar xf "openssl-${OPENSSL_VERSION}.tar.gz"
    fi

    cd "$src_dir"

    if [ "$CLEAN" = true ] || [ ! -f "libcrypto.a" ]; then
        echo "  Configuring..."
        export ANDROID_NDK_ROOT="$NDK_DIR"
        export PATH="${TOOLCHAIN}/bin:$PATH"

        ./Configure "${OPENSSL_TARGET}" \
            -D__ANDROID_API__=${API_LEVEL} \
            --prefix="$OUTPUT_DIR" \
            --openssldir="$OUTPUT_DIR/ssl" \
            no-shared \
            no-tests \
            no-ui-console \
            no-ssl3 \
            no-comp

        echo "  Building libcrypto..."
        make -j"$JOBS" build_libs
    fi

    echo "  Installing libcrypto..."
    cp libcrypto.a "$OUTPUT_DIR/lib/"
    cp -r include/openssl "$OUTPUT_DIR/include/"
    # Also copy generated opensslconf.h etc.
    if [ -d "include/crypto" ]; then
        cp -r include/crypto "$OUTPUT_DIR/include/"
    fi

    # Create pkgconfig file
    cat > "$OUTPUT_DIR/lib/pkgconfig/libcrypto.pc" <<PCEOF
prefix=$OUTPUT_DIR
exec_prefix=\${prefix}
libdir=\${exec_prefix}/lib
includedir=\${prefix}/include

Name: OpenSSL-libcrypto
Description: OpenSSL cryptography library
Version: ${OPENSSL_VERSION}
Libs: -L\${libdir} -lcrypto
Libs.private: -ldl -lpthread
Cflags: -I\${includedir}
PCEOF

    echo "  Done: libcrypto.a"
}

###############################################################################
# 2. libjpeg-turbo
###############################################################################

build_libjpeg_turbo() {
    echo ""
    echo "=== [2/4] Building libjpeg-turbo ${LIBJPEG_TURBO_VERSION} ==="

    local src_dir="${BUILD_DIR}/libjpeg-turbo-${LIBJPEG_TURBO_VERSION}"
    local build_subdir="${src_dir}/build-android"

    if [ -f "$OUTPUT_DIR/lib/libjpeg.a" ] && [ "$CLEAN" != true ]; then
        echo "  libjpeg.a already exists, skipping"
        return
    fi

    cd "$BUILD_DIR"
    if [ ! -d "$src_dir" ]; then
        if [ ! -f "libjpeg-turbo-${LIBJPEG_TURBO_VERSION}.tar.gz" ]; then
            echo "  Downloading libjpeg-turbo ${LIBJPEG_TURBO_VERSION}..."
            wget -q "https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/${LIBJPEG_TURBO_VERSION}/libjpeg-turbo-${LIBJPEG_TURBO_VERSION}.tar.gz"
        fi
        tar xf "libjpeg-turbo-${LIBJPEG_TURBO_VERSION}.tar.gz"
    fi

    rm -rf "$build_subdir"
    mkdir -p "$build_subdir"
    cd "$build_subdir"

    echo "  Configuring with CMake..."
    cmake .. \
        -G Ninja \
        -DCMAKE_TOOLCHAIN_FILE="${NDK_DIR}/build/cmake/android.toolchain.cmake" \
        -DANDROID_ABI="${ANDROID_ABI}" \
        -DANDROID_PLATFORM="android-${API_LEVEL}" \
        -DCMAKE_INSTALL_PREFIX="$OUTPUT_DIR" \
        -DENABLE_SHARED=OFF \
        -DENABLE_STATIC=ON \
        -DWITH_TURBOJPEG=OFF \
        -DCMAKE_BUILD_TYPE=Release

    echo "  Building..."
    ninja -j"$JOBS"

    echo "  Installing..."
    ninja install

    echo "  Done: libjpeg.a"
}

###############################################################################
# 3. v4l-utils (libv4lconvert only)
###############################################################################

build_v4lconvert() {
    echo ""
    echo "=== [3/4] Building v4l-utils ${V4L_UTILS_VERSION} (libv4lconvert) ==="

    local src_dir="${BUILD_DIR}/v4l-utils-${V4L_UTILS_VERSION}"
    local build_subdir="${src_dir}/build-android"

    if [ -f "$OUTPUT_DIR/lib/libv4lconvert.a" ] && [ "$CLEAN" != true ]; then
        echo "  libv4lconvert.a already exists, skipping"
        return
    fi

    cd "$BUILD_DIR"
    if [ ! -d "$src_dir" ]; then
        if [ ! -f "v4l-utils-${V4L_UTILS_VERSION}.tar.xz" ]; then
            echo "  Downloading v4l-utils ${V4L_UTILS_VERSION}..."
            wget -q "https://linuxtv.org/downloads/v4l-utils/v4l-utils-${V4L_UTILS_VERSION}.tar.xz"
        fi
        tar xf "v4l-utils-${V4L_UTILS_VERSION}.tar.xz"
    fi

    cd "$src_dir"

    # Create a meson cross file for Android
    local cross_file="${BUILD_DIR}/meson-cross-${ARCH}.ini"
    cat > "$cross_file" <<CROSSEOF
[binaries]
c = '${CC}'
cpp = '${CXX}'
ar = '${AR}'
strip = '${STRIP}'
ranlib = '${RANLIB}'
pkgconfig = 'pkg-config'

[properties]
sys_root = '${NDK_SYSROOT}'
c_args = ['-I${OUTPUT_DIR}/include']
c_link_args = ['-L${OUTPUT_DIR}/lib']

[host_machine]
system = 'android'
cpu_family = '${MESON_CPU_FAMILY}'
cpu = '${MESON_CPU}'
endian = 'little'
CROSSEOF

    rm -rf "$build_subdir"
    mkdir -p "$build_subdir"

    echo "  Configuring with meson..."
    # We only need libv4lconvert. Disable everything else.
    meson setup "$build_subdir" \
        --cross-file="$cross_file" \
        --prefix="$OUTPUT_DIR" \
        --default-library=static \
        --buildtype=release \
        -Dbpf=disabled \
        -Dv4l2-compliance=disabled \
        -Dv4l2-ctl=disabled \
        -Dgconv=disabled \
        -Ddoxygen-doc=disabled \
        -Ddoxygen-html=disabled \
        -Ddoxygen-man=disabled \
        -Djpeg=enabled \
        2>&1 || true
    # meson may fail on some optional deps; we only need the libv4lconvert target

    cd "$build_subdir"

    echo "  Building libv4lconvert..."
    # Try building just libv4lconvert via meson/ninja
    if ninja -j"$JOBS" lib/libv4lconvert/libv4lconvert.a 2>/dev/null; then
        echo "  Meson build succeeded"
        cp lib/libv4lconvert/libv4lconvert.a "$OUTPUT_DIR/lib/"
    else
        echo "  Meson build of libv4lconvert failed, falling back to manual compile..."
        build_v4lconvert_manual "$src_dir"
    fi

    # Install header
    mkdir -p "$OUTPUT_DIR/include"
    if [ -f "$src_dir/lib/include/libv4lconvert.h" ]; then
        cp "$src_dir/lib/include/libv4lconvert.h" "$OUTPUT_DIR/include/"
    elif [ -f "$src_dir/lib/libv4lconvert/libv4lconvert.h" ]; then
        cp "$src_dir/lib/libv4lconvert/libv4lconvert.h" "$OUTPUT_DIR/include/"
    fi

    echo "  Done: libv4lconvert.a"
}

build_v4lconvert_manual() {
    local src_dir="$1"
    local v4l_src="${src_dir}/lib/libv4lconvert"
    local work_dir="${BUILD_DIR}/v4lconvert-manual-${ARCH}"

    echo "  Manual compile of libv4lconvert..."

    rm -rf "$work_dir"
    mkdir -p "$work_dir"

    # Collect all .c source files in libv4lconvert (excluding control/ and processing/)
    local c_files=()
    for f in "$v4l_src"/*.c; do
        [ -f "$f" ] && c_files+=("$f")
    done

    if [ ${#c_files[@]} -eq 0 ]; then
        echo "  ERROR: No .c files found in $v4l_src"
        exit 1
    fi

    local cflags=(
        -I"${v4l_src}"
        -I"${src_dir}/lib/include"
        -I"${src_dir}/include"
        -I"${OUTPUT_DIR}/include"
        -DHAVE_JPEG=1
        -DANDROID
        -D__ANDROID_API__=${API_LEVEL}
        -fPIC
        -O2
    )

    local obj_files=()
    for f in "${c_files[@]}"; do
        local base
        base=$(basename "$f" .c)
        echo "    Compiling ${base}.c..."
        "$CC" "${cflags[@]}" -c "$f" -o "${work_dir}/${base}.o" 2>/dev/null || \
            echo "    WARNING: Failed to compile ${base}.c (non-fatal)"
        if [ -f "${work_dir}/${base}.o" ]; then
            obj_files+=("${work_dir}/${base}.o")
        fi
    done

    if [ ${#obj_files[@]} -eq 0 ]; then
        echo "  ERROR: No object files produced"
        exit 1
    fi

    "$AR" rcs "$OUTPUT_DIR/lib/libv4lconvert.a" "${obj_files[@]}"
    echo "  Created libv4lconvert.a from ${#obj_files[@]} object files"
}

###############################################################################
# 4. FFmpeg
###############################################################################

build_ffmpeg() {
    echo ""
    echo "=== [4/4] Building FFmpeg ${FFMPEG_VERSION} ==="

    local src_dir="${BUILD_DIR}/ffmpeg-${FFMPEG_VERSION}"
    local build_subdir="build-${ARCH}"
    local install_prefix="${BUILD_DIR}/ffmpeg-install-${ARCH}"

    if [ -f "$OUTPUT_DIR/lib/libavcodec.a" ] && [ "$CLEAN" != true ]; then
        echo "  libavcodec.a already exists, skipping"
        return
    fi

    cd "$BUILD_DIR"
    if [ ! -d "$src_dir" ]; then
        if [ ! -f "ffmpeg-${FFMPEG_VERSION}.tar.xz" ]; then
            echo "  Downloading FFmpeg ${FFMPEG_VERSION}..."
            wget -q "https://www.ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz"
        fi
        tar xf "ffmpeg-${FFMPEG_VERSION}.tar.xz"
    fi

    cd "$src_dir"

    # Apply patches
    echo "  Applying patches..."
    for patch_file in \
        "${PATCH_DIR}/configure.patch" \
        "${PATCH_DIR}/libavcodec-allcodecs.c.patch" \
        "${PATCH_DIR}/libavutil-file_open.c.patch" \
        "${PATCH_DIR}/mediacodec_set_parameters.patch"; do
        if [ -f "$patch_file" ]; then
            pname=$(basename "$patch_file")
            if ! patch -p1 -N --dry-run < "$patch_file" >/dev/null 2>&1; then
                echo "    $pname: already applied or does not apply, skipping"
            else
                echo "    Applying $pname..."
                patch -p1 -N < "$patch_file"
            fi
        else
            echo "    WARNING: $patch_file not found"
        fi
    done

    # Configure
    echo "  Configuring..."
    mkdir -p "$build_subdir"
    cd "$build_subdir"

    CFLAGS="-I${NDK_SYSROOT}/usr/include -I${OUTPUT_DIR}/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow"
    LDFLAGS="-L${OUTPUT_DIR}/lib"

    # shellcheck disable=SC2086
    ../configure \
        --arch="${FFMPEG_ARCH}" \
        --cc="$CC" \
        --cxx="$CXX" \
        --nm="$NM" \
        --ar="$AR" \
        --ranlib="$RANLIB" \
        --strip="$STRIP" \
        --enable-static \
        --disable-shared \
        --disable-symver \
        --enable-cross-compile \
        --disable-gnutls \
        --enable-gpl \
        --enable-version3 \
        --enable-jni \
        --disable-indevs \
        --disable-outdevs \
        --enable-indev=android_camera \
        --enable-indev=lavfi \
        --disable-lcms2 \
        --disable-libaom \
        --disable-libass \
        --disable-libbluray \
        --disable-libdav1d \
        --disable-libfontconfig \
        --disable-libfreetype \
        --disable-libfribidi \
        --disable-libgme \
        --disable-libharfbuzz \
        --disable-libmp3lame \
        --disable-libopencore-amrnb \
        --disable-libopencore-amrwb \
        --disable-libopenmpt \
        --disable-libopus \
        --disable-librav1e \
        --disable-librubberband \
        --disable-libsoxr \
        --disable-libsrt \
        --disable-libssh \
        --disable-libsvtav1 \
        --disable-libtheora \
        --disable-libv4l2 \
        --disable-libvidstab \
        --disable-libvmaf \
        --disable-libvo-amrwbenc \
        --disable-libvorbis \
        --disable-libvpx \
        --disable-libwebp \
        --disable-libx264 \
        --disable-libx265 \
        --disable-libxml2 \
        --disable-libxvid \
        --disable-libzimg \
        --disable-bzlib \
        --disable-xlib \
        --enable-mediacodec \
        --disable-opencl \
        --prefix="$install_prefix" \
        --target-os=android \
        --disable-vulkan \
        --extra-cflags="$CFLAGS" \
        --extra-ldflags="$LDFLAGS" \
        --disable-libfdk-aac \
        --disable-programs \
        --disable-doc \
        $FFMPEG_EXTRA_FLAGS

    # FFmpeg 8 uses -std=c17 which breaks NDK sysroot headers that use
    # GNU asm() extension. Replace with -std=gnu17 after configure.
    sed -i 's/-std=c17/-std=gnu17/g' ffbuild/config.mak

    # Build
    echo "  Building (${JOBS} parallel jobs)..."
    make -j"$JOBS"

    # Install to staging
    echo "  Installing..."
    make install

    # Copy to output
    echo "  Copying to output..."
    cp "$install_prefix/lib/libav"*.a "$OUTPUT_DIR/lib/"
    cp "$install_prefix/lib/libsw"*.a "$OUTPUT_DIR/lib/"
    [ -f "$install_prefix/lib/libpostproc.a" ] && \
        cp "$install_prefix/lib/libpostproc.a" "$OUTPUT_DIR/lib/"

    cp -r "$install_prefix/include/libav"* "$OUTPUT_DIR/include/"
    cp -r "$install_prefix/include/libsw"* "$OUTPUT_DIR/include/"
    [ -d "$install_prefix/include/libpostproc" ] && \
        cp -r "$install_prefix/include/libpostproc" "$OUTPUT_DIR/include/"

    cp "$install_prefix/lib/pkgconfig/libav"*.pc "$OUTPUT_DIR/lib/pkgconfig/"
    cp "$install_prefix/lib/pkgconfig/libsw"*.pc "$OUTPUT_DIR/lib/pkgconfig/"
    [ -f "$install_prefix/lib/pkgconfig/libpostproc.pc" ] && \
        cp "$install_prefix/lib/pkgconfig/libpostproc.pc" "$OUTPUT_DIR/lib/pkgconfig/"

    # Fix pkgconfig paths to point at /output/
    cd "$OUTPUT_DIR/lib/pkgconfig"
    for pc in *.pc; do
        [ -f "$pc" ] || continue
        sed -i "s|prefix=.*|prefix=$OUTPUT_DIR|g" "$pc"
        sed -i "s|libdir=.*|libdir=$OUTPUT_DIR/lib|g" "$pc"
        sed -i "s|includedir=.*|includedir=$OUTPUT_DIR/include|g" "$pc"
        sed -i 's/-landroid //g; s/-lmediandk//g; s/-lcamera2ndk//g' "$pc"
    done

    echo "  Done: FFmpeg libraries"
}

###############################################################################
# Main
###############################################################################

build_openssl
build_libjpeg_turbo
build_v4lconvert
build_ffmpeg

echo ""
echo "============================================================"
echo "  Build Complete — Android ${ARCH}"
echo "============================================================"
echo ""
echo "Libraries:"
ls -lh "$OUTPUT_DIR/lib/"*.a 2>/dev/null || echo "  (none)"
echo ""
echo "Shared libraries:"
ls -lh "$OUTPUT_DIR/lib/"*.so 2>/dev/null || echo "  (none)"
echo ""
echo "Headers:"
ls "$OUTPUT_DIR/include/" 2>/dev/null || echo "  (none)"
echo ""
echo "Pkgconfig:"
ls "$OUTPUT_DIR/lib/pkgconfig/" 2>/dev/null || echo "  (none)"
echo ""
