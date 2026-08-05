//go:build linux

package uinput

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The kernel's uinput ABI, from linux/uinput.h and linux/input.h. These numbers
// are written out rather than imported because x/sys/unix does not carry them —
// and unlike Ebitengine's internals, the kernel ABI is a stability promise, so
// writing them down is a one-off rather than a maintenance burden.
const (
	uinputPath = "/dev/uinput"

	// _IOW(dir=1, size, type='U', nr): (dir<<30)|(size<<16)|(type<<8)|nr.
	uiDevCreate  = 0x5501     // _IO('U', 1)
	uiDevDestroy = 0x5502     // _IO('U', 2)
	uiSetEvBit   = 0x40045564 // _IOW('U', 100, int)
	uiSetKeyBit  = 0x40045565 // _IOW('U', 101, int)
	uiSetAbsBit  = 0x40045567 // _IOW('U', 103, int)

	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport = 0x00

	maxNameSize = 80
	absCnt      = 0x40
)

// inputEvent is what gets written to the device: 24 bytes on a 64-bit kernel.
type inputEvent struct {
	sec   int64
	usec  int64
	typ   uint16
	code  uint16
	value int32
}

// userDevice is the legacy setup struct, written to the file descriptor before
// creating the device.
//
// The newer UI_DEV_SETUP and UI_ABS_SETUP ioctls do the same job in a tidier
// way, at the cost of two more ioctl numbers to derive by hand and get subtly
// wrong. One struct with a layout fixed since 2009 is the safer trade.
type userDevice struct {
	name         [maxNameSize]byte
	id           inputID
	ffEffectsMax uint32
	absMax       [absCnt]int32
	absMin       [absCnt]int32
	absFuzz      [absCnt]int32
	absFlat      [absCnt]int32
}

type inputID struct {
	bustype uint16
	vendor  uint16
	product uint16
	version uint16
}

// Device is a live virtual gamepad. Closing it unplugs it.
type Device struct {
	file    *os.File
	profile Profile
}

// Supported reports whether this build can create a virtual gamepad at all.
const Supported = true

// Available reports whether one could be created right now, and says what is
// missing when it could not.
//
// Worth asking before trying: the failure is always permissions, and "operation
// not permitted" on its own sends people looking in the wrong place.
func Available() error {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err == nil {
		f.Close()
		return nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s does not exist; load the kernel module with `modprobe uinput`", uinputPath)
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("no permission to write %s. It is usually root-only; give it to a group "+
			"you are in with a udev rule such as\n"+
			`  KERNEL=="uinput", GROUP="input", MODE="0660", OPTIONS+="static_node=uinput"`+"\n"+
			"and make sure you can also read /dev/input/event*, which is normally group input", uinputPath)
	}
	return fmt.Errorf("opening %s: %w", uinputPath, err)
}

// Create makes the device and returns it once the kernel has it.
//
// The device exists as soon as this returns, but the game will not see it until
// its own loop notices the new node. Callers that are about to press a button
// have to wait for that; this function deliberately does not, because it knows
// nothing about anybody's game loop.
func Create(p Profile) (*Device, error) {
	if p.Name == "" {
		return nil, errors.New("a gamepad profile needs a name")
	}
	if len(p.Axes) == 0 {
		return nil, errors.New("a gamepad profile needs at least one axis; " +
			"Ebitengine ignores a device that declares no absolute axes")
	}

	if err := Available(); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(uinputPath, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", uinputPath, err)
	}

	d := &Device{file: file, profile: p}
	if err := d.declare(); err != nil {
		file.Close()
		return nil, err
	}
	return d, nil
}

// declare tells the kernel what the device can do, then creates it.
func (d *Device) declare() error {
	fd := int(d.file.Fd())

	for _, ev := range []uintptr{evKey, evAbs} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return fmt.Errorf("declaring event type %d: %w", ev, err)
		}
	}
	for _, code := range d.profile.Buttons {
		if err := ioctl(fd, uiSetKeyBit, uintptr(code)); err != nil {
			return fmt.Errorf("declaring button %#x: %w", code, err)
		}
	}
	for _, axis := range d.profile.Axes {
		if err := ioctl(fd, uiSetAbsBit, uintptr(axis.Code)); err != nil {
			return fmt.Errorf("declaring axis %#x: %w", axis.Code, err)
		}
	}

	setup := userDevice{
		id: inputID{
			bustype: d.profile.Bus,
			vendor:  d.profile.Vendor,
			product: d.profile.Product,
			version: d.profile.Version,
		},
	}
	copy(setup.name[:maxNameSize-1], d.profile.Name)

	for _, axis := range d.profile.Axes {
		if axis.Code >= absCnt {
			return fmt.Errorf("axis code %#x is out of range", axis.Code)
		}
		setup.absMin[axis.Code] = axis.Min
		setup.absMax[axis.Code] = axis.Max
		setup.absFuzz[axis.Code] = axis.Fuzz
		setup.absFlat[axis.Code] = axis.Flat
	}

	raw := (*[unsafe.Sizeof(userDevice{})]byte)(unsafe.Pointer(&setup))[:]
	if _, err := d.file.Write(raw); err != nil {
		return fmt.Errorf("describing the device: %w", err)
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return fmt.Errorf("creating the device: %w", err)
	}
	return nil
}

// Profile returns what this device claims to be.
func (d *Device) Profile() Profile { return d.profile }

// Button presses or releases one.
func (d *Device) Button(code uint16, pressed bool) error {
	var value int32
	if pressed {
		value = 1
	}
	return d.emit(evKey, code, value)
}

// Axis moves one. The value is in the range the profile declared for it.
func (d *Device) Axis(code uint16, value int32) error {
	return d.emit(evAbs, code, value)
}

// Sync publishes everything emitted since the last one.
//
// Input drivers batch: a stick moving diagonally is two axis events and one
// sync, and a reader that saw them separately would see the diagonal as two
// straight moves. Nothing reaches the game until this is called.
func (d *Device) Sync() error {
	return d.emit(evSyn, synReport, 0)
}

func (d *Device) emit(typ, code uint16, value int32) error {
	event := inputEvent{typ: typ, code: code, value: value}

	raw := (*[unsafe.Sizeof(inputEvent{})]byte)(unsafe.Pointer(&event))[:]
	if _, err := d.file.Write(raw); err != nil {
		return fmt.Errorf("emitting event type %d code %#x: %w", typ, code, err)
	}
	return nil
}

// Close unplugs the device.
func (d *Device) Close() error {
	if d.file == nil {
		return nil
	}

	err := ioctl(int(d.file.Fd()), uiDevDestroy, 0)
	closeErr := d.file.Close()
	d.file = nil

	if err != nil {
		return fmt.Errorf("destroying the device: %w", err)
	}
	return closeErr
}

func ioctl(fd int, request, arg uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, arg); errno != 0 {
		return errno
	}
	return nil
}
