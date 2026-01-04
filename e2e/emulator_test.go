// emulator_test.go handles Android emulator lifecycle for end-to-end tests.

package e2e

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	androidSDKRoot = "/workspaces/xaionaro-go/ffstream/.Android/Sdk"
	testAVDName    = "test_avd"
)

// EmulatorManager handles Android emulator lifecycle.
type EmulatorManager struct {
	ctx     context.Context
	process *os.Process
}

// startEmulator starts the Android emulator (requires KVM for reasonable speed).
func startEmulator(ctx context.Context) (*EmulatorManager, error) {
	emulatorPath := androidSDKRoot + "/emulator/emulator"

	cmd := exec.CommandContext(ctx, emulatorPath,
		"-avd", testAVDName,
		"-no-window",
		"-no-audio",
		"-no-boot-anim",
		"-gpu", "swiftshader_indirect",
	)
	cmd.Env = append(os.Environ(),
		"ANDROID_SDK_ROOT="+androidSDKRoot,
		"ADB_SERVER_SOCKET="+adbServerSocket,
	)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &EmulatorManager{
		ctx:     ctx,
		process: cmd.Process,
	}, nil
}

// Stop stops the emulator.
func (em *EmulatorManager) Stop() error {
	if em.process == nil {
		return nil
	}
	return em.process.Kill()
}

// waitForBoot waits for the emulator to fully boot.
func (em *EmulatorManager) waitForBoot(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(em.ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if emulator is listed and booted
		stdout, _, err := adbCmd(ctx, "devices")
		if err == nil && strings.Contains(stdout, "emulator") && strings.Contains(stdout, "device") {
			// Check boot completed
			out, _, err := adbCmd(ctx, "-e", "shell", "getprop", "sys.boot_completed")
			if err == nil && strings.TrimSpace(out) == "1" {
				return nil
			}
		}

		time.Sleep(2 * time.Second)
	}
}

// checkKVMAvailable checks if KVM is available for hardware acceleration.
func checkKVMAvailable() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// TestEmulatorWithKVM tests ffstream on emulator (requires KVM).
func TestEmulatorWithKVM(t *testing.T) {
	if !checkKVMAvailable() {
		t.Skip("KVM not available - emulator would be too slow without hardware acceleration")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Log("Starting emulator...")
	em, err := startEmulator(ctx)
	if err != nil {
		t.Fatalf("Failed to start emulator: %v", err)
	}
	defer em.Stop()

	t.Log("Waiting for emulator to boot...")
	if err := em.waitForBoot(3 * time.Minute); err != nil {
		t.Fatalf("Emulator failed to boot: %v", err)
	}

	t.Log("Emulator booted successfully")

	// Get emulator device
	dev, err := getEmulator(ctx)
	if err != nil {
		t.Fatalf("Failed to get emulator device: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	// Basic connectivity test
	out, err := helper.shell("echo", "hello")
	if err != nil {
		t.Fatalf("Failed to run shell command: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("Unexpected output: %q", out)
	}

	t.Log("Emulator e2e test passed")
}

// TestEmulatorConnectivity tests basic emulator connectivity if one is already running.
func TestEmulatorConnectivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := getEmulator(ctx)
	if err != nil {
		t.Skipf("No emulator connected: %v", err)
	}

	t.Logf("Found emulator: %s", dev.Serial)

	helper := newDeviceTestHelper(t, ctx, dev)

	// Basic connectivity test
	out, err := helper.shell("echo", "emulator-test")
	if err != nil {
		t.Fatalf("Failed to run shell command: %v", err)
	}
	if !strings.Contains(out, "emulator-test") {
		t.Fatalf("Unexpected output: %q", out)
	}

	t.Log("Emulator connectivity test passed")
}
