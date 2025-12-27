#!/bin/bash
# Build ffstream for Android arm64
# This script handles the complete build process including ffmpeg if needed
#
# Usage:
#   ./build/build-ffstream-android.sh [--rebuild-ffmpeg] [--clean]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FFSTREAM_DIR="$(dirname "$SCRIPT_DIR")"
FFMPEG_LIBS_DIR="$FFSTREAM_DIR/3rdparty/arm64/termux/data/data/com.termux/files/usr/lib"

# Parse arguments
REBUILD_FFMPEG=false
CLEAN=false
for arg in "$@"; do
    case $arg in
        --rebuild-ffmpeg)
            REBUILD_FFMPEG=true
            ;;
        --clean)
            CLEAN=true
            ;;
        --help|-h)
            echo "Usage: $0 [--rebuild-ffmpeg] [--clean]"
            echo "  --rebuild-ffmpeg  Force rebuild of ffmpeg libraries"
            echo "  --clean           Clean build"
            exit 0
            ;;
    esac
done

cd "$FFSTREAM_DIR"

echo "=== Building ffstream for Android arm64 ==="

# Step 1: Check/build ffmpeg
echo ""
echo "=== Step 1: Check ffmpeg libraries ==="
if [ ! -f "$FFMPEG_LIBS_DIR/libavcodec.a" ] || [ "$REBUILD_FFMPEG" = true ]; then
    echo "Building ffmpeg libraries..."
    FFMPEG_ARGS=""
    if [ "$CLEAN" = true ]; then
        FFMPEG_ARGS="--clean"
    fi
    "$SCRIPT_DIR/build-ffmpeg-for-android.sh" $FFMPEG_ARGS
else
    echo "ffmpeg libraries already exist at $FFMPEG_LIBS_DIR"
    ls -lh "$FFMPEG_LIBS_DIR/libav"*.a | head -3
fi

# Step 2: Check NDK
echo ""
echo "=== Step 2: Check Android NDK ==="
NDK_LINK="$FFSTREAM_DIR/3rdparty/arm64/android-ndk-r28-beta2"
if [ ! -e "$NDK_LINK" ]; then
    NDK_PATH=$(ls -d /root/lib/android-ndk-r28* 2>/dev/null | head -1)
    if [ -z "$NDK_PATH" ]; then
        echo "ERROR: Android NDK not found"
        echo "Please install Android NDK r28 to /root/lib/ or set ANDROID_NDK_HOME"
        exit 1
    fi
    ln -sf "$NDK_PATH" "$NDK_LINK"
    echo "Created NDK symlink: $NDK_LINK -> $NDK_PATH"
else
    echo "NDK available at: $NDK_LINK"
fi

# Step 3: Install pkg-config-wrapper
echo ""
echo "=== Step 3: Install pkg-config-wrapper ==="
if [ ! -f "${GOPATH:-$HOME/go}/bin/pkg-config-wrapper" ]; then
    echo "Installing pkg-config-wrapper..."
    go install github.com/xaionaro-go/pkg-config-wrapper@5dd443e6c18336416c49047e2ba0002e26a85278
else
    echo "pkg-config-wrapper already installed"
fi

# Step 4: Build ffstream
echo ""
echo "=== Step 4: Build ffstream ==="
if [ "$CLEAN" = true ]; then
    rm -f bin/ffstream-android-arm64
fi

echo "Running: make ffstream-android-arm64-static-cgo"
make ffstream-android-arm64-static-cgo

# Step 5: Verify output
echo ""
echo "=== Step 5: Verify build ==="
if [ -f "bin/ffstream-android-arm64" ]; then
    echo "SUCCESS: ffstream built successfully"
    ls -lh bin/ffstream-android-arm64
    file bin/ffstream-android-arm64
else
    echo "ERROR: ffstream binary not found"
    exit 1
fi

echo ""
echo "=== Build Complete ==="
echo "Binary: $FFSTREAM_DIR/bin/ffstream-android-arm64"
echo ""
echo "To deploy to device:"
echo "  adb push bin/ffstream-android-arm64 /data/data/com.termux/files/usr/bin/ffstream"
echo ""
