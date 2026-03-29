# Building ffstream for Android ARM64

## Overview

Building Go applications with CGO for Android requires careful attention to linking. The default Go CGO behavior can produce binaries that crash immediately on Android due to static linking of bionic libc functions.

## The Problem: Static vs Dynamic libc Linking

### Symptom
Binary crashes immediately with segfault during initialization:
```
signal 11 (SIGSEGV), code 1 (SEGV_MAPERR), fault addr 0x0
```

Stack trace shows crash in `getauxval` during constructor initialization.

### Root Cause
Go's CGO by default uses **internal linking** which statically links parts of bionic libc (Android's C library). When `getauxval` is statically linked from bionic, it crashes because:
1. The static version expects certain runtime structures that don't exist yet
2. Android's dynamic linker hasn't set up the auxiliary vector properly for statically-linked code

### How to Diagnose

Check if `libc.so` is a NEEDED dependency (dynamic):
```bash
readelf -d binary | grep NEEDED
```

**Broken binary** - no libc.so in NEEDED:
```
NEEDED: liblog.so
NEEDED: libandroid.so
```

**Working binary** - has libc.so in NEEDED:
```
NEEDED: liblog.so
NEEDED: libc.so      <-- This is critical
NEEDED: libandroid.so
```

Verify `getauxval` is undefined (will be resolved at runtime):
```bash
nm -D binary | grep getauxval
```

**Broken**: No output or shows address (statically linked)
**Working**: Shows `U getauxval@LIBC` (U = undefined, resolved at runtime)

## The Solution

### Key Build Flags

1. **`-linkmode=external`**: Forces Go to use external linker instead of internal linking
2. **`-Wl,-Bdynamic`**: Tells linker to prefer dynamic linking for subsequent libraries
3. **Explicit library ordering**: Put `-Wl,-Bdynamic` before `-lc` and other system libs

### Makefile Configuration

```makefile
CGO_LDFLAGS='-Wl,-Bdynamic -llog -landroid -lmediandk -lcamera2ndk -ldl -lc \
    -L$(NDK)/sysroot/usr/lib/aarch64-linux-android/35/ \
    -L$(NDK)/sysroot/usr/lib/ \
    -L$(PWD)/3rdparty/arm64/sysroot/lib'

go build -ldflags='-linkmode=external' ...
```

### Library Search Path Order

The order of `-L` paths matters:
1. **First**: NDK sysroot for API level (e.g., `/aarch64-linux-android/35/`) - contains `libc.so`
2. **Second**: NDK sysroot base - contains `libc++_shared.so`
3. **Third**: FFmpeg sysroot - contains ffmpeg and other dependencies

## Required Dynamic Libraries

A working ffstream binary for Android needs these NEEDED libraries:

| Library | Source | Purpose |
|---------|--------|---------|
| `libc.so` | NDK sysroot | Standard C library (CRITICAL) |
| `libdl.so` | NDK sysroot | Dynamic loading |
| `liblog.so` | NDK sysroot | Android logging |
| `libandroid.so` | NDK sysroot | Android APIs |
| `libmediandk.so` | NDK sysroot | Media codec APIs |
| `libcamera2ndk.so` | NDK sysroot | Camera APIs |

Optional (dynamically linked):
| Library | Source | Purpose |
|---------|--------|---------|
| `libc++_shared.so` | NDK | C++ standard library |
| `libandroid-glob.so` | NDK | glob() implementation |
| `libandroid-posix-semaphore.so` | NDK | POSIX semaphores |
| `libpulse.so` | System | PulseAudio support |

## NDK Structure

```
android-ndk-r28/toolchains/llvm/prebuilt/linux-x86_64/
├── bin/
│   ├── aarch64-linux-android35-clang    # C compiler
│   └── aarch64-linux-android35-clang++  # C++ compiler
└── sysroot/usr/lib/aarch64-linux-android/
    ├── 35/                              # API level 35 libraries
    │   ├── libc.so                      # Dynamic C library
    │   ├── liblog.so
    │   ├── libandroid.so
    │   └── ...
    └── libc++_shared.so                 # C++ runtime
```

## Testing the Binary

### Quick test:
```bash
adb push binary /data/local/tmp/ffstream
adb shell "/data/local/tmp/ffstream --version"
```

If it runs without segfault, the linking is correct.

### Full test:
```bash
adb push bin/ffstream-android-arm64 /data/local/tmp/ffstream
adb shell "chmod +x /data/local/tmp/ffstream && /data/local/tmp/ffstream --version"
```

## Troubleshooting

### "cannot locate symbol Xzs_Construct"
- Cause: libc++_shared.so version mismatch
- Solution: Ensure the correct libc++_shared.so is available on the device

### Segfault in getauxval
- Cause: Static libc linking
- Solution: Add `-linkmode=external` and `-Wl,-Bdynamic -lc`

### "cannot find -llog"
- Cause: Missing library search path
- Solution: Add `-L$(NDK)/sysroot/usr/lib/aarch64-linux-android/35/`

## References

- NDK r28 download: https://dl.google.com/android/repository/android-ndk-r28-beta2-linux.zip
- FFmpeg build script: `build/build-ffmpeg-android.sh`
- Go CGO documentation: https://pkg.go.dev/cmd/cgo
