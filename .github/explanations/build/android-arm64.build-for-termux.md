# Building ffstream for Android ARM64 (Termux Environment)

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
    -L$(TERMUX_LIBS)'

go build -ldflags='-linkmode=external' ...
```

### Library Search Path Order

The order of `-L` paths matters:
1. **First**: NDK sysroot for API level (e.g., `/aarch64-linux-android/35/`) - contains `libc.so`
2. **Second**: NDK sysroot base - contains `libc++_shared.so`
3. **Third**: Termux libraries - contains ffmpeg and other dependencies

## Required Dynamic Libraries

A working ffstream binary for Android/Termux needs these NEEDED libraries:

| Library | Source | Purpose |
|---------|--------|---------|
| `libc.so` | NDK sysroot | Standard C library (CRITICAL) |
| `libdl.so` | NDK sysroot | Dynamic loading |
| `liblog.so` | NDK sysroot | Android logging |
| `libandroid.so` | NDK sysroot | Android APIs |
| `libmediandk.so` | NDK sysroot | Media codec APIs |
| `libcamera2ndk.so` | NDK sysroot | Camera APIs |

Optional (for full Termux compatibility):
| Library | Source | Purpose |
|---------|--------|---------|
| `libc++_shared.so` | NDK/Termux | C++ standard library |
| `libandroid-glob.so` | Termux | glob() implementation |
| `libandroid-posix-semaphore.so` | Termux | POSIX semaphores |
| `libpulse.so` | Termux | PulseAudio support |

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

### Quick test (outside Termux):
```bash
adb push binary /data/local/tmp/ffstream
adb shell "/data/local/tmp/ffstream --version"
```

If it runs without segfault, the linking is correct.

### Full test (in Termux environment):
```bash
adb shell "run-as com.termux sh -c 'LD_LIBRARY_PATH=/data/data/com.termux/files/home/lib /path/to/ffstream --version'"
```

Note: May see `Xzs_Construct` errors if libc++_shared.so versions mismatch between NDK and Termux.

## Comparison: Docker Build vs Local Build

| Aspect | Docker (termux-packages) | Local (NDK) |
|--------|-------------------------|-------------|
| Toolchain | Termux's patched clang | Stock NDK clang |
| Libraries | Full Termux ecosystem | NDK sysroot only |
| Compatibility | Best | Good for simple cases |
| Build time | Longer (docker overhead) | Faster |
| Setup | Requires Docker | Just NDK download |

## Troubleshooting

### "cannot locate symbol Xzs_Construct"
- Cause: libc++_shared.so version mismatch
- Solution: Ensure LD_LIBRARY_PATH points to Termux's libs first

### Segfault in getauxval
- Cause: Static libc linking
- Solution: Add `-linkmode=external` and `-Wl,-Bdynamic -lc`

### "cannot find -llog"
- Cause: Missing library search path
- Solution: Add `-L$(NDK)/sysroot/usr/lib/aarch64-linux-android/35/`

## References

- NDK r28 download: https://dl.google.com/android/repository/android-ndk-r28-beta2-linux.zip
- Termux packages: https://github.com/termux/termux-packages
- Go CGO documentation: https://pkg.go.dev/cmd/cgo
