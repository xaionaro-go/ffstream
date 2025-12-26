# FFstream E2E Tests

End-to-end tests for ffstream on Android devices using ADB.

## Prerequisites

1. **ADB Setup**: The tests require ADB connectivity. Set `ADB_SERVER_SOCKET` environment variable if your ADB server is on a different host:
   ```bash
   export ADB_SERVER_SOCKET=tcp:172.17.0.1:5037
   ```

2. **Real Device**: Connect an Android device with:
   - USB debugging enabled
   - Termux installed
   - Device authorized for ADB

3. **Emulator** (optional): For emulator tests, you need:
   - Android SDK with system images
   - KVM available for hardware acceleration
   - AVD created

## Running Tests

```bash
# Run all e2e tests
go test -v -timeout 120s ./e2e/...

# Run specific test
go test -v -timeout 60s ./e2e/... -run TestFFstreamDeployment

# Run only real device tests
go test -v ./e2e/... -run "Real|Deployment|BasicRun"

# Run only emulator tests
go test -v ./e2e/... -run "Emulator"
```

## Test Descriptions

### Device Tests (`e2e_test.go`)

- `TestListDevices` - Lists all connected ADB devices
- `TestRealDeviceConnection` - Verifies real device connectivity
- `TestEmulatorConnection` - Verifies emulator connectivity

### FFstream Tests (`ffstream_test.go`)

- `TestFFstreamDeployment` - Deploys ffstream deb to device and installs via termux
- `TestFFstreamBasicRun` - Tests `ffstream -version`
- `TestFFstreamEncodersList` - Lists available video encoders
- `TestFFstreamInputDevices` - Lists available input formats/devices
- `TestFFstreamHelp` - Tests help output
- `TestFFstreamStreamingBasic` - Basic encoding test (testsrc -> mp4)

### Emulator Tests (`emulator_test.go`)

- `TestEmulatorWithKVM` - Tests emulator functionality (requires KVM)
- `TestEmulatorConnectivity` - Tests emulator shell access

## Common Issues

### Missing Dependencies

If tests skip with "missing dependencies" error like:
```
CANNOT LINK EXECUTABLE "ffstream": library "libandroid-posix-semaphore.so" not found
```

Install the missing library in Termux and ensure `LD_LIBRARY_PATH` points to `~/lib`:
```bash
# In Termux:
pkg install libandroid-posix-semaphore pulseaudio

# Create ~/lib with symlinks (the test does this automatically)
mkdir -p ~/lib
ln -sf /data/data/com.termux/files/usr/lib/*.so* ~/lib/
```

**Important**: `LD_LIBRARY_PATH` must be set to ONLY `~/lib`, not `~/lib:/usr/lib`. Adding `/usr/lib` causes linking conflicts.

### No KVM

Emulator tests require KVM for hardware acceleration. Without KVM, emulator tests will be skipped. To check KVM:
```bash
ls -la /dev/kvm
```

### ADB Connection Issues

If tests fail with "no device found":
1. Check `adb devices` works
2. Verify ADB_SERVER_SOCKET is set correctly
3. Ensure device is authorized

## Test Architecture

The tests use a helper structure:
- `deviceTestHelper` - Wraps ADB commands for a specific device
- `termuxCmd()` - Executes commands in termux environment with proper PATH and LD_LIBRARY_PATH
- `copyDebToTermux()` - Transfers files to termux home using pipe trick (since run-as can't access sdcard directly)
