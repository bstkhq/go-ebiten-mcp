package uinput

import (
	"os"
	"strings"
	"testing"
	"time"
)

// These need write access to /dev/uinput, which is root-only by default. They
// skip rather than fail without it, and say what is missing: a machine that
// cannot make virtual devices is a normal machine, and CI should not go red
// over it.

func requireUinput(t *testing.T) {
	t.Helper()

	if err := Available(); err != nil {
		t.Skipf("no virtual gamepad here: %v", err)
	}
}

func TestSDLID(t *testing.T) {
	// The default profile has to produce this exact string: it is the id
	// Ebitengine's controller database has a standard-layout mapping for, and
	// getting it wrong means the standard buttons silently stop working.
	if got, want := Xbox360().SDLID(), "030000005e0400008e02000010010000"; got != want {
		t.Errorf("Xbox360 SDL id is %s, want %s", got, want)
	}

	// A game that switches on the vendor — go-videogame does — reads bytes 8 to
	// 12 of this string, little endian. 0x2a has to survive the round trip.
	custom := Profile{Bus: 0x03, Vendor: 0x2a, Product: 0x01, Version: 0x01}
	if got := custom.SDLID()[8:12]; got != "2a00" {
		t.Errorf("vendor 0x2a encodes as %q in %s, want 2a00", got, custom.SDLID())
	}
}

// TestCreateAppearsToTheSystem is the one that matters: the kernel has to end
// up with a device that everything else can find, under the name and identity
// the profile asked for.
func TestCreateAppearsToTheSystem(t *testing.T) {
	requireUinput(t)

	profile := Xbox360()
	profile.Name = "ebitenmcp test pad"

	device, err := Create(profile)
	if err != nil {
		t.Fatalf("creating the device: %v", err)
	}
	defer device.Close()

	if !waitForDevice(profile.Name, 2*time.Second) {
		t.Fatalf("the kernel never listed a device named %q", profile.Name)
	}

	// And pressing something must not error. Whether a game sees it is the
	// business of the tests in internal/hook, which have Ebitengine running.
	if err := device.Button(BtnA, true); err != nil {
		t.Errorf("pressing a button: %v", err)
	}
	if err := device.Axis(AbsX, -32767); err != nil {
		t.Errorf("moving an axis: %v", err)
	}
	if err := device.Sync(); err != nil {
		t.Errorf("syncing: %v", err)
	}

	if err := device.Close(); err != nil {
		t.Errorf("closing: %v", err)
	}
	if waitForDevice(profile.Name, 500*time.Millisecond) {
		t.Error("the device is still listed after being closed")
	}
}

func TestCreateRejectsAProfileEbitengineWouldIgnore(t *testing.T) {
	requireUinput(t)

	// Ebitengine skips any device that declares no absolute axes, so creating
	// one would produce a gamepad nothing can see. Better to say so.
	profile := Xbox360()
	profile.Axes = nil

	if _, err := Create(profile); err == nil {
		t.Error("a profile with no axes was accepted")
	}
}

// waitForDevice looks the device up the way anything else on the system would.
func waitForDevice(name string, within time.Duration) bool {
	deadline := time.Now().Add(within)

	for time.Now().Before(deadline) {
		devices, err := os.ReadFile("/proc/bus/input/devices")
		if err == nil && strings.Contains(string(devices), `Name="`+name+`"`) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
