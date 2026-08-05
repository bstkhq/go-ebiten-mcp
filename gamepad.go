package ebitenmcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bstkhq/go-ebiten-mcp/internal/uinput"
	"github.com/hajimehoshi/ebiten/v2"
)

// Gamepads are virtual controllers the operating system creates and Ebitengine
// discovers on its own.
//
// This is the one part of the library that does not reach into Ebitengine at
// all, so it is also the one part that cannot break when Ebitengine changes —
// and it keeps working in a build with -tags ebitenmcp_nohook, where the
// keyboard and mouse injection is compiled out.
//
// It covers what Ebitengine sees: buttons, axes, hats, the name and the SDL id,
// and the standard layout that follows from them. It does not cover a game that
// also opens its controller as a USB device, because a uinput device is not on
// the USB bus. See internal/uinput for what that means in practice.
type Gamepads struct {
	rt *Runtime

	// connecting serialises Connect end to end; mu guards devices and is held
	// for the whole of any operation on one.
	connecting sync.Mutex

	mu      sync.Mutex
	devices map[ebiten.GamepadID]*uinput.Device
}

// GamepadIdentity is what a virtual controller claims to be.
//
// The layout is deliberately not part of it. Every virtual pad has an Xbox 360
// controller's buttons and axes, because Ebitengine's controller database has a
// complete standard mapping for that layout — so a game asking for the standard
// buttons gets them. A pad with a layout nothing has a mapping for would answer
// no to IsStandardGamepadLayoutAvailable, which is the first thing most games
// check, and would prove nothing about the game's real handling.
//
// What a game does switch on is the vendor and the product, and that is worth
// choosing: a controller that could only ever claim to be an Xbox pad would
// send those games down a different path than their own hardware.
//
// A zero field means the Xbox 360 pad's own, so setting only Vendor is a
// sensible thing to do.
type GamepadIdentity struct {
	Name    string
	Bus     uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

// DefaultGamepadIdentity is the Xbox 360 pad, spelled out for a caller who
// wants to see what the zero value means.
func DefaultGamepadIdentity() GamepadIdentity {
	p := uinput.Xbox360()
	return GamepadIdentity{
		Name: p.Name, Bus: p.Bus, Vendor: p.Vendor, Product: p.Product, Version: p.Version,
	}
}

// SDLID is the identifier Ebitengine builds from the four numbers, and the one
// a game matches on.
func (i GamepadIdentity) SDLID() string { return i.layout().SDLID() }

// layout is the identity with the Xbox 360 buttons and axes around it, which is
// the only shape a device is ever created with.
func (i GamepadIdentity) layout() uinput.Profile {
	p := uinput.Xbox360()

	if i.Name != "" {
		p.Name = i.Name
	}
	if i.Bus != 0 {
		p.Bus = i.Bus
	}
	if i.Vendor != 0 {
		p.Vendor = i.Vendor
	}
	if i.Product != 0 {
		p.Product = i.Product
	}
	if i.Version != 0 {
		p.Version = i.Version
	}
	return p
}

// Gamepads returns the handle for virtual controllers.
func (r *Runtime) Gamepads() *Gamepads {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.gamepads == nil {
		r.gamepads = &Gamepads{rt: r, devices: map[ebiten.GamepadID]*uinput.Device{}}
	}
	return r.gamepads
}

// GamepadsAvailable reports why virtual controllers cannot be created, or nil
// when they can.
func GamepadsAvailable() error { return uinput.Available() }

// Connect creates a controller and returns the id the game will know it by.
//
// It waits for the game to notice. The kernel makes the device immediately, but
// Ebitengine only picks it up while polling inside its own loop, so returning
// any earlier would hand back an id for a controller nothing can see yet — and
// the first button press would land nowhere with no error to explain it.
func (g *Gamepads) Connect(ctx context.Context, identity GamepadIdentity) (ebiten.GamepadID, error) {
	// One at a time, end to end. Connect works out which id is new by comparing
	// the list before with the list after, so two of them running together both
	// see the same gap, both claim the same id, and the second overwrites the
	// first — leaving a uinput device with no handle, which nothing can
	// disconnect and which outlives the process.
	g.connecting.Lock()
	defer g.connecting.Unlock()

	before, err := g.visible(ctx)
	if err != nil {
		return 0, err
	}

	device, err := uinput.Create(identity.layout())
	if err != nil {
		return 0, err
	}

	id, err := g.waitForNew(ctx, before, identity)
	if err != nil {
		device.Close()
		return 0, err
	}

	g.mu.Lock()
	g.devices[id] = device
	g.mu.Unlock()

	return id, nil
}

// waitForNew watches for an id that was not there before and that claims to be
// the controller just created.
func (g *Gamepads) waitForNew(ctx context.Context, before map[ebiten.GamepadID]bool, identity GamepadIdentity) (ebiten.GamepadID, error) {
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		now, err := g.visible(ctx)
		if err != nil {
			return 0, err
		}

		for id := range now {
			if before[id] {
				continue
			}
			// More than one controller can appear at once on a busy machine,
			// so check it is ours rather than taking whatever is new.
			if ebiten.GamepadSDLID(id) == identity.SDLID() {
				return id, nil
			}
		}

		if err := g.rt.WaitTicks(ctx, 1); err != nil {
			return 0, err
		}
	}

	return 0, fmt.Errorf("the game never saw the controller. It was created, so this is "+
		"usually permissions: check that /dev/input/event* is readable, which is normally "+
		"group input. Its id would have been %s", identity.SDLID())
}

// visible asks the game which controllers it can see, from inside the loop,
// because that is where Ebitengine's gamepad state is coherent.
func (g *Gamepads) visible(ctx context.Context) (map[ebiten.GamepadID]bool, error) {
	ids := map[ebiten.GamepadID]bool{}

	if err := g.rt.Do(ctx, func() {
		for _, id := range ebiten.AppendGamepadIDs(nil) {
			ids[id] = true
		}
	}); err != nil {
		return nil, err
	}
	return ids, nil
}

// Disconnect unplugs a controller this package created.
func (g *Gamepads) Disconnect(id ebiten.GamepadID) error {
	g.mu.Lock()
	device := g.devices[id]
	delete(g.devices, id)
	g.mu.Unlock()

	if device == nil {
		return fmt.Errorf("gamepad %d was not created here, so it is not ours to unplug", id)
	}
	return device.Close()
}

// Button presses or releases one, by evdev code.
func (g *Gamepads) Button(id ebiten.GamepadID, code uint16, pressed bool) error {
	return g.withDevice(id, func(device *uinput.Device) error {
		if err := device.Button(code, pressed); err != nil {
			return err
		}
		return device.Sync()
	})
}

// Axis moves one, in the range its profile declared.
func (g *Gamepads) Axis(id ebiten.GamepadID, code uint16, value int32) error {
	return g.withDevice(id, func(device *uinput.Device) error {
		if err := device.Axis(code, value); err != nil {
			return err
		}
		return device.Sync()
	})
}

// Apply sends several changes as one movement.
//
// Input drivers batch events and publish them together, so a stick pushed
// diagonally is two axis events and one sync. Sending them separately would
// show up as two straight movements, which is not what the player did.
func (g *Gamepads) Apply(id ebiten.GamepadID, buttons map[uint16]bool, axes map[uint16]int32) error {
	return g.withDevice(id, func(device *uinput.Device) error {
		for code, pressed := range buttons {
			if err := device.Button(code, pressed); err != nil {
				return err
			}
		}
		for code, value := range axes {
			if err := device.Axis(code, value); err != nil {
				return err
			}
		}

		// One Sync for the whole batch: the kernel treats everything between two
		// of them as a single report, which is how a stick and a button pressed
		// together arrive in the same tick rather than in two.
		return device.Sync()
	})
}

// Identity returns what a connected controller claims to be.
func (g *Gamepads) Identity(id ebiten.GamepadID) (GamepadIdentity, error) {
	p, err := g.layout(id)
	return GamepadIdentity{
		Name: p.Name, Bus: p.Bus, Vendor: p.Vendor, Product: p.Product, Version: p.Version,
	}, err
}

// layout is the whole device description, buttons and axes included, which only
// the axis arithmetic needs and which no caller can build one of.
func (g *Gamepads) layout(id ebiten.GamepadID) (uinput.Profile, error) {
	var profile uinput.Profile

	err := g.withDevice(id, func(device *uinput.Device) error {
		profile = device.Profile()
		return nil
	})
	return profile, err
}

// Connected lists the controllers created here.
func (g *Gamepads) Connected() []ebiten.GamepadID {
	g.mu.Lock()
	defer g.mu.Unlock()

	ids := make([]ebiten.GamepadID, 0, len(g.devices))
	for id := range g.devices {
		ids = append(ids, id)
	}
	return ids
}

// withDevice runs fn against a controller this package created, holding the lock
// for the whole operation.
//
// Looking the device up and then using it after the lock is dropped — which is
// what this used to do — races with Disconnect: the device can be closed and its
// file set to nil in between. Holding it also stops two callers interleaving
// their events before a Sync, which the kernel would deliver as one incoherent
// report.
func (g *Gamepads) withDevice(id ebiten.GamepadID, fn func(*uinput.Device) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	device := g.devices[id]
	if device == nil {
		return fmt.Errorf("gamepad %d was not created here; only virtual controllers "+
			"can be driven, and a real one is driven by whoever is holding it", id)
	}
	return fn(device)
}

// Close unplugs everything created here.
//
// A virtual device outlives the process that made it unless it is destroyed, so
// skipping this would leave phantom controllers on the machine.
func (g *Gamepads) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()

	for id, device := range g.devices {
		device.Close()
		delete(g.devices, id)
	}
}
