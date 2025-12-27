#!/bin/bash
# Build ffmpeg7 with MediaCodec patches for Android arm64 (termux)
# This script automates the entire ffmpeg build process for ffstream
#
# Prerequisites:
#   - Android NDK r28c (or similar) at /root/lib/android-ndk-r28c or ANDROID_NDK_HOME
#   - Android SDK at /root/lib/android-sdk-* or ANDROID_SDK_ROOT
#   - Git, wget, and standard build tools
#
# Usage:
#   ./build/build-ffmpeg-for-android.sh [--clean] [--skip-deps]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FFSTREAM_DIR="$(dirname "$SCRIPT_DIR")"
TERMUX_PACKAGES_DIR="${TERMUX_PACKAGES_DIR:-/workspaces/xaionaro-go/termux-packages}"
OUTPUT_DIR="${FFSTREAM_DIR}/3rdparty/arm64/termux/data/data/com.termux/files/usr"
TERMUX_PREFIX="/data/data/com.termux/files/usr"

# Parse arguments
CLEAN=false
SKIP_DEPS=false
for arg in "$@"; do
    case $arg in
        --clean)
            CLEAN=true
            ;;
        --skip-deps)
            SKIP_DEPS=true
            ;;
        --help|-h)
            echo "Usage: $0 [--clean] [--skip-deps]"
            echo "  --clean     Clean build (remove previous build artifacts)"
            echo "  --skip-deps Skip building ffmpeg dependencies"
            exit 0
            ;;
    esac
done

echo "=== Building ffmpeg7 for Android arm64 (termux) ==="
echo "FFSTREAM_DIR: $FFSTREAM_DIR"
echo "TERMUX_PACKAGES_DIR: $TERMUX_PACKAGES_DIR"
echo "OUTPUT_DIR: $OUTPUT_DIR"

# Step 1: Clone or update termux-packages
echo ""
echo "=== Step 1: Setup termux-packages ==="
if [ ! -d "$TERMUX_PACKAGES_DIR" ]; then
    echo "Cloning termux-packages from xaionaro fork..."
    git clone https://github.com/xaionaro/termux-packages.git "$TERMUX_PACKAGES_DIR"
else
    echo "termux-packages already exists at $TERMUX_PACKAGES_DIR"
    if [ "$CLEAN" = true ]; then
        echo "Resetting termux-packages to clean state..."
        cd "$TERMUX_PACKAGES_DIR"
        git fetch origin
        git reset --hard origin/master || git reset --hard origin/main
    fi
fi

cd "$TERMUX_PACKAGES_DIR"

# Step 2: Setup Android SDK/NDK if not present
echo ""
echo "=== Step 2: Setup Android SDK/NDK ==="
if [ ! -d "/root/lib/android-sdk-"* ] && [ -z "$ANDROID_SDK_ROOT" ]; then
    echo "Setting up Android SDK..."
    ./scripts/setup-android-sdk.sh
fi

# Step 3: Create termux prefix directory
echo ""
echo "=== Step 3: Create termux prefix ==="
mkdir -p "$TERMUX_PREFIX"

# Step 4: Copy MediaCodec patches
echo ""
echo "=== Step 4: Copy MediaCodec patches ==="
FFMPEG_PKG_DIR="$TERMUX_PACKAGES_DIR/packages/ffmpeg"

# Copy the setParameters patch (adds ff_AMediaCodec_setParameters API)
if [ -f "$FFSTREAM_DIR/build/termux/ffmpeg_mediacodec_set_parameters.patch" ]; then
    cp "$FFSTREAM_DIR/build/termux/ffmpeg_mediacodec_set_parameters.patch" \
       "$FFMPEG_PKG_DIR/mediacodec_set_parameters.patch"
    echo "Copied mediacodec_set_parameters.patch"
else
    echo "WARNING: mediacodec_set_parameters.patch not found at $FFSTREAM_DIR/build/termux/"
fi

# NOTE: mediacodec_q_params.patch is NOT needed for ffmpeg 7.1+
# The qp_i_min, qp_p_min, qp_b_min parameters were upstreamed

# Step 5: Modify ffmpeg build.sh to disable external libraries we don't have
echo ""
echo "=== Step 5: Configure ffmpeg build (disable unavailable external libs) ==="
cd "$FFMPEG_PKG_DIR"

# Backup original build.sh
if [ ! -f "build.sh.orig" ]; then
    cp build.sh build.sh.orig
fi

# Disable external libraries that require additional dependencies
# These can be re-enabled if you build those dependencies first
sed -i 's/--enable-libopus/--disable-libopus/g' build.sh
sed -i 's/--enable-libsrt/--disable-libsrt/g' build.sh
sed -i 's/--enable-libv4l2/--disable-libv4l2/g' build.sh
sed -i 's/--enable-libx264/--disable-libx264/g' build.sh
sed -i 's/--enable-libx265/--disable-libx265/g' build.sh
sed -i 's/--enable-indev=pulse/--disable-indev=pulse/g' build.sh
sed -i 's/--enable-indev=v4l2/--disable-indev=v4l2/g' build.sh

echo "Modified build.sh to disable external libraries"
grep -E "disable-lib|disable-indev" build.sh || true

# Step 6: Build ffmpeg dependencies (if not skipping)
echo ""
echo "=== Step 6: Build ffmpeg dependencies ==="
cd "$TERMUX_PACKAGES_DIR"

if [ "$SKIP_DEPS" = false ]; then
    echo "Building ffmpeg dependencies..."
    # Use timeout to prevent interactive prompts from hanging
    timeout 3600 ./build-package.sh -I -d ffmpeg 2>&1 || {
        echo "Dependency build finished (may have partial success)"
    }
else
    echo "Skipping dependency build (--skip-deps specified)"
fi

# Step 7: Build additional required static libraries
echo ""
echo "=== Step 7: Build additional static libraries (libiconv, zlib) ==="
cd "$TERMUX_PACKAGES_DIR"

# Build libiconv (needed by ffmpeg for static linking)
echo "Building libiconv..."
timeout 300 ./build-package.sh -I -s -f libiconv 2>&1 || true

# Build zlib (needed by ffmpeg for static linking)
echo "Building zlib..."
timeout 300 ./build-package.sh -I -s -f zlib 2>&1 || true

# Step 8: Build ffmpeg
echo ""
echo "=== Step 8: Build ffmpeg ==="
cd "$TERMUX_PACKAGES_DIR"

if [ "$CLEAN" = true ]; then
    echo "Cleaning previous ffmpeg build..."
    rm -rf /root/.termux-build/ffmpeg
    rm -f /data/data/.built-packages/ffmpeg
fi

echo "Building ffmpeg (this may take 20-30 minutes)..."
# Use timeout and force rebuild (-f), skip deps (-s since we already built them)
timeout 2700 ./build-package.sh -I -s -f ffmpeg 2>&1 || {
    ret=$?
    if [ $ret -eq 124 ]; then
        echo "ERROR: Build timed out after 45 minutes"
        exit 1
    fi
    # Check if build actually succeeded despite non-zero exit
    if [ -f "$TERMUX_PREFIX/lib/libavcodec.a" ]; then
        echo "Build appears successful despite exit code $ret"
    else
        echo "ERROR: Build failed with exit code $ret"
        exit $ret
    fi
}

# Step 9: Verify build output
echo ""
echo "=== Step 9: Verify build output ==="
if [ ! -f "$TERMUX_PREFIX/lib/libavcodec.a" ]; then
    echo "ERROR: libavcodec.a not found at $TERMUX_PREFIX/lib/"
    echo "Build may have failed. Check logs above."
    exit 1
fi

echo "Built ffmpeg libraries:"
ls -lh "$TERMUX_PREFIX/lib/libav"*.a "$TERMUX_PREFIX/lib/libsw"*.a "$TERMUX_PREFIX/lib/libpostproc.a" 2>/dev/null || true

# Step 10: Copy to ffstream 3rdparty directory
echo ""
echo "=== Step 10: Copy libraries to ffstream 3rdparty ==="
mkdir -p "$OUTPUT_DIR/lib/pkgconfig"
mkdir -p "$OUTPUT_DIR/include"

# Copy static libraries (ffmpeg + dependencies)
cp "$TERMUX_PREFIX/lib/libav"*.a "$OUTPUT_DIR/lib/"
cp "$TERMUX_PREFIX/lib/libsw"*.a "$OUTPUT_DIR/lib/"
cp "$TERMUX_PREFIX/lib/libpostproc.a" "$OUTPUT_DIR/lib/"
cp "$TERMUX_PREFIX/lib/libiconv.a" "$OUTPUT_DIR/lib/" 2>/dev/null || true
cp "$TERMUX_PREFIX/lib/liblzma.a" "$OUTPUT_DIR/lib/" 2>/dev/null || true
cp "$TERMUX_PREFIX/lib/libz.a" "$OUTPUT_DIR/lib/" 2>/dev/null || true

# Copy headers
cp -r "$TERMUX_PREFIX/include/libav"* "$OUTPUT_DIR/include/"
cp -r "$TERMUX_PREFIX/include/libsw"* "$OUTPUT_DIR/include/"
cp -r "$TERMUX_PREFIX/include/libpostproc" "$OUTPUT_DIR/include/"

# Copy pkgconfig files
cp "$TERMUX_PREFIX/lib/pkgconfig/libav"*.pc "$OUTPUT_DIR/lib/pkgconfig/"
cp "$TERMUX_PREFIX/lib/pkgconfig/libsw"*.pc "$OUTPUT_DIR/lib/pkgconfig/"
cp "$TERMUX_PREFIX/lib/pkgconfig/libpostproc.pc" "$OUTPUT_DIR/lib/pkgconfig/"

# Step 11: Fix pkgconfig files for local build
echo ""
echo "=== Step 11: Fix pkgconfig paths ==="
cd "$OUTPUT_DIR/lib/pkgconfig"
for pc in *.pc; do
    # Update prefix path to local 3rdparty directory
    sed -i "s|prefix=/data/data/com.termux/files/usr|prefix=$OUTPUT_DIR|g" "$pc"
    sed -i "s|-L/data/data/com.termux/files/usr/lib|-L$OUTPUT_DIR/lib|g" "$pc"
    # Remove android-specific libs (should be linked dynamically, not statically)
    sed -i 's/-landroid //g; s/-lmediandk//g; s/-lcamera2ndk//g' "$pc"
done

echo "Copied to $OUTPUT_DIR"
echo ""
echo "Libraries:"
ls -lh "$OUTPUT_DIR/lib/"*.a
echo ""
echo "Headers:"
ls "$OUTPUT_DIR/include/"
echo ""
echo "Pkgconfig:"
ls "$OUTPUT_DIR/lib/pkgconfig/"

# Step 12: Setup NDK symlink
echo ""
echo "=== Step 12: Setup NDK symlink ==="
NDK_PATH=$(ls -d /root/lib/android-ndk-r28* 2>/dev/null | head -1)
if [ -n "$NDK_PATH" ]; then
    NDK_LINK="$FFSTREAM_DIR/3rdparty/arm64/android-ndk-r28-beta2"
    if [ -L "$NDK_LINK" ] || [ -d "$NDK_LINK" ]; then
        rm -rf "$NDK_LINK"
    fi
    ln -sf "$NDK_PATH" "$NDK_LINK"
    echo "Created NDK symlink: $NDK_LINK -> $NDK_PATH"
else
    echo "WARNING: Android NDK not found at /root/lib/android-ndk-r28*"
    echo "You may need to set ANDROID_NDK_HOME or create the symlink manually"
fi

echo ""
echo "=== Build Complete ==="
echo ""
echo "ffmpeg7 with MediaCodec patches has been built and installed to:"
echo "  $OUTPUT_DIR"
echo ""
echo "You can now build ffstream for Android with:"
echo "  cd $FFSTREAM_DIR && make ffstream-android-arm64-static-cgo"
echo ""
