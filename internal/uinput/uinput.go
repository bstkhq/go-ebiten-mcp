// Package uinput creates a virtual gamepad the operating system treats as real.
//
// Unlike the keyboard and mouse injection in internal/hook, there is nothing
// clever here and nothing reaching into Ebitengine. The kernel makes a device,
// Ebitengine finds it the way it finds a controller somebody plugged in, and
// every layer in between — evdev, the SDL id, the standard-layout mapping —
// behaves exactly as it does for real hardware. That is worth the permissions it
// costs: it is the only part of this project that cannot drift when Ebitengine
// changes.
//
// It is Linux-only, because a virtual input device is an operating system's own
// business and each has its own. See the package comment in uinput_other.go for
// what the alternatives are elsewhere.
//
// # What this cannot do
//
// A uinput device is not a USB device. It lives under /sys/devices/virtual/input
// rather than on the USB bus, so it has no USB string descriptors — no
// manufacturer, no product, no serial — and nothing that enumerates USB will
// find it.
//
// That matters when a game talks to its controller twice: once through
// Ebitengine for buttons and axes, and once directly over USB HID for whatever
// else the hardware does. go-videogame is exactly that shape — it parses the
// vendor out of the SDL id and, for its own controllers, opens the USB device to
// drive it. A virtual pad claiming one of those vendors would send the game down
// that branch and then fail to be found, which is worse than not claiming it.
//
// So: for a game like that, choose a vendor it does not treat specially, and the
// controller works for everything input-related. A device that is genuinely on
// the USB bus needs the gadget framework with dummy_hcd, or usbip — both of
// which need kernel modules and root, and neither of which is here.
package uinput

import "fmt"

// Axis is one analogue control: a stick direction, a trigger, or half a hat.
type Axis struct {
	Code uint16
	Min  int32
	Max  int32
	Flat int32 // dead zone around the centre
	Fuzz int32 // noise the driver should filter out
}

// Profile is the identity and the shape of the device to create.
//
// All of it is configurable on purpose. Games routinely decide what a controller
// is from its vendor id — go-videogame picks a whole driver that way — so a
// virtual pad that could only claim to be an Xbox controller would send those
// games down a different path than their real hardware, and prove nothing.
type Profile struct {
	// Name is what ebiten.GamepadName returns.
	Name string

	// Bus, Vendor, Product and Version are the four numbers evdev carries.
	// There is no manufacturer string: the manufacturer is the assigned USB
	// vendor id, 0x045e being Microsoft. Ebitengine builds the SDL id from
	// these, and that id is what a game matches on.
	Bus     uint16
	Vendor  uint16
	Product uint16
	Version uint16

	// Buttons are evdev key codes. Their order does not matter: Ebitengine
	// numbers buttons by ascending code, so the indices a mapping refers to
	// follow from which codes exist, not from how they are listed here.
	Buttons []uint16

	// Axes are the absolute controls, hats included.
	Axes []Axis
}

// SDLID is the id Ebitengine will report for a device with this profile.
//
// Worth being able to compute without creating anything: it is what a game
// switches on, so being able to check it before connecting turns "the game
// ignored my gamepad" into "the game saw a different controller".
func (p Profile) SDLID() string {
	return fmt.Sprintf("%02x%02x0000%02x%02x0000%02x%02x0000%02x%02x0000",
		byte(p.Bus), byte(p.Bus>>8),
		byte(p.Vendor), byte(p.Vendor>>8),
		byte(p.Product), byte(p.Product>>8),
		byte(p.Version), byte(p.Version>>8))
}

// evdev codes, from linux/input-event-codes.h.
const (
	BtnA      = 0x130
	BtnB      = 0x131
	BtnC      = 0x132
	BtnX      = 0x133
	BtnY      = 0x134
	BtnZ      = 0x135
	BtnTL     = 0x136
	BtnTR     = 0x137
	BtnTL2    = 0x138
	BtnTR2    = 0x139
	BtnSelect = 0x13a
	BtnStart  = 0x13b
	BtnMode   = 0x13c
	BtnThumbL = 0x13d
	BtnThumbR = 0x13e

	AbsX     = 0x00
	AbsY     = 0x01
	AbsZ     = 0x02
	AbsRX    = 0x03
	AbsRY    = 0x04
	AbsRZ    = 0x05
	AbsHat0X = 0x10
	AbsHat0Y = 0x11
)

// Xbox360 is the default profile, and it is the default for a reason beyond
// familiarity: this vendor and product pair has a complete standard-layout
// mapping in Ebitengine's controller database, so IsStandardGamepadButtonPressed
// works without anyone writing a mapping.
//
// The codes below are the ones a real Xbox 360 pad declares. Since Ebitengine
// numbers buttons by ascending evdev code, declaring the same set produces the
// same b0..b10 and a0..a5 the database's mapping refers to. Change the set and
// the indices move, which is exactly what a custom controller wants.
func Xbox360() Profile {
	const stick, trigger = 32767, 255

	return Profile{
		Name:    "Microsoft X-Box 360 pad",
		Bus:     0x0003, // USB
		Vendor:  0x045e,
		Product: 0x028e,
		Version: 0x0110,
		Buttons: []uint16{
			BtnA, BtnB, BtnX, BtnY,
			BtnTL, BtnTR,
			BtnSelect, BtnStart, BtnMode,
			BtnThumbL, BtnThumbR,
		},
		Axes: []Axis{
			{Code: AbsX, Min: -stick, Max: stick, Flat: 128, Fuzz: 16},
			{Code: AbsY, Min: -stick, Max: stick, Flat: 128, Fuzz: 16},
			{Code: AbsZ, Min: 0, Max: trigger},
			{Code: AbsRX, Min: -stick, Max: stick, Flat: 128, Fuzz: 16},
			{Code: AbsRY, Min: -stick, Max: stick, Flat: 128, Fuzz: 16},
			{Code: AbsRZ, Min: 0, Max: trigger},
			{Code: AbsHat0X, Min: -1, Max: 1},
			{Code: AbsHat0Y, Min: -1, Max: 1},
		},
	}
}
