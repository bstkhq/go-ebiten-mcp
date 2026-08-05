//go:build !linux

// A virtual gamepad is the operating system's own business, and every one of
// them does it differently. Ebitengine reads controllers from evdev on Linux,
// DirectInput and XInput on Windows, and IOKit's HID manager on macOS, so there
// is no shared mechanism to build on.
//
// The situation elsewhere, so that nobody has to go and find out:
//
//   - Windows: ViGEmBus, a signed kernel driver, presents a virtual Xbox 360 or
//     DualShock that both DirectInput and XInput see. It works well and it means
//     installing a driver, which is why it is not done here.
//   - macOS: it needs a DriverKit extension of one's own. The project that did
//     this without one has been unmaintained for years.
//
// Only the gamepad is affected. Keyboard, mouse and touch injection work
// wherever Ebitengine does, because that goes through a struct that is the same
// on every platform.
package uinput

import (
	"errors"
	"runtime"
)

// Supported reports whether this build can create a virtual gamepad at all.
const Supported = false

// Device is not creatable here.
type Device struct{}

func unsupported() error {
	switch runtime.GOOS {
	case "windows":
		return errors.New("a virtual gamepad on Windows needs the ViGEmBus driver, " +
			"which this does not use; keyboard, mouse and touch injection are unaffected")
	case "darwin":
		return errors.New("a virtual gamepad on macOS needs a DriverKit extension, " +
			"which this does not have; keyboard, mouse and touch injection are unaffected")
	default:
		return errors.New("virtual gamepads are only implemented for Linux; " +
			"keyboard, mouse and touch injection are unaffected")
	}
}

// Available says why a virtual gamepad cannot be created here.
func Available() error { return unsupported() }

// Create always fails on this platform.
func Create(Profile) (*Device, error) { return nil, unsupported() }

func (*Device) Profile() Profile          { return Profile{} }
func (*Device) Button(uint16, bool) error { return unsupported() }
func (*Device) Axis(uint16, int32) error  { return unsupported() }
func (*Device) Sync() error               { return unsupported() }
func (*Device) Close() error              { return nil }
