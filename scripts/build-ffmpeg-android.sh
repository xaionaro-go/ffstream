#!/bin/bash
# Build FFmpeg 8.0.1 with MediaCodec patches for Android (arm64 or x86_64)
# using the NDK toolchain directly.
#
# Usage:
#   ./scripts/build-ffmpeg-android.sh --arch=arm64|x86_64 [--clean]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FFSTREAM_DIR="$(dirname "$SCRIPT_DIR")"
NDK_DIR="${FFSTREAM_DIR}/3rdparty/arm64/android-ndk-r28-beta2"
API_LEVEL=35
FFMPEG_VERSION="8.0.1"

# Parse arguments
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
            echo "  --arch=arm64|x86_64  Target architecture (required)"
            echo "  --clean              Clean build (remove previous build artifacts)"
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

# Set architecture-specific variables
case "$ARCH" in
    arm64)
        FFMPEG_ARCH="aarch64"
        CC_PREFIX="aarch64-linux-android${API_LEVEL}"
        EXTRA_CONFIGURE_FLAGS=""
        BUILD_SUBDIR="build-arm64"
        ;;
    x86_64)
        FFMPEG_ARCH="x86_64"
        CC_PREFIX="x86_64-linux-android${API_LEVEL}"
        EXTRA_CONFIGURE_FLAGS="--disable-x86asm"
        BUILD_SUBDIR="build-x86_64"
        ;;
    *)
        echo "ERROR: Unsupported architecture: $ARCH (must be arm64 or x86_64)"
        exit 1
        ;;
esac

BUILD_DIR="/tmp/ffmpeg-${ARCH}-build"
OUTPUT_DIR="${FFSTREAM_DIR}/3rdparty/${ARCH}/sysroot"

echo "=== Building FFmpeg ${FFMPEG_VERSION} for Android ${ARCH} ==="
echo "FFSTREAM_DIR: ${FFSTREAM_DIR}"
echo "NDK_DIR: ${NDK_DIR}"
echo "OUTPUT_DIR: ${OUTPUT_DIR}"
echo "FFMPEG_ARCH: ${FFMPEG_ARCH}"

# Toolchain setup
TOOLCHAIN="${NDK_DIR}/toolchains/llvm/prebuilt/linux-x86_64"
CC="${TOOLCHAIN}/bin/${CC_PREFIX}-clang"
CXX="${TOOLCHAIN}/bin/${CC_PREFIX}-clang++"
AR="${TOOLCHAIN}/bin/llvm-ar"
NM="${TOOLCHAIN}/bin/llvm-nm"
STRIP="${TOOLCHAIN}/bin/llvm-strip"
RANLIB="${TOOLCHAIN}/bin/llvm-ranlib"
SYSROOT="${TOOLCHAIN}/sysroot"

if [ ! -f "$CC" ]; then
    echo "ERROR: NDK compiler not found at $CC"
    echo "Make sure the NDK is installed at $NDK_DIR"
    exit 1
fi

INSTALL_PREFIX="${BUILD_DIR}/install"

if [ "$CLEAN" = true ]; then
    echo "Cleaning previous build..."
    rm -rf "$BUILD_DIR"
fi

mkdir -p "$BUILD_DIR"
cd "$BUILD_DIR"

# Step 1: Download FFmpeg source if not present
if [ ! -d "ffmpeg-${FFMPEG_VERSION}" ]; then
    echo "=== Downloading FFmpeg ${FFMPEG_VERSION} ==="
    if [ ! -f "ffmpeg-${FFMPEG_VERSION}.tar.xz" ]; then
        wget -q "https://www.ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz"
    fi
    tar xf "ffmpeg-${FFMPEG_VERSION}.tar.xz"
fi

cd "ffmpeg-${FFMPEG_VERSION}"

# Step 2: Apply patches
echo "=== Applying patches ==="
PATCH_DIR="${SCRIPT_DIR}/patches"
for patch_file in \
    "${PATCH_DIR}/configure.patch" \
    "${PATCH_DIR}/libavcodec-allcodecs.c.patch" \
    "${PATCH_DIR}/libavutil-file_open.c.patch" \
    "${PATCH_DIR}/mediacodec_set_parameters.patch"; do
    if [ -f "$patch_file" ]; then
        pname=$(basename "$patch_file")
        if ! patch -p1 -N --dry-run < "$patch_file" >/dev/null 2>&1; then
            echo "  $pname: already applied or does not apply, skipping"
        else
            echo "  Applying $pname..."
            patch -p1 -N < "$patch_file"
        fi
    else
        echo "  WARNING: $patch_file not found"
    fi
done

# Step 3: Configure
echo "=== Configuring FFmpeg for ${ARCH} Android ==="
mkdir -p "$BUILD_SUBDIR"
cd "$BUILD_SUBDIR"

CFLAGS="-I${SYSROOT}/usr/include -Wno-incompatible-function-pointer-types -Wno-unused-result -Wno-xor-used-as-pow"

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
    --prefix="$INSTALL_PREFIX" \
    --target-os=android \
    --disable-vulkan \
    --extra-cflags="$CFLAGS" \
    --disable-libfdk-aac \
    --disable-programs \
    --disable-doc \
    $EXTRA_CONFIGURE_FLAGS

# FFmpeg 8 uses -std=c17 which breaks NDK sysroot headers that use
# GNU asm() extension. Replace with -std=gnu17 after configure.
sed -i 's/-std=c17/-std=gnu17/g' ffbuild/config.mak

# Step 4: Build
echo "=== Building FFmpeg ($(nproc) parallel jobs) ==="
make -j"$(nproc)"

# Step 5: Install to staging area
echo "=== Installing ==="
make install

# Step 6: Copy to ffstream 3rdparty
echo "=== Copying to $OUTPUT_DIR ==="
mkdir -p "$OUTPUT_DIR/lib/pkgconfig"
mkdir -p "$OUTPUT_DIR/include"

# Copy static libraries
cp "$INSTALL_PREFIX/lib/libav"*.a "$OUTPUT_DIR/lib/"
cp "$INSTALL_PREFIX/lib/libsw"*.a "$OUTPUT_DIR/lib/"
[ -f "$INSTALL_PREFIX/lib/libpostproc.a" ] && cp "$INSTALL_PREFIX/lib/libpostproc.a" "$OUTPUT_DIR/lib/"

# Copy headers
cp -r "$INSTALL_PREFIX/include/libav"* "$OUTPUT_DIR/include/"
cp -r "$INSTALL_PREFIX/include/libsw"* "$OUTPUT_DIR/include/"
[ -d "$INSTALL_PREFIX/include/libpostproc" ] && cp -r "$INSTALL_PREFIX/include/libpostproc" "$OUTPUT_DIR/include/"

# Copy pkgconfig files
cp "$INSTALL_PREFIX/lib/pkgconfig/libav"*.pc "$OUTPUT_DIR/lib/pkgconfig/"
cp "$INSTALL_PREFIX/lib/pkgconfig/libsw"*.pc "$OUTPUT_DIR/lib/pkgconfig/"
[ -f "$INSTALL_PREFIX/lib/pkgconfig/libpostproc.pc" ] && cp "$INSTALL_PREFIX/lib/pkgconfig/libpostproc.pc" "$OUTPUT_DIR/lib/pkgconfig/"

# Step 7: Fix pkgconfig paths
cd "$OUTPUT_DIR/lib/pkgconfig"
for pc in *.pc; do
    sed -i "s|prefix=.*|prefix=$OUTPUT_DIR|g" "$pc"
    sed -i "s|libdir=.*|libdir=$OUTPUT_DIR/lib|g" "$pc"
    sed -i "s|includedir=.*|includedir=$OUTPUT_DIR/include|g" "$pc"
    sed -i 's/-landroid //g; s/-lmediandk//g; s/-lcamera2ndk//g' "$pc"
done

echo ""
echo "=== Build Complete ==="
echo "Libraries:"
ls -lh "$OUTPUT_DIR/lib/"*.a
echo ""
echo "Headers:"
ls "$OUTPUT_DIR/include/"
echo ""
echo "Pkgconfig:"
ls "$OUTPUT_DIR/lib/pkgconfig/"
echo ""
echo "FFmpeg ${FFMPEG_VERSION} ${ARCH} libraries installed to: $OUTPUT_DIR"
