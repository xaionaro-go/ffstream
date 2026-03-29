#!/bin/bash
# Build FFmpeg and all dependencies for Android using Docker.
# Runs on the HOST machine.
#
# Usage:
#   ./build/docker-build.sh --arch=arm64|x86_64 [--clean] [--rebuild-image]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
IMAGE_NAME="ffstream-android-builder"

###############################################################################
# Parse arguments
###############################################################################

ARCH=""
CLEAN=false
REBUILD_IMAGE=false

for arg in "$@"; do
    case $arg in
        --arch=*)
            ARCH="${arg#--arch=}"
            ;;
        --clean)
            CLEAN=true
            ;;
        --rebuild-image)
            REBUILD_IMAGE=true
            ;;
        --help|-h)
            echo "Usage: $0 --arch=arm64|x86_64 [--clean] [--rebuild-image]"
            echo ""
            echo "Options:"
            echo "  --arch=arm64|x86_64  Target architecture (required)"
            echo "  --clean              Clean build (remove previous build artifacts)"
            echo "  --rebuild-image      Force rebuild of the Docker image"
            exit 0
            ;;
        *)
            echo "ERROR: Unknown argument: $arg"
            echo "Usage: $0 --arch=arm64|x86_64 [--clean] [--rebuild-image]"
            exit 1
            ;;
    esac
done

if [ -z "$ARCH" ]; then
    echo "ERROR: --arch is required"
    echo "Usage: $0 --arch=arm64|x86_64 [--clean] [--rebuild-image]"
    exit 1
fi

case "$ARCH" in
    arm64|x86_64) ;;
    *)
        echo "ERROR: Unsupported architecture: $ARCH (must be arm64 or x86_64)"
        exit 1
        ;;
esac

OUTPUT_DIR="${PROJECT_DIR}/3rdparty/${ARCH}/sysroot"

###############################################################################
# Step 1: Build Docker image
###############################################################################

echo "=== Step 1: Docker image ==="

image_exists() {
    docker image inspect "$IMAGE_NAME" >/dev/null 2>&1
}

if [ "$REBUILD_IMAGE" = true ] || ! image_exists; then
    echo "Building Docker image '${IMAGE_NAME}'..."
    echo "  (This includes downloading the Android NDK — may take a while on first run)"
    docker build \
        -f "${SCRIPT_DIR}/Dockerfile.android-builder" \
        -t "$IMAGE_NAME" \
        "$PROJECT_DIR"
else
    echo "Docker image '${IMAGE_NAME}' already exists (use --rebuild-image to force rebuild)"
fi

###############################################################################
# Step 2: Run the build inside Docker
###############################################################################

echo ""
echo "=== Step 2: Building for ${ARCH} ==="

mkdir -p "$OUTPUT_DIR"

DOCKER_ARGS=(
    --rm
    -v "${OUTPUT_DIR}:/output"
    -v "${SCRIPT_DIR}/patches:/build/patches:ro"
)

BUILD_ARGS=("--arch=${ARCH}")
if [ "$CLEAN" = true ]; then
    BUILD_ARGS+=("--clean")
fi

echo "Output directory: ${OUTPUT_DIR}"
echo "Running: docker run ${IMAGE_NAME} ${BUILD_ARGS[*]}"
echo ""

docker run "${DOCKER_ARGS[@]}" "$IMAGE_NAME" "${BUILD_ARGS[@]}"

###############################################################################
# Step 3: Summary
###############################################################################

echo ""
echo "============================================================"
echo "  Docker build complete — Android ${ARCH}"
echo "============================================================"
echo ""
echo "Output directory: ${OUTPUT_DIR}"
echo ""
echo "Libraries:"
ls -lh "$OUTPUT_DIR/lib/"*.a 2>/dev/null || echo "  (none)"
echo ""
echo "Shared libraries:"
ls -lh "$OUTPUT_DIR/lib/"*.so 2>/dev/null || echo "  (none)"
echo ""
