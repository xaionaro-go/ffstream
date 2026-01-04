// e2e_test.go provides end-to-end tests for ffstream.

// Package e2e provides end-to-end tests for ffstream.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	adbServerSocket = "tcp:172.17.0.1:5037"
	testTimeout     = 60 * time.Second
)

// DeviceInfo holds information about an Android device.
type DeviceInfo struct {
	Serial      string
	Product     string
	Model       string
	Device      string
	TransportID string
	IsEmulator  bool
}

// adbCmd runs an adb command and returns stdout, stderr, and error.
func adbCmd(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "adb", args...)
	cmd.Env = append(os.Environ(), "ADB_SERVER_SOCKET="+adbServerSocket)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// adbCmdWithSerial runs an adb command targeting a specific device.
func adbCmdWithSerial(ctx context.Context, serial string, args ...string) (string, string, error) {
	fullArgs := append([]string{"-s", serial}, args...)
	return adbCmd(ctx, fullArgs...)
}

// listDevices returns a list of connected Android devices.
func listDevices(ctx context.Context) ([]DeviceInfo, error) {
	stdout, _, err := adbCmd(ctx, "devices", "-l")
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}

	var devices []DeviceInfo
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "List of devices") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "device" {
			continue
		}

		dev := DeviceInfo{
			Serial:     fields[0],
			IsEmulator: strings.HasPrefix(fields[0], "emulator-"),
		}

		for _, field := range fields[2:] {
			kv := strings.SplitN(field, ":", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "product":
				dev.Product = kv[1]
			case "model":
				dev.Model = kv[1]
			case "device":
				dev.Device = kv[1]
			case "transport_id":
				dev.TransportID = kv[1]
			}
		}
		devices = append(devices, dev)
	}

	return devices, nil
}

// getDevice returns a device matching the predicate or an error.
func getDevice(ctx context.Context, predicate func(DeviceInfo) bool) (*DeviceInfo, error) {
	devices, err := listDevices(ctx)
	if err != nil {
		return nil, err
	}
	for _, dev := range devices {
		if predicate(dev) {
			return &dev, nil
		}
	}
	return nil, fmt.Errorf("no device found matching criteria")
}

// getRealDevice returns the first connected real (non-emulator) device.
func getRealDevice(ctx context.Context) (*DeviceInfo, error) {
	return getDevice(ctx, func(d DeviceInfo) bool { return !d.IsEmulator })
}

// getEmulator returns the first connected emulator.
func getEmulator(ctx context.Context) (*DeviceInfo, error) {
	return getDevice(ctx, func(d DeviceInfo) bool { return d.IsEmulator })
}

// TestListDevices verifies we can list connected devices.
func TestListDevices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	devices, err := listDevices(ctx)
	if err != nil {
		t.Fatalf("listDevices failed: %v", err)
	}

	t.Logf("Found %d device(s):", len(devices))
	for _, dev := range devices {
		t.Logf("  - Serial=%s Model=%s IsEmulator=%v", dev.Serial, dev.Model, dev.IsEmulator)
	}
}

// TestRealDeviceConnection verifies connection to a real phone.
func TestRealDeviceConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	t.Logf("Real device found: Serial=%s Model=%s", dev.Serial, dev.Model)

	// Verify we can run a command on the device
	stdout, _, err := adbCmdWithSerial(ctx, dev.Serial, "shell", "echo", "hello")
	if err != nil {
		t.Fatalf("Failed to run command on device: %v", err)
	}
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("Unexpected output: %q", stdout)
	}
	t.Logf("Device shell works: %s", strings.TrimSpace(stdout))
}

// TestEmulatorConnection verifies connection to an emulator.
func TestEmulatorConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	dev, err := getEmulator(ctx)
	if err != nil {
		t.Skipf("No emulator connected: %v", err)
	}

	t.Logf("Emulator found: Serial=%s", dev.Serial)

	// Verify we can run a command on the emulator
	stdout, _, err := adbCmdWithSerial(ctx, dev.Serial, "shell", "echo", "hello")
	if err != nil {
		t.Fatalf("Failed to run command on emulator: %v", err)
	}
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("Unexpected output: %q", stdout)
	}
	t.Logf("Emulator shell works: %s", strings.TrimSpace(stdout))
}
